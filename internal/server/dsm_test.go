package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dsmTestApp(t *testing.T) (*App, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := NewDSM(filepath.Join(dir, "data"), "test-dsm")
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "authenticate.cgi")
	// Run a real child process with the CGI environment, including proxy IPs.
	script := `#!/bin/sh
test "$REMOTE_ADDR" = 192.0.2.3 || exit 1
test "$SERVER_ADDR" = 192.0.2.2 || exit 1
test "$SERVER_PORT" = 5001 || exit 1
test "$HTTPS" = on || exit 1
test "$HTTP_USER_AGENT" = test-browser || exit 1
test "$HTTP_X_SYNO_TOKEN" = test-synotoken || exit 1
test "$HTTP_COOKIE" = 'id=valid-session' || exit 1
test -z "$INHERITED_TEST_SECRET" || exit 1
printf '%s' nas-admin
`
	if err = os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.dsm.helper = helper
	a.dsm.admin = func(name string) bool { return name == "nas-admin" }
	return a, a.Handler(), helper
}

func dsmRequest(method, path, csrf string) *http.Request {
	r := httptest.NewRequest(method, "https://nas.test:5001"+path, bytes.NewBufferString(`{"language":"ru-RU"}`))
	r.RemoteAddr = "127.0.0.1:49000"
	r.Header.Set("Cookie", "id=valid-session")
	r.Header.Set("X-Plex-Helper-Client-IP", "192.0.2.3")
	r.Header.Set("X-Plex-Helper-Server-IP", "192.0.2.2")
	r.Header.Set("X-Plex-Helper-Server-Port", "5001")
	r.Header.Set("X-Plex-Helper-Protocol", "https")
	r.Header.Set("User-Agent", "test-browser")
	r.Header.Set("X-Syno-Token", "test-synotoken")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	return r
}

func dsmCall(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDSMSessionAuthorizationAndRevocation(t *testing.T) {
	t.Setenv("INHERITED_TEST_SECRET", "do-not-pass-this-to-helper")
	a, h, helper := dsmTestApp(t)
	w := dsmCall(h, dsmRequest("GET", "/api/state", ""))
	var state struct {
		Authenticated, SetupRequired bool
		AuthMode, User, CSRF         string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Authenticated || state.SetupRequired || state.AuthMode != "dsm" || state.User != "nas-admin" || state.CSRF == "" {
		t.Fatal(w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("DSM mode issued an independent login cookie")
	}
	if _, err := os.Stat(filepath.Join(a.Store.dir, "setup-token")); !os.IsNotExist(err) {
		t.Fatal("setup credential created")
	}
	for _, path := range []string{"/api/login", "/api/setup"} {
		if dsmCall(h, dsmRequest("POST", path, "")).Code != 404 {
			t.Fatal("local authentication fallback")
		}
	}
	if dsmCall(h, dsmRequest("POST", "/api/settings", "")).Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	if dsmCall(h, dsmRequest("POST", "/api/settings", state.CSRF)).Code != 200 {
		t.Fatal("authenticated DSM request failed")
	}
	r := dsmRequest("POST", "/api/settings", state.CSRF)
	r.Header.Set("Origin", "https://evil.test")
	if dsmCall(h, r).Code != 403 {
		t.Fatal("cross-origin write accepted")
	}
	// Expired/revoked DSM session immediately stops reads and writes, even with
	// the previously valid CSRF token. There is no independent login lifetime.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ method, path string }{{"GET", "/api/journals"}, {"POST", "/api/settings"}} {
		if dsmCall(h, dsmRequest(spec.method, spec.path, state.CSRF)).Code != 401 {
			t.Fatal("revoked DSM session accepted")
		}
	}
}

func TestDSMRejectsUntrustedProxyAndNonAdministrator(t *testing.T) {
	a, h, helper := dsmTestApp(t)
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.RemoteAddr = "192.0.2.3:40000" },
		func(r *http.Request) { r.Header.Del("X-Plex-Helper-Client-IP") },
		func(r *http.Request) { r.Header.Set("X-Plex-Helper-Server-Port", "invalid") },
		func(r *http.Request) { r.Header.Set("Cookie", "id=expired-session") },
	} {
		r := dsmRequest("GET", "/api/journals", "")
		mutate(r)
		if dsmCall(h, r).Code != 401 {
			t.Fatal("untrusted request accepted")
		}
	}
	a.dsm.admin = func(string) bool { return false }
	if dsmCall(h, dsmRequest("GET", "/api/journals", "")).Code != 401 {
		t.Fatal("non-administrator accepted")
	}
	a.dsm.admin = func(name string) bool { return name == "nas-admin" }
	os.Remove(helper)
	if dsmCall(h, dsmRequest("GET", "/api/journals", "")).Code != 401 {
		t.Fatal("missing DSM helper failed open")
	}
}

func TestDSMListenAndPrefixAssets(t *testing.T) {
	for _, addr := range []string{":8650", "0.0.0.0:8650", "192.168.1.115:8650", "127.0.0.1:0", "localhost:8650"} {
		if ValidDSMListen(addr) {
			t.Fatalf("public/ambiguous bind accepted: %s", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8650", "[::1]:8650"} {
		if !ValidDSMListen(addr) {
			t.Fatalf("loopback rejected: %s", addr)
		}
	}
	_, h, _ := dsmTestApp(t)
	w := dsmCall(h, dsmRequest("GET", "/", ""))
	for _, path := range []string{"style.css", "app.js", "tmdb.svg"} {
		if strings.Contains(w.Body.String(), `="/`+path+`"`) {
			t.Fatal("root-relative resource escapes DSM prefix")
		}
	}
}
