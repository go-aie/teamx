package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

type renewableProviderFixture struct {
	codeProviderFixture
	clock                          atomic.Int64
	exchanges, refreshes, verifies atomic.Int32
	refresh                        func(context.Context, authn.Grant) (authn.Grant, error)
	verify                         func(context.Context, authn.Grant) (authn.Identity, error)
}

func newRenewableProviderFixture() *renewableProviderFixture {
	p := &renewableProviderFixture{}
	p.clock.Store(time.Now().UnixNano())
	return p
}
func (p *renewableProviderFixture) now() time.Time          { return time.Unix(0, p.clock.Load()).UTC() }
func (p *renewableProviderFixture) advance(d time.Duration) { p.clock.Add(int64(d)) }
func (p *renewableProviderFixture) CompleteGrant(ctx context.Context, v url.Values) (authn.Login, error) {
	p.exchanges.Add(1)
	id, err := p.Complete(ctx, v)
	if err != nil {
		return authn.Login{}, err
	}
	now := p.now()
	// Short-lived tokens exercise expiry clamping independently of the default check window.
	g := authn.Grant{Provider: id.Provider, Authority: id.Authority, Subject: id.Subject, ClientID: "teamx-client", AccessToken: "access-" + id.Subject, RefreshToken: "refresh-" + id.Subject, AccessExpires: now.Add(15 * time.Minute), Nonce: "original-nonce", Scope: "openid profile offline_access"}
	return authn.Login{Identity: id, Grant: g, VerifiedAt: now, VerifiedUntil: now.Add(15 * time.Minute)}, nil
}
func (p *renewableProviderFixture) Refresh(ctx context.Context, g authn.Grant) (authn.Grant, error) {
	n := p.refreshes.Add(1)
	if p.refresh != nil {
		return p.refresh(ctx, g)
	}
	g.AccessToken = fmt.Sprintf("access-%s-%d", g.Subject, n)
	g.RefreshToken = fmt.Sprintf("refresh-%s-%d", g.Subject, n)
	g.AccessExpires = p.now().Add(time.Hour)
	return g, nil
}
func (p *renewableProviderFixture) Verify(ctx context.Context, g authn.Grant) (authn.Identity, error) {
	p.verifies.Add(1)
	if p.verify != nil {
		return p.verify(ctx, g)
	}
	return authn.Identity{Provider: g.Provider, Authority: g.Authority, Subject: g.Subject}, nil
}
func renewableHost(t *testing.T, home string, p *renewableProviderFixture, age string) *spaceServer {
	t.Helper()
	s := hostedFixtureWithProvider(t, home, externalAuthConfig{Provider: "opencsg", Issuer: "https://opencsg.example", ClientID: "teamx-client", AllowAllAuthenticated: true, SessionMaxAge: age}, p)
	s.access.now = p.now
	return s
}
func sessionFor(t *testing.T, s *spaceServer, c *http.Cookie) accessSession {
	t.Helper()
	s.access.mu.Lock()
	defer s.access.mu.Unlock()
	entry, ok := s.access.sessions[tokenDigest(c.Value)]
	if !ok {
		t.Fatal("session missing")
	}
	return entry
}
func authenticateRenewal(t *testing.T, s *spaceServer, c *http.Cookie) (accessSession, error) {
	t.Helper()
	return s.access.renewal.authenticate(context.Background(), tokenDigest(c.Value))
}
func waitRenewal(t *testing.T, s *spaceServer, c *http.Cookie) {
	t.Helper()
	s.access.mu.Lock()
	f := s.access.renewal.flights[tokenDigest(c.Value)]
	s.access.mu.Unlock()
	if f != nil {
		select {
		case <-f.done:
		case <-time.After(5 * time.Second):
			t.Fatal("renewal did not settle")
		}
	}
}
func savedRenewal(t *testing.T, s *spaceServer) renewableSnapshot {
	t.Helper()
	data, err := os.ReadFile(s.access.sessionPath())
	if err != nil {
		t.Fatal(err)
	}
	var saved renewableSnapshot
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestAccessSessionRenewalPersistsRotationAndAbsoluteDeadline(t *testing.T) {
	home := t.TempDir()
	p := newRenewableProviderFixture()
	s := renewableHost(t, home, p, "")
	cookie, _, _ := codeLogin(t, s, "alice")
	original := sessionFor(t, s, cookie)
	if cookie.MaxAge < int((7*24*time.Hour-time.Second)/time.Second) {
		t.Fatal("cookie is not absolute seven days")
	}
	p.advance(16 * time.Minute)
	w := hostedRequest(s, "/v1/access/session", nil, cookie, "")
	if w.Code != 200 {
		t.Fatalf("renew %d %s", w.Code, w.Body.String())
	}
	renewed := sessionFor(t, s, cookie)
	if renewed.expires != original.expires || p.refreshes.Load() != 1 || p.verifies.Load() != 1 || p.exchanges.Load() != 1 || renewed.renewal.Grant.RefreshToken == original.renewal.Grant.RefreshToken {
		t.Fatal("renewal changed deadline or failed rotation")
	}
	if strings.Contains(w.Body.String(), "refresh-") || strings.Contains(w.Body.String(), "access_token") || !strings.Contains(w.Body.String(), "renew_after") {
		t.Fatal("HTTP metadata leaked credentials or omitted schedule")
	}
	if savedRenewal(t, s).Version != 3 {
		t.Fatal("v3 not committed")
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	s = renewableHost(t, home, p, "720h")
	if sessionFor(t, s, cookie).expires != original.expires {
		t.Fatal("restart extended session")
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	s = renewableHost(t, home, p, "1h")
	if !sessionFor(t, s, cookie).expires.Equal(original.renewal.Created.Add(time.Hour)) {
		t.Fatal("shorter policy ignored")
	}
	p.advance(time.Hour)
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 401 {
		t.Fatal("absolute deadline bypassed", w.Code)
	}
}

func TestAccessSessionRenewalSharesRefreshAndWaiterCancellation(t *testing.T) {
	p := newRenewableProviderFixture()
	s := renewableHost(t, t.TempDir(), p, "")
	cookie, _, space := codeLogin(t, s, "alice")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.refresh = func(ctx context.Context, g authn.Grant) (authn.Grant, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
			return g, ctx.Err()
		}
		g.AccessExpires = p.now().Add(time.Hour)
		return g, nil
	}
	p.advance(16 * time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "https://teamx.example/s/"+space+"/v1/setup", nil).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:2"
	r.AddCookie(cookie)
	canceled := make(chan struct{})
	go func() { s.ServeHTTP(httptest.NewRecorder(), r); close(canceled) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	<-canceled
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := authenticateRenewal(t, s, cookie); errs <- err }()
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if p.refreshes.Load() != 1 || p.verifies.Load() != 1 {
		t.Fatal("shared refresh duplicated")
	}
}

func TestAccessSessionRenewalSavesGrantBeforeTemporaryUserinfoFailure(t *testing.T) {
	home := t.TempDir()
	p := newRenewableProviderFixture()
	s := renewableHost(t, home, p, "")
	cookie, _, _ := codeLogin(t, s, "alice")
	p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
		return authn.Identity{}, &authn.Failure{Kind: authn.Temporary}
	}
	p.advance(16 * time.Minute)
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 200 {
		t.Fatal("temporary userinfo failure blocked the fixed grace window", w.Code, w.Body.String())
	}
	current := sessionFor(t, s, cookie)
	if p.refreshes.Load() != 1 || current.renewal.Grant.RefreshToken != "refresh-alice-1" || !current.renewal.VerifiedUntil.Before(p.now()) {
		t.Fatal("latest grant lost or permission extended")
	}
	p.advance(authenticationGrace)
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 503 || !strings.Contains(w.Body.String(), "authentication_temporarily_unavailable") {
		t.Fatal("outage extended grace or logged out", w.Code, w.Body.String())
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	p.verify = nil
	s = renewableHost(t, home, p, "")
	if _, err := authenticateRenewal(t, s, cookie); err != nil {
		t.Fatal(err)
	}
	if p.refreshes.Load() != 1 {
		t.Fatal("restart reused old refresh instead of verifying latest access token")
	}
}

func TestAccessSessionRenewalRevocationWinsInflightRefresh(t *testing.T) {
	for _, action := range []string{"logout", "disable", "replace", "invalid_grant", "wrong_subject"} {
		t.Run(action, func(t *testing.T) {
			p := newRenewableProviderFixture()
			s := renewableHost(t, t.TempDir(), p, "")
			cookie, subject, _ := codeLogin(t, s, "alice")
			if action == "invalid_grant" {
				p.refresh = func(context.Context, authn.Grant) (authn.Grant, error) {
					return authn.Grant{}, &authn.Failure{Kind: authn.InvalidGrant}
				}
			}
			if action == "wrong_subject" {
				p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
					return authn.Identity{Provider: "opencsg", Authority: "https://opencsg.example", Subject: "bob"}, nil
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			if action != "invalid_grant" && action != "wrong_subject" {
				p.refresh = func(ctx context.Context, g authn.Grant) (authn.Grant, error) {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return g, ctx.Err()
					}
					g.AccessExpires = p.now().Add(time.Hour)
					return g, nil
				}
			}
			p.advance(16 * time.Minute)
			done := make(chan error, 1)
			go func() { _, err := authenticateRenewal(t, s, cookie); done <- err }()
			if action != "invalid_grant" && action != "wrong_subject" {
				<-entered
				switch action {
				case "logout":
					if w := hostedRequest(s, "/v1/access/logout", struct{}{}, cookie, ""); w.Code != 200 {
						t.Fatal(w.Code)
					}
				case "disable":
					if _, err := s.access.users.setStatus(subject.UserID, "disabled"); err != nil {
						t.Fatal(err)
					}
					s.fenceUser(subject.UserID)
				case "replace":
					p.advance(time.Second)
					login, _ := p.CompleteGrant(context.Background(), url.Values{"code": {"alice"}})
					if _, err := s.access.renewal.install(login, subject, tokenDigest(cookie.Value)); err != nil {
						t.Fatal(err)
					}
				}
				close(release)
			}
			if err := <-done; !errors.Is(err, errLoginRequired) {
				t.Fatal("old refresh retained permission", err)
			}
			if _, ok := s.access.browserSubject(&http.Request{Header: http.Header{"Cookie": {cookie.String()}}}); ok {
				t.Fatal("revoked cookie resurrected")
			}
		})
	}
}

func TestAccessSessionRenewalCapacityAndIndependentUsers(t *testing.T) {
	p := newRenewableProviderFixture()
	s := renewableHost(t, t.TempDir(), p, "")
	bob, _, _ := codeLogin(t, s, "bob")
	var first *http.Cookie
	for i := 0; i < 5; i++ {
		p.advance(time.Millisecond)
		cookie, _, _ := codeLogin(t, s, "alice")
		if i == 0 {
			first = cookie
		}
	}
	if w := hostedRequest(s, "/v1/access/session", nil, first, ""); w.Code != 401 {
		t.Fatal("oldest owner session retained", w.Code)
	}
	if w := hostedRequest(s, "/v1/access/session", nil, bob, ""); w.Code != 200 {
		t.Fatal("another user evicted", w.Code)
	}
	p.advance(16 * time.Minute)
	var wg sync.WaitGroup
	s.access.mu.Lock()
	keys := make([][32]byte, 0, len(s.access.sessions))
	for key := range s.access.sessions {
		keys = append(keys, key)
	}
	s.access.mu.Unlock()
	for _, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.access.renewal.authenticate(context.Background(), key); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(savedRenewal(t, s).Sessions) != 5 {
		t.Fatal("concurrent commits lost another session")
	}
}

func TestAccessSessionRenewalLegacyUpgradeAndCorruption(t *testing.T) {
	for _, mode := range []string{"legacy", "corrupt", "permissions", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			p := newRenewableProviderFixture()
			s := renewableHost(t, home, p, "")
			cookie, subject, _ := codeLogin(t, s, "alice")
			path := s.access.sessionPath()
			if err := s.closeWithTimeout(); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "legacy":
				saved := accessSessionSnapshot{2, s.access.sessionBinding(), map[string]savedAccessSession{fmt.Sprintf("%x", tokenDigest(cookie.Value)): {subject, p.now().Add(time.Minute)}}}
				data, _ := json.Marshal(saved)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(home, "other.json")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "legacy" {
				s = renewableHost(t, home, p, "")
				if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 401 {
					t.Fatal("legacy cookie retained")
				}
				if savedRenewal(t, s).Version != 3 {
					t.Fatal("legacy not replaced")
				}
				return
			}
			before, _ := os.ReadFile(path)
			_, err := newHostedSpaceServer(context.Background(), s.access.config, s.assembly.config, s.assembly.factories, io.Discard)
			if err == nil {
				t.Fatal("unsafe snapshot accepted")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("corrupt evidence rewritten")
			}
		})
	}
}

