//go:build e2e

package room

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

type observedSSOProbe struct {
	authn.Provider
	report func(error)
}

func (p observedSSOProbe) Complete(ctx context.Context, values url.Values) (authn.Identity, error) {
	id, err := p.Provider.Complete(ctx, values)
	p.report(err)
	return id, err
}

// This bounded real SSO probe admits no business operation or local user. It
// obtains the server-proven stable subject for an administrator's allowlist.
// Credentials remain in memory; the private receipt contains identity only.
func TestOpenCSGSSOLoopback(t *testing.T) {
	path := os.Getenv("TEAMX_OPENCSG_ACCEPTANCE_CONFIG")
	if path == "" {
		t.Skip("set TEAMX_OPENCSG_ACCEPTANCE_CONFIG to a private absolute TOML configuration")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("acceptance configuration must be absolute")
	}
	_, info, err := readSettingsFile(path)
	if err != nil || info == nil || info.Mode().Perm() != 0600 {
		t.Fatal("acceptance configuration must be a private 0600 regular file")
	}
	c, err := loadStartupConfig(path, true)
	if err != nil || c.Deployment.Authentication.Provider != "opencsg" {
		t.Fatal("acceptance configuration requires OpenCSG authentication")
	}
	provider, err := c.Deployment.Authentication.provider()
	if err != nil {
		t.Fatal(err)
	}
	runSSOLoopback(t, provider, "OpenCSG")
}

func runSSOLoopback(t *testing.T, provider authn.Provider, label string) {
	t.Helper()
	home := os.Getenv("TEAMX_SSO_ACCEPTANCE_HOME")
	if home == "" {
		t.Skip("set a fresh absolute TEAMX_SSO_ACCEPTANCE_HOME and complete local browser login")
	}
	if !filepath.IsAbs(home) {
		t.Fatal("acceptance home must be absolute")
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("acceptance home must not already exist")
	}
	if err := makeDurableDir(home); err != nil {
		t.Fatal(err)
	}
	listen := os.Getenv("TEAMX_SSO_ACCEPTANCE_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:18088"
	}
	if err := validateManagedListen(listen); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	boundary := &serverAccess{internal: origin, public: origin}
	if public := os.Getenv("TEAMX_SSO_ACCEPTANCE_ORIGIN"); public != "" {
		u, err := url.Parse(public)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			t.Fatal("acceptance public origin must be a canonical HTTPS origin")
		}
		origin = public
		// The probe still binds loopback. A local TLS terminator or socket
		// proxy must preserve Host and send X-Forwarded-Proto: https.
		boundary.public, boundary.activeMode = public, accessProxy
		boundary.proxies = map[netip.Addr]bool{netip.MustParseAddr("127.0.0.1"): true, netip.MustParseAddr("::1"): true}
	}
	var mu sync.Mutex
	var completed bool
	ack := make(chan struct{})
	var once sync.Once
	writer := &snapshotWriter{ops: defaultSnapshotOps()}
	observed := observedSSOProbe{Provider: provider, report: func(err error) {
		if err != nil {
			t.Logf("SSO callback received; protected identity exchange rejected: %v (credentials omitted)", err)
		} else {
			t.Log("SSO callback received; protected identity exchange verified")
		}
	}}
	login, err := newExternalLogin(observed, origin, func(ctx context.Context, w http.ResponseWriter, id authn.Identity) error {
		if err := writer.save(filepath.Join(home, "identity-receipt.json"), id, maxPrivateFileBytes); err != nil {
			return err
		}
		mu.Lock()
		completed = true
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 24 << 10}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actual, ok := boundary.requestOrigin(r); !ok || actual != origin {
			accessError(w, 403, "invalid_origin")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		// A navigation form POST with no-referrer sends Origin: null in
		// Chromium. Keep its same-origin proof; the sensitive SSO routes
		// override this with no-referrer before processing the callback.
		w.Header().Set("Referrer-Policy", "same-origin")
		if r.URL.Path == externalLoginPath || r.URL.Path == externalCallbackPath {
			login.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/" && r.URL.Path != "/access" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		mu.Lock()
		done := completed
		mu.Unlock()
		if done {
			fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>TeamX SSO</title><p>%s 登录验证已完成，可以关闭此页面。</p>", label)
			once.Do(func() { close(ack) })
			return
		}
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>TeamX SSO</title><p>TeamX 本机隔离登录验证</p><form method=post action='/v1/access/sso/login'><button>使用 %s 登录</button></form>", label)
		if r.URL.Query().Get("sso") == "failed" {
			fmt.Fprint(w, "<p>登录验证未完成，请重试。</p>")
		}
	})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Logf("Open local browser: %s/", origin)
	select {
	case <-ack:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(30 * time.Minute):
		_ = server.Close()
		t.Fatal("browser login not completed before deadline; no business access granted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := writer.closeWriter(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("Verified external identity receipt: %s", filepath.Join(home, "identity-receipt.json"))
}
