package room

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/app/platforms"
)

func spaceRuntimeFixture(t *testing.T) *SpaceRuntime {
	t.Helper()
	return spaceRuntimeFixtureAt(t, "http://127.0.0.1:8088")
}

func spaceRuntimeFixtureAt(t *testing.T, endpoint string) *SpaceRuntime {
	t.Helper()
	c := freshAccessConfig(t, t.TempDir())
	if err := prepareFirstAccess(c); err != nil {
		t.Fatal(err)
	}
	c = freshAccessConfig(t, filepath.Dir(c.path))
	access, err := newServerAccess(c, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newSpaceRuntime(c, serviceConfig{platformCatalog: defaultPlatformCatalog(), access: access, settings: newSystemSettings(c), endpoint: endpoint, maxTurns: 7, environment: startupEnvironment(c.Env)}, personalDeployment(nil).factories)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.closeWithTimeout(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func TestDeploymentSelectionValidatesBeforeInitialization(t *testing.T) {
	selection := personalDeployment(func(string, string, serviceConfig) (*service, error) {
		t.Fatal("pure selection invoked service factory")
		return nil, nil
	})
	if err := selection.validate(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name   string
		remove func(*spaceFactories)
	}{
		{"host tools", func(f *spaceFactories) { f.hostTools = nil }},
		{"service", func(f *spaceFactories) { f.service = nil }},
		{"manager", func(f *spaceFactories) { f.manager = nil }},
		{"models", func(f *spaceFactories) { f.models = nil }},
		{"platform clients", func(f *spaceFactories) { f.platformClients = nil }},
		{"model controller", func(f *spaceFactories) { f.modelController = nil }},
		{"computer", func(f *spaceFactories) { f.computer = nil }},
		{"setup", func(f *spaceFactories) { f.setup = nil }},
		{"migration", func(f *spaceFactories) { f.migration = nil }},
	} {
		t.Run(item.name, func(t *testing.T) {
			selected := selection
			item.remove(&selected.factories)
			home := filepath.Join(t.TempDir(), "uninitialized")
			listened := false
			err := runConfiguredServer(t.Context(), []string{"--home", home}, io.Discard, io.Discard, serverOptions{deployment: &selected, listen: func(string, string) (net.Listener, error) {
				listened = true
				return nil, errors.New("unexpected listener")
			}})
			if err == nil || listened {
				t.Fatal("missing binding reached initialization", err)
			}
			if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed selection touched private home", err)
			}
		})
	}
}

func TestSpaceRuntimeBindsRegisteredConfiguration(t *testing.T) {
	r := spaceRuntimeFixture(t)
	if r.OwnerUserID != r.config.access.subject().UserID || r.roots != r.config.access.identity.Space.Roots || r.config.modelStartup != &r.startup || r.config.ownerUserID != r.OwnerUserID {
		t.Fatal("runtime did not use registered owner/roots")
	}
	if r.startup.Access.OperatorID != "" || r.startup.Server.Listen != nil || r.startup.installationID != "" {
		t.Fatal("space received deployment settings")
	}
	original := r.config.access.config
	changed := original
	other := filepath.Join(t.TempDir(), "other-root")
	changed.Server.DataDir = &other
	if _, err := newSpaceRuntime(changed, r.config, r.factories); !errors.Is(err, ErrIdentity) {
		t.Fatal("runtime accepted a root outside registration", err)
	}
	if _, err := os.Lstat(other); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("root rejection created data", err)
	}
}

func TestSpaceRuntimeInitializationFailureReleasesOwnedResources(t *testing.T) {
	r := spaceRuntimeFixture(t)
	failure := errors.New("computer initialization failure")
	r.factories.computer = func(context.Context, startupConfig, *systemSettings, ServiceInfo, string, string, func(MemberIdentity) (bool, error)) (*computerController, error) {
		return nil, failure
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := r.Serve(t.Context(), listener, func(any) error { t.Fatal("failed initialization became ready"); return nil }, io.Discard); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if r.State() != "stopped" {
		t.Fatal(r.State())
	}
	m, err := newRoomManager(r.roots.Data, 7)
	if err != nil {
		t.Fatal("initialization retained settled data root", err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Serve(t.Context(), listener, func(any) error { return nil }, io.Discard); !errors.Is(err, errSpaceStopped) {
		t.Fatal("restarted a stopped runtime", err)
	}
}

func TestSpaceRuntimeShutdownTimeoutRetainsManagerComputerAndLocks(t *testing.T) {
	r := spaceRuntimeFixture(t)
	m, err := r.factories.manager(r.roots.Data, r.roots.Teams, 7, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.manager = m
	m.config, m.settings = r.config, r.config.settings
	control, err := r.factories.computer(t.Context(), r.startup, m.settings, ServiceInfo{ServiceEpoch: m.epoch}, r.config.access.installationID, r.OwnerUserID, func(MemberIdentity) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	r.computer, m.settings.computer, m.ownComputer = control, control, control
	control.start()
	r.homeLock, err = lockBundleHome(r.roots.Home)
	if err != nil {
		t.Fatal(err)
	}
	pending := make(chan struct{})
	m.busy, m.operationDone = true, pending
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := r.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unsettled operation lost its timeout", err)
	}
	if r.State() != "closing" || r.manager != m || r.computer != control || control.ctx.Err() != nil {
		t.Fatal("timeout discarded original resources")
	}
	if other, err := newRoomManager(r.roots.Data, 7); !errors.Is(err, ErrLocked) {
		if other != nil {
			other.close()
		}
		t.Fatal("timeout released data lock", err)
	}
	if lock, err := lockBundleHome(r.roots.Home); err == nil {
		unlockDirectory(lock)
		t.Fatal("timeout released home lock")
	}
	close(pending)
	if err := r.Close(t.Context()); err != nil {
		t.Fatal("retry failed", err)
	}
	if r.State() != "stopped" || !control.closed {
		t.Fatal("settlement did not close runtime")
	}
	if lock, err := lockBundleHome(r.roots.Home); err != nil {
		t.Fatal("settled home remained locked", err)
	} else {
		unlockDirectory(lock)
	}
	m2, err := newRoomManager(r.roots.Data, 7)
	if err != nil {
		t.Fatal("settled root could not reopen", err)
	}
	if m2 == m || m2.notifications == m.notifications {
		t.Fatal("reopen reused a stopped manager")
	}
	m2.close()
}

func TestSpaceRuntimeCloseDuringInitializationRetainsOwner(t *testing.T) {
	r := spaceRuntimeFixture(t)
	var err error
	r.homeLock, err = lockBundleHome(r.roots.Home)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	original := r.factories.manager
	r.factories.manager = func(dir, teams string, turns uint64, defaults *teamRuntime, upgrade *inputUpgradeOptions, catalog *platforms.Catalog) (*roomManager, error) {
		close(entered)
		<-release
		return original(dir, teams, turns, defaults, upgrade, catalog)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		done <- r.Serve(t.Context(), listener, func(any) error { t.Error("canceled initialization became ready"); return nil }, io.Discard)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := r.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("close did not wait for constructor", err)
	}
	if lock, err := lockBundleHome(r.roots.Home); err == nil {
		unlockDirectory(lock)
		t.Error("in-progress constructor lost home lock")
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("initialization cancellation lost", err)
	}
	if err := r.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.State() != "stopped" {
		t.Fatal(r.State())
	}
}