func TestAccessSessionRenewalStorageFailureAndUncertainRestart(t *testing.T) {
	for _, mode := range []string{"write", "rename", "directory_sync", "logout"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			p := newRenewableProviderFixture()
			s := renewableHost(t, home, p, "")
			alice, _, _ := codeLogin(t, s, "alice")
			bob, _, _ := codeLogin(t, s, "bob")
			original := s.access.sessionStore.ops
			switch mode {
			case "write", "logout":
				s.access.sessionStore.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("injected private write error") }
			case "rename":
				s.access.sessionStore.ops.rename = func(from, to string) error {
					if err := original.rename(from, to); err != nil {
						return err
					}
					return errors.New("injected post-rename error")
				}
			case "directory_sync":
				s.access.sessionStore.ops.syncDir = func(string) error { return errors.New("injected directory sync error") }
			}
			var w *httptest.ResponseRecorder
			if mode == "logout" {
				w = hostedRequest(s, "/v1/access/logout", struct{}{}, alice, "")
			} else {
				p.advance(16 * time.Minute)
				w = hostedRequest(s, "/v1/access/session", nil, alice, "")
			}
			if w.Code != 503 || !strings.Contains(w.Body.String(), "session_storage_unavailable") || len(w.Result().Cookies()) != 0 {
				t.Fatal("storage failure acknowledged auth or logout", w.Code, w.Body.String())
			}
			if w := hostedRequest(s, "/v1/access/session", nil, alice, ""); w.Code == 200 {
				t.Fatal("failed session retained permission")
			}
			if mode == "write" {
				before := p.refreshes.Load()
				if w := hostedRequest(s, "/v1/access/session", nil, alice, ""); w.Code != 503 {
					t.Fatal("rotation loss not quarantined", w.Code)
				}
				if p.refreshes.Load() != before {
					t.Fatal("failed save retried old rotating token")
				}
			}
			if mode == "rename" || mode == "directory_sync" {
				if w := hostedRequest(s, "/v1/access/session", nil, bob, ""); w.Code != 503 {
					t.Fatal("uncertain store still authorized another browser", w.Code)
				}
				s.access.sessionStore.ops = original
				if err := s.closeWithTimeout(); err != nil {
					t.Fatal(err)
				}
				s = renewableHost(t, home, p, "")
				if _, err := authenticateRenewal(t, s, alice); err != nil {
					t.Fatal("controlled reopen failed", err)
				}
				if sessionFor(t, s, alice).renewal.Grant.RefreshToken != "refresh-alice-1" {
					t.Fatal("reopen did not recover rotated grant")
				}
			} else {
				s.access.sessionStore.ops = original
			}
		})
	}
}

