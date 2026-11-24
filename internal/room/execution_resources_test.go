package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

type resourceFailureFactory struct {
	agent.Factory
	partial bool
}

func (f resourceFailureFactory) OpenSession(ctx context.Context, _ agent.Host, resume bool) (*agent.Session, error) {
	return agent.OpenSession(ctx, f, resourceFailureHost{}, resume)
}
func (f resourceFailureFactory) Open(context.Context, agent.Launch, bool) (agent.Component, error) {
	if f.partial {
		return &adapterComponent{}, errors.New("failed after allocation")
	}
	return nil, errors.New("failed before allocation")
}

type resourceFailureHost struct{}

func (resourceFailureHost) PrepareOpen(context.Context) (agent.Launch, error) {
	return agent.Launch{}, nil
}
func (resourceFailureHost) Opened(*agent.Artifact) error { return nil }
func (resourceFailureHost) PrepareTurn(context.Context, agent.Input, agent.TurnCapabilities) (*agent.PreparedTurn, error) {
	panic("unexpected turn")
}
func (resourceFailureHost) Release() {}

func TestExecutionResourcesFailedOpenOwnership(t *testing.T) {
	for _, partial := range []bool{false, true} {
		r := newExecutionResources(executionResourceLimits{Sessions: 1})
		b := dshBackend{executionResources: r, ownerUserID: "user", access: &serverAccess{space: SpaceContext{SpaceID: "space"}}, factory: resourceFailureFactory{partial: partial}}
		s, err := b.OpenSession(t.Context(), SessionOptions{})
		if err == nil {
			t.Fatal("missing open error")
		}
		if partial {
			if s == nil {
				t.Fatal("lost partially allocated session")
			}
			if _, err := r.acquireSession("user", "other"); !errors.Is(err, ErrBusy) {
				t.Fatal("partial open released its owner", err)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		} else if s != nil {
			t.Fatal("unexpected session")
		}
		release, err := r.acquireSession("user", "other")
		if err != nil {
			t.Fatal("settled failed open leaked capacity", err)
		}
		release()
	}
}

func TestExecutionResourcesAggregateAndConcurrentAdmission(t *testing.T) {
	for _, tc := range []struct {
		name        string
		limits      executionResourceLimits
		user, space string
	}{
		{"server", executionResourceLimits{Sessions: 1}, "other-user", "other-space"},
		{"user", executionResourceLimits{SessionsPerUser: 1}, "user", "other-space"},
		{"space", executionResourceLimits{SessionsPerSpace: 1}, "user", "space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newExecutionResources(tc.limits)
			release, err := r.acquireSession("user", "space")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.acquireSession(tc.user, tc.space); !errors.Is(err, ErrBusy) {
				t.Fatal(err)
			}
			release()
			release()
			next, err := r.acquireSession(tc.user, tc.space)
			if err != nil {
				t.Fatal(err)
			}
			next()
		})
	}
	r := newExecutionResources(executionResourceLimits{Sessions: 7})
	var admitted atomic.Int32
	var wg sync.WaitGroup
	var releases sync.Map
	for i := range 100 {
		wg.Go(func() {
			release, err := r.acquireSession(fmt.Sprint(i), fmt.Sprint(i))
			if err == nil {
				admitted.Add(1)
				releases.Store(i, release)
			} else if !errors.Is(err, ErrBusy) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if admitted.Load() != 7 {
		t.Fatal(admitted.Load())
	}
	releases.Range(func(_, value any) bool { value.(func())(); return true })
	if r.sessions != 0 || len(r.users) != 0 || len(r.spaces) != 0 {
		t.Fatal("leases leaked")
	}
}

func TestExecutionResourcesRetainedUntilSessionSettles(t *testing.T) {
	for _, closeErr := range []error{dsh.ErrCleanupPending, context.DeadlineExceeded, errors.New("unproven close")} {
		r := newExecutionResources(executionResourceLimits{Sessions: 1})
		release, err := r.acquireSession("user", "space")
		if err != nil {
			t.Fatal(err)
		}
		s := &dshSession{releaseResources: release, component: &adapterComponent{close: func(context.Context) error { return closeErr }}}
		if s.Close(t.Context()) == nil {
			t.Fatal("lost close failure")
		}
		if _, err := r.acquireSession("user", "other"); !errors.Is(err, ErrBusy) {
			t.Fatal("unsettled lease released", err)
		}
		s.component = &adapterComponent{close: func(context.Context) error {
			return &dsh.ClosedError{Err: errors.New("original Session cannot resume")}
		}}
		if err := s.Close(t.Context()); !backendCleanupSettled(err) {
			t.Fatal(err)
		}
		next, err := r.acquireSession("user", "other")
		if err != nil {
			t.Fatal(err)
		}
		next()
	}
}

func TestExecutionResourcesRejectBeforeBackendLaunch(t *testing.T) {
	r := newExecutionResources(executionResourceLimits{Sessions: 1})
	release, err := r.acquireSession("user", "space")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	b := dshBackend{executionResources: r, ownerUserID: "user", access: &serverAccess{space: SpaceContext{SpaceID: "space"}}, open: func(context.Context, dsh.Options) (dshComponent, error) {
		t.Fatal("capacity rejection reached component acquisition")
		return nil, nil
	}}
	if session, err := b.OpenSession(t.Context(), SessionOptions{}); session != nil || !errors.Is(err, ErrBusy) {
		t.Fatal(session, err)
	}
}

func TestExecutionResourcesSpaceReservations(t *testing.T) {
	r := newExecutionResources(executionResourceLimits{ActiveSpaces: 1, Spaces: 2})
	records := []SpaceRecord{{OwnerUserID: "user", Status: "archiving"}, {OwnerUserID: "user", Status: "active"}}
	if !errors.Is(r.checkSpaces(records), ErrBusy) {
		t.Fatal("archiving released capacity early")
	}
	records[0].Status = "suspended"
	if err := r.checkSpaces(records); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.checkSpaces(append(records, SpaceRecord{Status: "suspended"})), ErrBusy) {
		t.Fatal("archived space escaped storage count")
	}
}

func TestExecutionResourceConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[deployment.resources]\nsessions=5\nsessions_per_user=3\nsessions_per_space=2\nspaces=4\nactive_spaces=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := readStartupConfig(path, true, false)
	if err != nil || c.Deployment.Resources.Sessions != 5 || c.Deployment.Resources.ActiveSpaces != 2 {
		t.Fatal(c.Deployment, err)
	}
	if err := (executionResourceLimits{Sessions: -1}).validate(); err == nil {
		t.Fatal("negative quota accepted")
	}
}

func TestSpaceResourceAdmissionUsesSharedDeploymentLimits(t *testing.T) {
	s, _, _ := startSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) {
		r.config.executionResources = newExecutionResources(executionResourceLimits{ActiveSpaces: 1})
	})
	if _, err := s.create(spaceChangeInput{RequestID: "11111111111111111111111111111111", Name: "other"}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.directory.Spaces) != 1 || len(s.entries) != 1 {
		t.Fatal("rejected reservation mutated directory or started runtime")
	}
}

