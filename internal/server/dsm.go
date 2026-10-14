package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"time"
)

// DSM nginx supplies the CGI environment through overwritten proxy headers.
// Identity is always obtained from Synology's helper, never a request header.
type dsmAuthenticator struct {
	helper string
	admin  func(string) bool
	slots  chan struct{}
}

func newDSMAuthenticator() *dsmAuthenticator {
	return &dsmAuthenticator{helper: "/usr/syno/synoman/webman/modules/authenticate.cgi", admin: dsmAdministrator, slots: make(chan struct{}, 8)}
}

func dsmAdministrator(name string) bool {
	u, err := user.Lookup(name)
	if err != nil {
		return false
	}
	g, err := user.LookupGroup("administrators")
	if err != nil {
		return false
	}
	groups, err := u.GroupIds()
	if err != nil {
		return false
	}
	for _, id := range groups {
		if id == g.Gid {
			return true
		}
	}
	return false
}

type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 1024 {
		return 0, errors.New("invalid DSM authentication output")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (d *dsmAuthenticator) authenticate(r *http.Request) (string, string) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(ip).IsLoopback() {
		return "", "proxy_required"
	}
	clientIP := net.ParseIP(r.Header.Get("X-Plex-Helper-Client-IP"))
	serverIP := net.ParseIP(r.Header.Get("X-Plex-Helper-Server-IP"))
	port, err := strconv.Atoi(r.Header.Get("X-Plex-Helper-Server-Port"))
	cookie, cookieErr := r.Cookie("id")
	if clientIP == nil || serverIP == nil || err != nil || port < 1 || port > 65535 {
		return "", "proxy_environment_invalid"
	}
	if cookieErr != nil || cookie.Value == "" {
		return "", "session_cookie_missing"
	}
	select {
	case d.slots <- struct{}{}:
		defer func() { <-d.slots }()
	default:
		return "", "authentication_busy"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.helper)
	cmd.WaitDelay = time.Second
	// Do not inherit package environment, credentials, or caller-controlled CGI vars.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "GATEWAY_INTERFACE=CGI/1.1", "SERVER_PROTOCOL=HTTP/1.1",
		"HTTP_COOKIE=" + r.Header.Get("Cookie"), "REMOTE_ADDR=" + clientIP.String(), "SERVER_ADDR=" + serverIP.String(),
		"SERVER_PORT=" + strconv.Itoa(port), "HTTP_HOST=" + r.Host, "REQUEST_METHOD=" + r.Method,
		"REQUEST_URI=/webman/3rdparty/plex-helper" + r.URL.RequestURI(), "QUERY_STRING=" + r.URL.RawQuery,
		"HTTP_X_SYNO_TOKEN=" + r.Header.Get("X-Syno-Token"),
		"HTTP_USER_AGENT=" + r.UserAgent(), "HTTP_REFERER=" + r.Referer()}
	if r.Header.Get("X-Plex-Helper-Protocol") == "https" {
		cmd.Env = append(cmd.Env, "HTTPS=on")
	}
	var output boundedOutput
	cmd.Stdout = &output
	if cmd.Run() != nil {
		return "", "helper_failed"
	}
	name := strings.TrimSpace(string(output.data))
	if name == "" || strings.ContainsAny(name, "\x00\r\n") {
		return "", "session_rejected"
	}
	if !d.admin(name) {
		return "", "administrator_required"
	}
	return name, ""
}

func (a *App) dsmSession(r *http.Request) (session, bool) {
	name, authError := a.dsm.authenticate(r)
	if authError != "" {
		return session{AuthError: authError}, false
	}
	cookie, _ := r.Cookie("id")
	mac := hmac.New(sha256.New, []byte(a.csrfKey))
	mac.Write([]byte(name + "\x00" + cookie.Value))
	return session{User: name, CSRF: hex.EncodeToString(mac.Sum(nil))}, true
}

// DSM's nginx is the only network entry point; package data never receives a
// public listener, even if an obsolete service-listen file is present.
func ValidDSMListen(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535 && net.ParseIP(host).IsLoopback()
}
