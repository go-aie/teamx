//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func TestOAuthConcurrentRefreshSharedPlatforms(t *testing.T) {
	parallelFixtureTest(t)
	m, account := oauthTestManager(t, time.Now().Add(time.Second))
	m.mu.Lock()
	file, _ := m.platforms.Load()
	second := file.Platforms[0]
	second.ID = strings.Repeat("d", 32)
	second.ToolEntryID = "gitlab-" + strings.Repeat("d", 32)
	file.Platforms = append(file.Platforms, second)
	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	file, _ = m.platforms.Load()
	other, err := registry.ResolveAuthorization(file, file.Platforms[1])
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	m.platformHTTP = oauthTransport(&posts, func(r *http.Request) error {
		close(started)
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-release:
			return nil
		}
	})
	type result struct {
		lease *gitlabTokenLease
		err   error
	}
	results := make(chan result, 20)
	for i := 0; i < 20; i++ {
		a := account
		if i%2 == 1 {
			a = other
		}
		go func() {
			l, e := m.acquireGitLabToken(context.Background(), RoomIdentity{}, a, time.Second)
			results <- result{l, e}
		}()
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	// The network boundary is outside manager.mu, even during the refresh POST.
	m.mu.Lock()
	m.mu.Unlock()
	close(release)
	for range 20 {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.lease.credential.Kind != "oauth" || r.lease.credential.Token.Reveal() != "rotated-access" {
				t.Fatal("wrong leased token")
			}
			if err := m.finishGitLabToken(r.lease); err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("refresh waiter stuck")
		}
	}
	if posts.Load() != 1 {
		t.Fatal("refreshes", posts.Load())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	file, err = m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	after, err := registry.ResolveAuthorization(file, file.Platforms[0])
	if err != nil || after != account || file.OAuthCredentials[0].TokenRevision != 2 {
		t.Fatal("normal renewal changed authorization", err)
	}
	if len(m.oauthLeases) != 0 || len(m.oauthFlights) != 0 || len(m.platformCalls) != 0 {
		t.Fatal("token ownership leaked")
	}
}

func TestOAuthRefreshWaitsForSettledTokenUsers(t *testing.T) {
	parallelFixtureTest(t)
	m, account := oauthTestManager(t, time.Now().Add(time.Hour))
	old, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	file, _ := m.platforms.Load()
	file.OAuthCredentials[0].ExpiresAt = time.Now().Add(35 * time.Second)
	if err = m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	var posts atomic.Int32
	m.platformHTTP = oauthTransport(&posts, nil)
	done := make(chan error, 1)
	go func() {
		next, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, 10*time.Second)
		if err == nil {
			err = m.finishGitLabToken(next)
		}
		done <- err
	}()
	<-old.ctx.Done()
	if posts.Load() != 0 {
		t.Fatal("refreshed before old operation settled")
	}
	if err = m.finishGitLabToken(old); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("settlement did not release refresh")
	}
	if posts.Load() != 1 {
		t.Fatal("refresh count", posts.Load())
	}
}

func TestOAuthOldGrantResultsRejectedAndUseBounded(t *testing.T) {
	parallelFixtureTest(t)
	m, account := oauthTestManager(t, time.Now().Add(time.Hour))
	lease, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	file, _ := m.platforms.Load()
	file.OAuthCredentials[0].State = "reauthorization_required"
	if err = m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	if err = m.finishGitLabToken(lease); !errors.Is(err, platform.ErrCredential) {
		t.Fatal("accepted revoked authorization result", err)
	}
	if _, err = m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second); err == nil {
		t.Fatal("revoked grant admitted")
	}
	if _, err = m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, maxGitLabTokenUse+1); !errors.Is(err, ErrRequest) {
		t.Fatal("unbounded token use admitted", err)
	}
}