func TestSpaceResourceArchiveRestoreAdmission(t *testing.T) {
	resources := newExecutionResources(executionResourceLimits{ActiveSpaces: 1, Sessions: 1})
	s, _, _ := startArchivableSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) {
		r.config.executionResources = resources
	})
	original := archiveSpaceForTest(t, s, s.initial.SpaceID)
	other, err := s.create(spaceChangeInput{RequestID: "22222222222222222222222222222222", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	live := waitSpaceState(t, s, other.ID, "setup_required")
	if live.config.executionResources != resources {
		t.Fatal("Space assembly replaced shared quota owner")
	}
	if _, err := s.changeSpaceStatus(original.ID, "restore", original.Version); !errors.Is(err, ErrBusy) {
		t.Fatal("restore escaped aggregate limit", err)
	}
	if got := spaceRecordForTest(t, s, original.ID); got.Status != "suspended" || got.Version != original.Version {
		t.Fatal("failed restore changed durable identity", got)
	}
	archiveSpaceForTest(t, s, other.ID)
	if _, err := s.changeSpaceStatus(original.ID, "restore", original.Version); err != nil {
		t.Fatal(err)
	}
	restored := waitSpaceState(t, s, original.ID, "ready")
	if restored.config.executionResources != resources {
		t.Fatal("restore reset shared quota counters")
	}
}
