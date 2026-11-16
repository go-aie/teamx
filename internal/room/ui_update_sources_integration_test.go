//go:build integration || e2e

package room

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUIUpdateSourcesGitLabAuthorizationStagesAndExpiry(t *testing.T) {
	g := newGitlabLoginHarness(t)
	m := g.h.m
	sub := updateSub(t, m.updates, 1, humanCaller.ID, ServiceInfo{})
	other := updateSub(t, m.updates, 2, "other-operator", ServiceInfo{})
	v, cookie := g.login(t)
	sourceTopics(t, sub, "authorization")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	g.before = func(_ *http.Request) { close(entered); <-release }
	callback := make(chan int, 1)
	go func() { callback <- g.callback(v, cookie, nil).Code }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("exchange did not start")
	}
	sourceTopics(t, sub, "authorization")
	once.Do(func() { close(release) })
	if code := <-callback; code != 303 {
		t.Fatal(code)
	}
	sourceTopics(t, sub, "authorization", "platforms")
	if result := g.status(t, v, cookie); result.Status != "succeeded" {
		t.Fatal(result.Status)
	}
	if g.callback(v, cookie, nil).Code == 303 || g.posts.Load() != 1 {
		t.Fatal("notification/status repeated login")
	}
	updates, _ := other.take()
	for _, u := range updates {
		if u.Topic == "authorization" {
			t.Fatal("authorization leaked operator", u)
		}
	}
	// The same expiration installation boundary used by the existing deadline
	// worker signals the owner, without exchanging or restarting authorization.
	g2 := newGitlabLoginHarness(t)
	sub2 := updateSub(t, g2.h.m.updates, 1, humanCaller.ID, ServiceInfo{})
	v2, cookie2 := g2.login(t)
	sourceTopics(t, sub2, "authorization")
	g2.h.m.mu.Lock()
	g2.h.m.finishGitLabAuthLocked(g2.h.m.gitlabAuthSessions[v2.SessionID], "expired", "")
	g2.h.m.mu.Unlock()
	sourceTopics(t, sub2, "authorization")
	if result := g2.status(t, v2, cookie2); result.Status != "expired" || g2.posts.Load() != 0 {
		t.Fatal(result.Status, g2.posts.Load())
	}
}

func TestUIUpdateSourcesLarkNativeQRCodeAndCancellation(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	profile, dir := roomAuthProfile(t)
	sub := updateSub(t, m.updates, 1, humanCaller.ID, ServiceInfo{})
	start := authHTTP(t, m, "login", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Profile: &profile})
	req := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, SessionID: start.SessionID}
	view := authWait(t, m, req, "waiting")
	if view.QRCodeURL == "" || view.URL == "" {
		t.Fatal("native QR/link not installed")
	}
	sourceTopics(t, sub, "authorization")
	view = authHTTP(t, m, "cancel", req)
	if view.Status != "cancelled" {
		t.Fatal(view.Status)
	}
	sourceTopics(t, sub, "authorization")
	if _, err := os.Stat(filepath.Join(dir, "auth-logout")); !os.IsNotExist(err) {
		t.Fatal("notification/cancellation started logout", err)
	}
}

func TestUIUpdateSourcesScionNativeAuthorizationTerminal(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	sub := updateSub(t, m.updates, 1, humanCaller.ID, ServiceInfo{})
	executable := scionAuthProgram(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(executable), "mode"), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	id, _ := newOpaqueID()
	startScionAuth(t, m, executable, id, "alice-pat")
	sourceTopics(t, sub, "authorization")
	deadline, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, err := os.Stat(filepath.Join(filepath.Dir(executable), "started")); err == nil {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("login did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	response := managerHTTP(m, "/v1/platforms/scion/cancel", scionAuthBody(m, map[string]any{"session_id": id}))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	sourceTopics(t, sub, "authorization")
	if view := awaitScionAuth(t, m, id); view.Status != "cancelled" {
		t.Fatal(view.Status)
	}
}
