#!/usr/bin/env python3
"""Check real package contents, reproducibility and service hooks on a builder."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def run(command, **kwargs):
    return subprocess.run(command, check=True, cwd=ROOT, **kwargs)


def contents(archive):
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:*") as source:
        members = source.getmembers()
        assert len({m.name for m in members}) == len(members), "Duplicate archive path"
        for member in members:
            assert member.isfile(), f"Unexpected member type: {member.name}"
            assert not member.name.startswith("/") and ".." not in Path(member.name).parts
            assert member.uid == member.gid == 0
            assert member.mtime == int(os.environ.get("SOURCE_DATE_EPOCH", "1791936000"))
        return {m.name: (source.extractfile(m).read(), m.mode) for m in members}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default=os.environ.get("GO", "go"))
    args = parser.parse_args()
    environment = os.environ.copy()
    environment.update(CGO_ENABLED="0", GOTOOLCHAIN="local", GOWORK="off")
    with tempfile.TemporaryDirectory(prefix="plex-helper-package-test-") as temporary:
        directory = Path(temporary)
        outputs = [directory / "first", directory / "second"]
        for output in outputs:
            run([sys.executable, str(ROOT / "scripts/build-spk.py"), "--go", args.go, "--output", str(output)], env=environment)
        packages = [next(p.glob("*.spk")) for p in outputs]
        assert packages[0].read_bytes() == packages[1].read_bytes(), "SPK builds differ"
        files = contents(packages[0].read_bytes())
        required = {"INFO", "package.tgz", "PACKAGE_ICON.PNG", "PACKAGE_ICON_256.PNG", "conf/privilege", "conf/resource", "scripts/start-stop-status", "scripts/preinst", "scripts/postinst", "scripts/preupgrade", "scripts/postupgrade", "scripts/preuninst", "scripts/postuninst", "WIZARD_UIFILES/install_uifile"}
        assert required <= files.keys()
        assert json.loads(files["conf/privilege"][0])["defaults"]["run-as"] == "package"
        assert json.loads(files["conf/privilege"][0])["username"] == "plex-helper"
        info = files["INFO"][0].decode()
        assert 'arch="x86_64"' in info and 'os_min_ver="7.2-72806"' in info
        assert f'checksum="{hashlib.md5(files["package.tgz"][0]).hexdigest()}"' in info
        assert "install_dep_packages" not in info
        payload = contents(files["package.tgz"][0])
        assert payload["bin/plex-helper"][1] == 0o755
        config = json.loads(payload["ui/config"][0])
        assert config[".url"]["com.damuraiz.plexhelper"]["url"] == "3rdparty/plex-helper/launch.html"
        assert json.loads(files["conf/resource"][0])["port-config"]["protocol-file"] in payload
        assert 'dst.ports="8650/tcp"' in payload["port_conf/plex-helper.sc"][0].decode()
        assert len(json.loads(files["WIZARD_UIFILES/install_uifile"][0])) == 1
        for name, (data, mode) in files.items():
            if name.startswith("scripts/"):
                assert mode == 0o755
                run(["sh", "-n"], input=data, stdout=subprocess.DEVNULL)

        # Run the actual application, never a media library; all paths are temp.
        target = directory / "target"
        (target / "bin").mkdir(parents=True)
        local_binary = target / "bin/plex-helper"
        native_environment = environment.copy()
        for key in ("GOOS", "GOARCH", "GOAMD64"):
            native_environment.pop(key, None)
        run([args.go, "build", "-trimpath", "-buildvcs=false", "-o", str(local_binary), "./cmd/plex-helper"], env=native_environment)
        data_dir = directory / "data"
        hooks = ROOT / "packaging/scripts"
        hook_environment = environment.copy()
        hook_environment.update(SYNOPKG_PKGVAR=str(data_dir), SYNOPKG_PKGDEST=str(target), SYNOPKG_PKG_STATUS="INSTALL", wizard_admin_password="package-test-password-12345")
        def hook(name, argument=None, expected=0):
            command = ["sh", str(hooks / name)] + ([argument] if argument else [])
            result = subprocess.run(command, cwd=ROOT, env=hook_environment, capture_output=True, text=True)
            assert result.returncode == expected, f"{name} {argument}: {result.returncode}\n{result.stdout}{result.stderr}"
            return result
        hook("preinst")
        hook("postinst")
        assert (data_dir / "config.json").exists()
        config_before = (data_dir / "config.json").read_bytes()
        assert b"package-test-password-12345" not in config_before
        # Ask the OS for a free loopback port, then release it before the service.
        import socket
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        (data_dir / "service-listen").write_text(f"127.0.0.1:{port}\n")
        hook("start-stop-status", "status", expected=3)
        hook("start-stop-status", "prestart")
        try:
            hook("start-stop-status", "start")
            first_pid = (data_dir / "service.pid").read_text()
            hook("start-stop-status", "status")
            actual_birth = (data_dir / "service.birth").read_bytes()
            (data_dir / "service.birth").write_text("wrong-process-start\n")
            hook("start-stop-status", "status", expected=1)
            (data_dir / "service.birth").write_bytes(actual_birth)
            hook("start-stop-status", "start")
            assert (data_dir / "service.pid").read_text() == first_pid, "Duplicate daemon"
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/", timeout=3) as response:
                assert response.status == 200
            if Path("/proc").is_dir():
                # Initial plaintext wizard password must not reach server env.
                server_environment = (Path("/proc") / first_pid.strip() / "environ").read_bytes()
                assert b"wizard_admin_password=" not in server_environment
            hook("start-stop-status", "stop")
            hook("start-stop-status", "status", expected=3)
            hook("start-stop-status", "stop")
            (data_dir / "service.pid").write_text("999999999\n")
            hook("start-stop-status", "status", expected=1)
            hook("start-stop-status", "stop")
            # A forged live PID must not be killed, even if owned by this user.
            unrelated = subprocess.Popen(["sleep", "60"])
            try:
                (data_dir / "service.pid").write_text(f"{unrelated.pid}\n")
                hook("start-stop-status", "status", expected=1)
                hook("start-stop-status", "stop")
                assert unrelated.poll() is None
            finally:
                unrelated.terminate()
                unrelated.wait()
            hook_environment["SYNOPKG_PKG_STATUS"] = "UPGRADE"
            for name in ("preupgrade", "preuninst", "postuninst", "preinst", "postinst", "postupgrade"):
                hook(name)
            assert (data_dir / "config.json").read_bytes() == config_before, "Upgrade resets settings"
            hook("start-stop-status", "start")
            hook("start-stop-status", "stop")
            hook_environment["SYNOPKG_PKG_STATUS"] = "UNINSTALL"
            hook("preuninst")
            hook("postuninst")
            assert (data_dir / "config.json").read_bytes() == config_before, "Hook removed settings"
            hook_environment.update(SYNOPKG_PKG_STATUS="INSTALL", wizard_admin_password="different-install-password-12345")
            hook("postinst")
            assert (data_dir / "config.json").read_bytes() == config_before, "Reinstall resets credentials"
            hook_environment.update(SYNOPKG_PKG_STATUS="INSTALL", wizard_admin_password="short")
            hook("preinst", expected=1)
        finally:
            hook("start-stop-status", "stop")
        print("PASS: deterministic SPK, static ELF, DSM metadata, real service lifecycle, stale/foreign PID protection, upgrade/uninstall-hook retention")
        print("NOT VERIFIED: Package Center installation, DSM wizard/ACL/desktop integration, NAS boot or DSM upgrade behavior")


if __name__ == "__main__":
    main()