func TestAccessSessionRenewalSlowCommitDoesNotHoldAuthenticationLock(t *testing.T) {
	p := newRenewableProviderFixture()
	s := renewableHost(t, t.TempDir(), p, "")
	cookie, _, _ := codeLogin(t, s, "alice")
	entered, release := make(chan struct{}), make(chan struct{})
	original := s.access.sessionStore.ops.createTemp
	s.access.sessionStore.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		close(entered)
		<-release
		return original(dir, pattern)
	}
	done := make(chan error, 1)
	go func() { done <- s.access.renewal.change(func(map[[32]byte]accessSession) error { return nil }) }()
	<-entered
	read := make(chan error, 1)
	go func() { _, err := authenticateRenewal(t, s, cookie); read <- err }()
	select {
	case err := <-read:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("slow disk held authentication lock")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAccessSessionRenewalCloseCancelsAndSettlesExternalCalls(t *testing.T) {
	p := newRenewableProviderFixture()
	s := renewableHost(t, t.TempDir(), p, "")
	cookie, _, _ := codeLogin(t, s, "alice")
	entered, settled := make(chan struct{}), make(chan struct{})
	p.refresh = func(ctx context.Context, g authn.Grant) (authn.Grant, error) {
		close(entered)
		<-ctx.Done()
		close(settled)
		return g, ctx.Err()
	}
	p.advance(16 * time.Minute)
	done := make(chan error, 1)
	go func() { _, err := authenticateRenewal(t, s, cookie); done <- err }()
	<-entered
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-settled:
	default:
		t.Fatal("writer closed before IAM task settled")
	}
	<-done
	if _, err := authenticateRenewal(t, s, cookie); !errors.Is(err, errSessionStorage) {
		t.Fatal("closed owner admitted renewal", err)
	}
}