func TestOAuthRefreshUnknownIsNotReplayedAfterRestart(t *testing.T) {
	parallelFixtureTest(t)
	m, account := oauthTestManager(t, time.Now().Add(time.Second))
	var posts atomic.Int32
	m.platformHTTP = oauthTransport(&posts, func(*http.Request) error { return errors.New("lost refresh response") })
	if _, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second); err == nil {
		t.Fatal("unknown exchange accepted")
	}
	if _, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second); err == nil || posts.Load() != 1 {
		t.Fatal("unknown refresh replayed", err)
	}
	m.mu.Lock()
	file, err := m.platforms.Load()
	m.mu.Unlock()
	if err != nil || file.OAuthCredentials[0].State != "refresh_unknown" {
		t.Fatal("missing uncertainty evidence", err)
	}
	if err = m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	reopened.platformHTTP = oauthTransport(&posts, nil)
	if _, err = reopened.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second); err == nil || posts.Load() != 1 {
		t.Fatal("restart replayed refresh", err)
	}
}

func TestOAuthRefreshStorageFailures(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"intent_failed", "result_failed", "intent_unknown", "result_unknown"} {
		t.Run(mode, func(t *testing.T) {
			m, account := oauthTestManager(t, time.Now().Add(time.Second))
			var posts atomic.Int32
			original := m.store.ops
			fail := func() {
				m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("injected disk failure") }
			}
			unknown := func() {
				m.store.ops.syncDir = func(string) error { return errors.New("injected directory sync failure") }
			}
			m.platformHTTP = oauthTransport(&posts, func(*http.Request) error {
				m.mu.Lock()
				defer m.mu.Unlock()
				m.store.mu.Lock()
				defer m.store.mu.Unlock()
				if mode == "result_failed" {
					fail()
				}
				if mode == "result_unknown" {
					unknown()
				}
				return nil
			})
			if mode == "intent_failed" {
				fail()
			}
			if mode == "intent_unknown" {
				unknown()
			}
			if _, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second); err == nil {
				t.Fatal("save failure accepted")
			}
			if strings.HasPrefix(mode, "intent") && posts.Load() != 0 {
				t.Fatal("POST before durable intent")
			}
			if strings.HasPrefix(mode, "result") && posts.Load() != 1 {
				t.Fatal("missing refresh exchange")
			}
			// Restore I/O, preserving the actual writer's commit-unknown latch.
			m.mu.Lock()
			m.store.mu.Lock()
			m.store.ops = original
			m.store.mu.Unlock()
			file, err := m.platforms.Load()
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "result_failed" && file.OAuthCredentials[0].State != "refresh_pending" {
				t.Fatal("lost crash barrier")
			}
			if mode == "result_unknown" && (file.OAuthCredentials[0].TokenRevision != 2 || file.OAuthCredentials[0].AccessToken != "rotated-access") {
				t.Fatal("lost committed rotated pair")
			}
			if mode != "intent_failed" {
				prior := posts.Load()
				if _, err = m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second); err == nil || posts.Load() != prior {
					t.Fatal("save failure replayed refresh", err)
				}
				if err = m.close(); err != nil {
					t.Fatal(err)
				}
				reopened := testManager(t, m.store.dir, nil)
				reopened.platformHTTP = oauthTransport(&posts, nil)
				lease, recoveryErr := reopened.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second)
				if mode == "result_unknown" {
					if recoveryErr != nil || lease.credential.Token.Reveal() != "rotated-access" {
						t.Fatal("committed token pair was not recovered", recoveryErr)
					}
					if err = reopened.finishGitLabToken(lease); err != nil {
						t.Fatal(err)
					}
				} else if recoveryErr == nil {
					t.Fatal("restart admitted uncertain refresh")
				}
				if posts.Load() != prior {
					t.Fatal("recovery replayed refresh")
				}
			}
		})
	}
}

func TestOAuthShutdownCancelsAndSettlesRefresh(t *testing.T) {
	parallelFixtureTest(t)
	m, account := oauthTestManager(t, time.Now().Add(time.Second))
	var posts atomic.Int32
	started := make(chan struct{})
	m.platformHTTP = oauthTransport(&posts, func(r *http.Request) error { close(started); <-r.Context().Done(); return r.Context().Err() })
	done := make(chan error, 1)
	go func() {
		_, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh never started")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("shutdown accepted refresh")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not settle refresh")
	}
}

