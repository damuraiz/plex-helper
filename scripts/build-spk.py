#!/usr/bin/env python3
"""Build a deterministic DSM 7 package; Python is only needed on the builder."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import tarfile
import tempfile
import zlib

ROOT = Path(__file__).resolve().parents[1]
GO_VERSION = "go1.25.14"
DEFAULT_EPOCH = 1791936000  # 2026-10-14 00:00:00 UTC; never use wall-clock time.


def archive(files, epoch, compress=False):
    output = io.BytesIO()
    destination = gzip.GzipFile(fileobj=output, mode="wb", filename="", mtime=epoch, compresslevel=9) if compress else output
    with tarfile.open(fileobj=destination, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        for name, (data, mode) in sorted(files.items()):
            item = tarfile.TarInfo(name)
            item.size, item.mode, item.mtime = len(data), mode, epoch
            item.uid = item.gid = 0
            item.uname = item.gname = "root"
            tar.addfile(item, io.BytesIO(data))
    if compress:
        destination.close()
    return output.getvalue()


def png(size):
    """Small original app icon, generated without image tools or dependencies."""
    def chunk(kind, value):
        return struct.pack(">I", len(value)) + kind + value + struct.pack(">I", zlib.crc32(kind + value) & 0xffffffff)
    rows = bytearray()
    for y in range(size):
        rows.append(0)
        for x in range(size):
            # Teal folder over navy with a gold play marker.
            folder = size // 6 <= x < size * 5 // 6 and size // 3 <= y < size * 3 // 4
            tab = size // 6 <= x < size // 2 and size // 4 <= y < size // 3
            play = size * 2 // 5 <= x < size * 3 // 5 and abs(y - size // 2) <= (size * 3 // 5 - x) // 2
            rows.extend((243, 193, 92, 255) if play else ((46, 178, 164, 255) if folder or tab else (17, 28, 42, 255)))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(bytes(rows), 9)) + chunk(b"IEND", b"")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", default="0.2.1-1")
    parser.add_argument("--output", type=Path, default=ROOT / "dist")
    parser.add_argument("--go", default=os.environ.get("GO", "go"))
    parser.add_argument("--binary", type=Path, help="Use an already built static linux/amd64 binary (does not run Go).")
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+\.\d+-\d+", args.version):
        parser.error("version must be X.Y.Z-N")
    epoch = int(os.environ.get("SOURCE_DATE_EPOCH", DEFAULT_EPOCH))
    if not 0 <= epoch <= 0xffffffff:
        parser.error("SOURCE_DATE_EPOCH must fit the gzip timestamp")
    with tempfile.TemporaryDirectory(prefix="plex-helper-build-") as temporary:
        binary = args.binary
        if binary is None:
            environment = os.environ.copy()
            environment.update(CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOAMD64="v1", GOTOOLCHAIN="local", GOWORK="off", GOENV="off", GOFLAGS="", GOEXPERIMENT="", TZ="UTC")
            version = subprocess.check_output([args.go, "version"], env=environment, cwd=ROOT, text=True).strip()
            if version.split()[2] != GO_VERSION:
                raise SystemExit(f"Expected {GO_VERSION}; got {version}")
            binary = Path(temporary) / "plex-helper"
            subprocess.run([args.go, "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w -buildid=", "-o", str(binary), "./cmd/plex-helper"], cwd=ROOT, env=environment, check=True)
        binary_data = binary.read_bytes()
        if len(binary_data) < 64 or binary_data[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", binary_data, 18)[0] != 62:
            raise SystemExit("Expected a little-endian ELF64 x86-64 binary, not a macOS executable")
        # Refuse an ELF interpreter (PT_INTERP), which would require a loader on NAS.
        phoff, phsize, phnum = struct.unpack_from("<Q", binary_data, 32)[0], struct.unpack_from("<H", binary_data, 54)[0], struct.unpack_from("<H", binary_data, 56)[0]
        if any(struct.unpack_from("<I", binary_data, phoff + i * phsize)[0] == 3 for i in range(phnum)):
            raise SystemExit("Binary has PT_INTERP; rebuild with CGO_ENABLED=0")
        payload = {"bin/plex-helper": (binary_data, 0o755)}
        if (ROOT / "THIRD_PARTY_NOTICES.md").exists():
            payload["THIRD_PARTY_NOTICES.md"] = ((ROOT / "THIRD_PARTY_NOTICES.md").read_bytes(), 0o644)
        if (ROOT / "LICENSE").is_file():
            payload["LICENSE"] = ((ROOT / "LICENSE").read_bytes(), 0o644)
        for path in sorted((ROOT / "packaging/target").rglob("*")):
            if path.is_file():
                payload[path.relative_to(ROOT / "packaging/target").as_posix()] = (path.read_bytes(), 0o644)
        for size in (16, 24, 32, 48, 64, 72, 256):
            payload[f"ui/images/plex-helper_{size}.png"] = (png(size), 0o644)
        package_tgz = archive(payload, epoch, compress=True)
        info = (ROOT / "packaging/INFO.in").read_text().replace("@VERSION@", args.version).replace("@CHECKSUM@", hashlib.md5(package_tgz).hexdigest()).encode()
        outer = {"INFO": (info, 0o644), "package.tgz": (package_tgz, 0o644), "PACKAGE_ICON.PNG": (png(64), 0o644), "PACKAGE_ICON_256.PNG": (png(256), 0o644)}
        for folder in ("conf", "scripts"):
            for path in sorted((ROOT / "packaging" / folder).rglob("*")):
                if path.is_file():
                    outer[path.relative_to(ROOT / "packaging").as_posix()] = (path.read_bytes(), 0o755 if folder == "scripts" else 0o644)
        args.output.mkdir(parents=True, exist_ok=True)
        destination = args.output / f"plex-helper-x86_64-{args.version}.spk"
        destination.write_bytes(archive(outer, epoch))
        checksum = hashlib.sha256(destination.read_bytes()).hexdigest()
        destination.with_suffix(".spk.sha256").write_text(f"{checksum}  {destination.name}\n")
        print(f"{destination}\nSHA256 {checksum}")


if __name__ == "__main__":
    main()