func TestAccessSessionRenewalReadsFullCapacityAndRejectsByteOverflow(t *testing.T) {
	p := newRenewableProviderFixture()
	s := renewableHost(t, t.TempDir(), p, "")
	cookie, _, _ := codeLogin(t, s, "alice")
	base := sessionFor(t, s, cookie)
	all := map[[32]byte]accessSession{}
	for i := 0; i < 64; i++ {
		id := authn.Identity{Provider: "opencsg", Authority: "https://opencsg.example", Subject: fmt.Sprintf("user-%d", i)}
		user, err := s.access.users.reserve(id, s.access.config.Deployment.Authentication)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 4; j++ {
			token, err := randomAccessToken()
			if err != nil {
				t.Fatal(err)
			}
			record := base
			record.Subject = userSubject{user.ID, user.AuthVersion}
			r := *base.renewal
			r.Grant.Subject = id.Subject
			r.Grant.AccessToken = strings.Repeat("a", 8192)
			r.Grant.RefreshToken = strings.Repeat("b", 8192)
			record.renewal = &r
			all[tokenDigest(token)] = record
		}
	}
	if err := s.access.renewal.change(func(next map[[32]byte]accessSession) error {
		clear(next)
		for k, v := range all {
			next[k] = v
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.access.sessionPath())
	if err != nil || info.Size() < 1<<20 {
		t.Fatal("capacity fixture did not exceed old reader limit", err)
	}
	if err := s.access.renewal.restore(); err != nil {
		t.Fatal("v3 capacity restore failed", err)
	}
	if len(savedRenewal(t, s).Sessions) != 256 {
		t.Fatal("capacity restore dropped valid sessions")
	}
	before, err := os.ReadFile(s.access.sessionPath())
	if err != nil {
		t.Fatal(err)
	}
	err = s.access.renewal.change(func(next map[[32]byte]accessSession) error {
		for k, v := range next {
			r := *v.renewal
			r.Grant.AccessToken = strings.Repeat("a", 64<<10)
			r.Grant.RefreshToken = strings.Repeat("b", 64<<10)
			v.renewal = &r
			next[k] = v
		}
		return nil
	})
	if !errors.Is(err, errSessionCapacity) {
		t.Fatal("snapshot byte overflow accepted", err)
	}
	after, _ := os.ReadFile(s.access.sessionPath())
	if string(before) != string(after) {
		t.Fatal("overflow rewrote existing sessions")
	}
	// A 257th login cannot evict someone else's session.
	user, err := s.access.users.reserve(authn.Identity{Provider: "opencsg", Authority: "https://opencsg.example", Subject: "extra"}, s.access.config.Deployment.Authentication)
	if err != nil {
		t.Fatal(err)
	}
	login, _ := p.CompleteGrant(context.Background(), url.Values{"code": {"extra"}})
	if _, err := s.access.renewal.install(login, userSubject{user.ID, user.AuthVersion}, [32]byte{}); !errors.Is(err, errSessionCapacity) {
		t.Fatal("deployment capacity evicted unrelated session", err)
	}
}

func TestAccessSessionRenewalFixedLeaseAndArchivedControl(t *testing.T) {
	p := newRenewableProviderFixture()
	s := renewableHost(t, t.TempDir(), p, "")
	cookie, subject, space := codeLogin(t, s, "alice")
	a := s.access
	r := httptest.NewRequest(http.MethodGet, a.public+"/v1/updates", nil)
	r.AddCookie(cookie)
	// Lease creation uses the ordinary authorized Space request context.
	user, _ := a.users.user(subject.UserID)
	aSpace, err := a.forSpace(user.DefaultSpace)
	if err != nil {
		t.Fatal(err)
	}
	r = r.WithContext(context.WithValue(r.Context(), accessContextKey{}, aSpace))
	r, err = aSpace.bindSpaceRequest(r, subject)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := aSpace.watchUIUpdates(r)
	if err != nil {
		t.Fatal(err)
	}
	defer a.unwatchUIUpdates(lease)
	deadline := lease.expires
	p.advance(16 * time.Minute)
	if _, err := authenticateRenewal(t, s, cookie); err != nil {
		t.Fatal(err)
	}
	if !lease.expires.Equal(deadline) || !lease.expires.Before(sessionFor(t, s, cookie).renewal.VerifiedUntil) {
		t.Fatal("existing SSE lease was extended")
	}
	s.mu.Lock()
	for i := range s.directory.Spaces {
		if s.directory.Spaces[i].ID == space {
			s.directory.Spaces[i].Status = "suspended"
		}
	}
	s.mu.Unlock()
	if w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, ""); w.Code != 200 {
		t.Fatal("archived default blocked renewal", w.Code, w.Body.String())
	}
	if w := hostedRequest(s, "/v1/access/logout", struct{}{}, cookie, ""); w.Code != 200 {
		t.Fatal("archived default blocked logout", w.Code)
	}
	select {
	case <-lease.done:
	default:
		t.Fatal("logout did not revoke SSE lease")
	}
}

func TestOpenCSGSessionMaxAgeValidation(t *testing.T) {
	for _, value := range []string{"bad", "0s", "-1h", "721h"} {
		if _, err := (externalAuthConfig{SessionMaxAge: value}).sessionMaxAge(); err == nil {
			t.Fatal("invalid max age accepted", value)
		}
	}
	for value, want := range map[string]time.Duration{"": 168 * time.Hour, "24h": 24 * time.Hour, "720h": 720 * time.Hour} {
		if got, err := (externalAuthConfig{SessionMaxAge: value}).sessionMaxAge(); err != nil || got != want {
			t.Fatal(value, got, err)
		}
	}
}

type renewableOAuthFixture struct{ *renewableProviderFixture }

func (p renewableOAuthFixture) CompleteWithToken(context.Context, url.Values) (authn.Identity, string, error) {
	return authn.Identity{}, "", errors.New("legacy token exchange must not be used")
}

type renewableGatewayFixture struct{ calls atomic.Int32 }

func (g *renewableGatewayFixture) GatewayAPIKey(_ context.Context, id authn.Identity, token string) (string, error) {
	g.calls.Add(1)
	if token != "access-"+id.Subject {
		return "", errors.New("wrong owner's access token")
	}
	return "gk_gateway-" + id.Subject, nil
}
func TestOpenCSGRenewalBootstrapUsesOneExchangeAndNeverRepeatsGateway(t *testing.T) {
	p := newRenewableProviderFixture()
	provider := renewableOAuthFixture{p}
	s := hostedFixtureWithProvider(t, t.TempDir(), externalAuthConfig{Provider: "opencsg", Issuer: "https://opencsg.example", ClientID: "teamx-client", AllowAllAuthenticated: true}, provider)
	s.access.now = p.now
	gateway := &renewableGatewayFixture{}
	s.configureOAuthModelDefaults(provider, gateway)
	alice, subject, _ := codeLogin(t, s, "alice")
	if p.exchanges.Load() != 1 || gateway.calls.Load() != 1 {
		t.Fatal("bootstrap repeated authorization code exchange")
	}
	if key, _, err := s.access.users.gatewayKey(subject.UserID); err != nil || key != "gk_gateway-alice" {
		t.Fatal("owner key lost", err)
	}
	p.advance(16 * time.Minute)
	if _, err := authenticateRenewal(t, s, alice); err != nil {
		t.Fatal(err)
	}
	if p.exchanges.Load() != 1 || gateway.calls.Load() != 1 {
		t.Fatal("refresh invoked model bootstrap")
	}
	bob, bobSubject, _ := codeLogin(t, s, "bob")
	if key, _, err := s.access.users.gatewayKey(bobSubject.UserID); err != nil || key != "gk_gateway-bob" {
		t.Fatal("another owner's key reused", err)
	}
	if sessionFor(t, s, alice).renewal.Grant.Subject == sessionFor(t, s, bob).renewal.Grant.Subject {
		t.Fatal("browser grants mixed")
	}
}

func TestAccessSessionRenewalEarlyFailureKeepsLeaseAndBoundsRetries(t *testing.T) {
	p := newRenewableProviderFixture()
	s := renewableHost(t, t.TempDir(), p, "")
	cookie, _, _ := codeLogin(t, s, "alice")
	p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
		return authn.Identity{}, &authn.Failure{Kind: authn.ClientConfig}
	}
	// Access is still valid, so this check must not rotate credentials.
	p.advance(14 * time.Minute)
	// Simulate a longer-lived access token to exercise direct userinfo checking.
	if err := s.access.renewal.change(func(next map[[32]byte]accessSession) error {
		key := tokenDigest(cookie.Value)
		entry := next[key]
		r := *entry.renewal
		r.Grant.AccessExpires = p.now().Add(time.Hour)
		entry.renewal = &r
		next[key] = entry
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, ""); w.Code != 200 {
		t.Fatal("early outage discarded existing permission", w.Code)
	}
	if p.refreshes.Load() != 0 || p.verifies.Load() != 1 {
		t.Fatal("valid access rotated or check not shared")
	}
	if _, err := authenticateRenewal(t, s, cookie); err != nil {
		t.Fatal(err)
	}
	if p.verifies.Load() != 1 {
		t.Fatal("backoff bypassed")
	}
	p.advance(2 * time.Minute)
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 503 {
		t.Fatal("client config error treated as invalid grant", w.Code)
	}
	if _, ok := s.access.browserSubject(&http.Request{Header: http.Header{"Cookie": {cookie.String()}}}); !ok {
		t.Fatal("temporary failure deleted session")
	}
	p.verify = nil
	p.advance(time.Minute)
	if _, err := authenticateRenewal(t, s, cookie); err != nil {
		t.Fatal("recovery failed", err)
	}
}

// Keep the short verification window but give its access token a longer TTL,
// so these cases exercise identity outages independently of token expiration.
func extendRenewalAccessToken(t *testing.T, s *spaceServer, cookie *http.Cookie, until time.Time) {
	t.Helper()
	if err := s.access.renewal.change(func(next map[[32]byte]accessSession) error {
		key := tokenDigest(cookie.Value)
		entry := next[key]
		r := *entry.renewal
		r.Grant.AccessExpires = until
		entry.renewal = &r
		next[key] = entry
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAccessSessionRenewalTemporaryGraceSurvivesRestartWithoutExtension(t *testing.T) {
	home := t.TempDir()
	p := newRenewableProviderFixture()
	s := renewableHost(t, home, p, "")
	cookie, subject, space := codeLogin(t, s, "alice")
	extendRenewalAccessToken(t, s, cookie, p.now().Add(2*time.Hour))
	original := sessionFor(t, s, cookie)
	deadline := original.renewal.VerifiedUntil.Add(authenticationGrace)
	p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
		return authn.Identity{}, &authn.Failure{Kind: authn.Temporary}
	}
	p.advance(16 * time.Minute)
	assertGrace := func() {
		t.Helper()
		w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, "")
		var metadata struct {
			UsableUntil time.Time `json:"usable_until"`
			RenewAfter  time.Time `json:"renew_after"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &metadata) != nil || !metadata.UsableUntil.Equal(deadline) || !metadata.RenewAfter.After(p.now()) {
			t.Fatal("grace blocked, extended, or lost its retry schedule", w.Code, w.Body.String())
		}
		if current := sessionFor(t, s, cookie); !current.renewal.VerifiedUntil.Equal(original.renewal.VerifiedUntil) || !current.expires.Equal(original.expires) {
			t.Fatal("outage extended persisted authorization")
		}
		if w := hostedRequest(s, "/v1/spaces", nil, cookie, ""); w.Code != 200 {
			t.Fatal("grace lost ordinary directory access", w.Code, w.Body.String())
		}
	}
	assertGrace()
	waitSpaceState(t, s, space, "setup_required")
	if w := hostedRequest(s, "/s/"+space+"/v1/setup", nil, cookie, ""); w.Code != 200 {
		t.Fatal("grace lost ordinary business access", w.Code, w.Body.String())
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	p.advance(time.Minute)
	s = renewableHost(t, home, p, "")
	assertGrace()
	if p.refreshes.Load() != 0 {
		t.Fatal("identity retry rotated an unexpired access token")
	}

	// A new stream uses the same fixed grace deadline as ordinary HTTP.
	a := s.access
	user, _ := a.users.user(subject.UserID)
	aSpace, err := a.forSpace(user.DefaultSpace)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, a.public+"/v1/updates", nil)
	r.AddCookie(cookie)
	r = r.WithContext(context.WithValue(r.Context(), accessContextKey{}, aSpace))
	r, err = aSpace.bindSpaceRequest(r, subject)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := aSpace.watchUIUpdates(r)
	if err != nil || !lease.expires.Equal(deadline) {
		t.Fatal("SSE did not use the bounded grace deadline", err)
	}
	defer a.unwatchUIUpdates(lease)
	p.advance(3 * time.Minute) // Exactly the original verified_until + 5m.
	if w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, ""); w.Code != 503 {
		t.Fatal("retries or restart extended grace", w.Code)
	}
	select {
	case <-lease.done:
	default:
		t.Fatal("expired grace retained its SSE lease")
	}
	if _, err := aSpace.watchUIUpdates(r); err == nil {
		t.Fatal("SSE admitted after grace expired")
	}
	for _, path := range []string{"/", "/s/" + space + "/"} {
		w := hostedRequest(s, path, nil, cookie, "")
		if w.Code != 503 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), "authentication-recovery.mjs") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("navigation lost automatic recovery", path, w.Code, w.Body.String())
		}
	}
	for _, asset := range []string{"/ui/authentication-recovery.mjs", "/ui/session.mjs", "/ui/access.css"} {
		if w := hostedRequest(s, asset, nil, cookie, ""); w.Code != 200 {
			t.Fatal("recovery dependency requires unavailable authentication", asset, w.Code)
		}
	}
	if w := hostedRequest(s, "/s/"+space+"/v1/setup", nil, cookie, ""); w.Code != 503 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
		t.Fatal("business API bypassed authentication or returned HTML", w.Code)
	}
	p.verify = nil
	p.advance(time.Minute)
	if w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, ""); w.Code != 200 || !sessionFor(t, s, cookie).renewal.VerifiedAt.Equal(p.now()) {
		t.Fatal("session did not recover without login", w.Code, w.Body.String())
	}
}

func TestAccessSessionRenewalGraceHonorsCredentialAndRevocationBoundaries(t *testing.T) {
	for _, mode := range []string{"client_config", "unknown_failure", "access_expired", "absolute_expired", "invalid_grant", "disabled", "logout", "storage"} {
		t.Run(mode, func(t *testing.T) {
			p := newRenewableProviderFixture()
			s := renewableHost(t, t.TempDir(), p, "")
			cookie, subject, _ := codeLogin(t, s, "alice")
			if mode != "access_expired" {
				extendRenewalAccessToken(t, s, cookie, p.now().Add(time.Hour))
			}
			p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
				return authn.Identity{}, &authn.Failure{Kind: authn.Temporary}
			}
			p.refresh = func(context.Context, authn.Grant) (authn.Grant, error) {
				return authn.Grant{}, &authn.Failure{Kind: authn.Temporary}
			}
			p.advance(16 * time.Minute)
			if mode != "access_expired" {
				if w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, ""); w.Code != 200 {
					t.Fatal("grace setup failed", w.Code)
				}
			}
			want := 503
			switch mode {
			case "client_config":
				p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
					return authn.Identity{}, &authn.Failure{Kind: authn.ClientConfig}
				}
			case "unknown_failure":
				p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
					return authn.Identity{}, errors.New("unclassified failure")
				}
			case "invalid_grant":
				p.verify = func(context.Context, authn.Grant) (authn.Identity, error) {
					return authn.Identity{}, &authn.Failure{Kind: authn.InvalidGrant}
				}
				want = 401
			case "absolute_expired":
				if err := s.access.renewal.change(func(next map[[32]byte]accessSession) error {
					key := tokenDigest(cookie.Value)
					x := next[key]
					x.expires = p.now().Add(time.Second)
					next[key] = x
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				want = 401
			case "disabled":
				if _, err := s.access.users.setStatus(subject.UserID, "disabled"); err != nil {
					t.Fatal(err)
				}
				s.fenceUser(subject.UserID)
				want = 401
			case "logout":
				if w := hostedRequest(s, "/v1/access/logout", struct{}{}, cookie, ""); w.Code != 200 {
					t.Fatal(w.Code)
				}
				want = 401
			case "storage":
				p.verify = nil
				s.access.sessionStore.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("storage unavailable") }
			}
			p.advance(time.Minute)
			if w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, ""); w.Code != want {
				t.Fatal("grace bypassed an authentication boundary", w.Code, w.Body.String())
			}
		})
	}
}

func TestExternalLoginRenewableFailureClearsCallbackAndExplainsCapacity(t *testing.T) {
	for _, failure := range []struct {
		err    error
		marker string
	}{{errSessionCapacity, "session_capacity_reached"}, {errSessionStorage, "session_storage_unavailable"}} {
		t.Run(failure.marker, func(t *testing.T) {
			p := newRenewableProviderFixture()
			s := renewableHost(t, t.TempDir(), p, "")
			s.access.login.admitRenewable = func(context.Context, http.ResponseWriter, authn.Login, *http.Request) error { return failure.err }
			start := hostedRequest(s, externalLoginPath, struct{}{}, nil, "")
			jump, _ := url.Parse(start.Header().Get("Location"))
			callback, _ := url.Parse(jump.Query().Get("redirect_uri"))
			callback.RawQuery = url.Values{"state": {jump.Query().Get("state")}, "code": {"alice"}}.Encode()
			result := hostedRequest(s, callback.RequestURI(), nil, start.Result().Cookies()[0], "")
			if result.Header().Get("Location") != "/access?sso="+failure.marker || strings.Contains(result.Header().Get("Location"), "code=") {
				t.Fatal("capacity failure hidden or sensitive callback retained")
			}
			for _, cookie := range result.Result().Cookies() {
				if cookie.Name == s.access.cookieName() && cookie.Value != "" {
					t.Fatal("failed login issued business cookie")
				}
			}
		})
	}
}

func TestAccessSessionRenewalHourlyChecksPreserveAbsoluteDeadlineOnRestart(t *testing.T) {
	home := t.TempDir()
	p := newRenewableProviderFixture()
	s := renewableHost(t, home, p, "")
	oldCookie, subject, _ := codeLogin(t, s, "alice")
	oldSession := sessionFor(t, s, oldCookie)
	login, err := p.CompleteGrant(context.Background(), url.Values{"code": {"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	login.Grant.AccessExpires = p.now().Add(4 * time.Hour)
	login.VerifiedUntil = login.VerifiedAt.Add(time.Hour)
	excessive := login
	excessive.VerifiedUntil = login.VerifiedUntil.Add(time.Second)
	if _, err := s.access.renewal.install(excessive, subject, [32]byte{}); !errors.Is(err, errLoginRequired) {
		t.Fatal("check window beyond one hour accepted", err)
	}
	token, err := s.access.renewal.install(login, subject, [32]byte{})
	if err != nil {
		t.Fatal("one-hour login rejected", err)
	}
	cookie := &http.Cookie{Name: s.access.cookieName(), Value: token}
	original := sessionFor(t, s, cookie)
	if want := login.VerifiedAt.Add(50 * time.Minute); !renewAfter(original.renewal).Equal(want) {
		t.Fatal("hourly check not scheduled ten minutes early")
	}
	p.advance(30 * time.Minute)
	if _, err := authenticateRenewal(t, s, cookie); err != nil {
		t.Fatal(err)
	}
	if p.verifies.Load() != 0 || p.refreshes.Load() != 0 {
		t.Fatal("long-lived token checked before hourly window")
	}
	p.advance(29 * time.Minute)
	if w := hostedRequest(s, "/v1/access/session/renew", struct{}{}, cookie, ""); w.Code != 200 {
		t.Fatal("hourly verification failed", w.Code, w.Body.String())
	}
	renewed := sessionFor(t, s, cookie)
	if p.verifies.Load() != 1 || p.refreshes.Load() != 0 {
		t.Fatal("hourly check did not reuse valid access token")
	}
	if renewed.renewal.VerifiedUntil.Sub(renewed.renewal.VerifiedAt) != time.Hour || !renewed.expires.Equal(original.expires) {
		t.Fatal("wrong check window or absolute deadline extended")
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	s = renewableHost(t, home, p, "")
	restored := sessionFor(t, s, cookie)
	if !sessionFor(t, s, oldCookie).renewal.VerifiedUntil.Equal(oldSession.renewal.VerifiedUntil) {
		t.Fatal("restart extended an existing shorter check window")
	}
	if !restored.renewal.VerifiedUntil.Equal(renewed.renewal.VerifiedUntil) || !restored.expires.Equal(original.expires) {
		t.Fatal("restart changed persisted deadlines")
	}
}