func TestOAuthRenewalPreservesNotificationCoreProgress(t *testing.T) {
	parallelFixtureTest(t)
	h := newTodoHarness(t)
	account := installOAuthFixture(t, h.m, h.connection, time.Now().Add(time.Second))
	var posts atomic.Int32
	h.m.platformHTTP = oauthTransport(&posts, nil)
	before := h.state()
	before.Binding.CredentialFingerprint = account.CredentialFingerprint
	h.m.mu.Lock()
	if err := h.m.notifications.SaveLocked(before); err != nil {
		t.Fatal(err)
	}
	// G1's contract fixture joins the stable authorization boundary to the real
	// notification core. Concrete Events/Todos transport wiring belongs to G4.
	adapter := h.m.notificationAdapters[gitlab.TodosSourceID]
	adapter.Admit = func(_ context.Context, state notification.State) (notification.Scope, error) {
		file, err := h.m.platforms.Load()
		if err != nil {
			return notification.Scope{}, err
		}
		current, err := registry.ResolveAuthorization(file, file.Platforms[0])
		if err != nil {
			return notification.Scope{}, err
		}
		if current != account {
			return notification.Scope{}, notification.ErrConflict
		}
		s := state.(gitlab.TodosState)
		data, _ := json.Marshal(gitlab.TodosQueryScope{Binding: s.Binding, PlatformVersion: current.PlatformVersion})
		return notification.Scope{Data: data, Revision: fmt.Sprint(current.PlatformVersion), CredentialStamp: current.CredentialFingerprint}, nil
	}
	h.m.notificationAdapters[gitlab.TodosSourceID] = adapter
	h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(ctx context.Context, q gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			if posts.Load() == 0 {
				lease, err := h.m.acquireGitLabToken(ctx, h.room.RoomIdentity, account, time.Second)
				if err != nil {
					return gitlab.TodoBatch{}, err
				}
				if err = h.m.finishGitLabToken(lease); err != nil {
					return gitlab.TodoBatch{}, err
				}
			}
			return h.pages.FetchTodos(ctx, q)
		}), nil
	}
	h.m.mu.Unlock()
	h.pages.pending = []gitlab.Todo{h.todo(99, 0, "pending")}
	h.newRunner()
	h.full()
	after := h.state()
	if posts.Load() != 1 || len(after.Inbox) != 1 || len(after.Observed) != 1 || after.SubscriptionID != before.SubscriptionID || after.SubscriptionVersion != before.SubscriptionVersion || !after.Baseline.Equal(before.Baseline) || after.QueryPaused() {
		t.Fatal("normal refresh lost progress or paused notification")
	}
}

func TestOAuthCrashBarrierSurvivesServiceRestart(t *testing.T) {
	parallelFixtureTest(t)
	m, account := oauthTestManager(t, time.Now().Add(time.Second))
	m.mu.Lock()
	_, err := m.platforms.BeginOAuthRefresh(account.Authorization)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// The intent is durable, but there is no live flight, as after process death.
	if err = m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	var posts atomic.Int32
	reopened.platformHTTP = oauthTransport(&posts, nil)
	if _, err = reopened.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second); err == nil || posts.Load() != 0 {
		t.Fatal("crash barrier replayed destructive refresh", err)
	}
	list := platformTestList(t, reopened)
	if len(list) != 1 || list[0].AuthorizationStatus != "refresh_unknown" {
		t.Fatal("crash state hidden")
	}
}

func TestOAuthCanceledWaiterDoesNotReplayOrCancelRefresh(t *testing.T) {
	parallelFixtureTest(t)
	m, account := oauthTestManager(t, time.Now().Add(time.Second))
	var posts atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	m.platformHTTP = oauthTransport(&posts, func(r *http.Request) error {
		close(started)
		select {
		case <-r.Context().Done():
			return r.Context().Err()
		case <-release:
			return nil
		}
	})
	done := make(chan error, 1)
	go func() {
		lease, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, account, time.Second)
		if err == nil {
			err = m.finishGitLabToken(lease)
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() { _, err := m.acquireGitLabToken(ctx, RoomIdentity{}, account, time.Second); waiter <- err }()
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not cancel")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("leader did not finish")
	}
	if posts.Load() != 1 {
		t.Fatal("cancellation replayed refresh")
	}
}
