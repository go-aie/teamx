//go:build e2e

package room

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-aie/teamx/internal/authn/opencsg"
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
	listener, err := net.Listen("tcp4", "127.0.0.1:18088")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	provider, err := opencsg.New("https://opencsg.com", "https://hub.opencsg.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var completed bool
	ack := make(chan struct{})
	var once sync.Once
	writer := &snapshotWriter{ops: defaultSnapshotOps()}
	observed := observedSSOProbe{Provider: provider, report: func(err error) {
		if err != nil {
			t.Log("SSO callback received; protected identity exchange rejected (credentials omitted)")
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
		peer, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.Host != listener.Addr().String() || !net.ParseIP(peer).IsLoopback() {
			accessError(w, 403, "invalid_origin")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
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
			fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>TeamX SSO</title><p>OpenCSG 登录验证已完成，可以关闭此页面。</p>")
			once.Do(func() { close(ack) })
			return
		}
		fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>TeamX SSO</title><p>TeamX 本机隔离登录验证</p><form method=post action='/v1/access/sso/login'><button>使用 OpenCSG 登录</button></form>")
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
