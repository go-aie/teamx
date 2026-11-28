package room

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/agent/srt"
)

// SpaceRuntime owns one registered business root. It delegates settlement to
// the existing manager, rather than duplicating Room/notification state. A
// stopped runtime is disposable; reopening requires a new object and epoch.
type SpaceRuntime struct {
	SpaceContext
	roots         spaceRoots
	startup       startupConfig // space settings; deployment access/listener settings remain at the server boundary
	config        serviceConfig
	defaults      *teamRuntime
	factories     spaceFactories
	manager       *roomManager
	single        *service
	computer      *computerController
	singleRoom    bool
	roomID        string
	setupRequired bool
	migrationErr  error
	state         atomic.Value
	closeMu       sync.Mutex
	homeLock      *os.File
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	assemblyDone  chan struct{}
	assemblyOnce  sync.Once
}

func newSpaceRuntime(startup startupConfig, config serviceConfig, factories spaceFactories) (*SpaceRuntime, error) {
	if err := (deploymentComposition{factories: factories}).validate(); err != nil {
		return nil, err
	}
	if config.settings == nil || config.settings.path != startup.path {
		return nil, errors.New("space settings binding missing or mismatched")
	}
	access := config.access
	if err := config.hostTools.validateCatalog(config.platformCatalog); err != nil {
		return nil, err
	}
	config.platformCatalog = config.hostTools.catalog(config.platformCatalog)
	if access == nil || access.identity == nil {
		return nil, ErrIdentity
	}
	if err := access.authorizeRegisteredSpace(access.subject(), access.space.SpaceID); err != nil {
		return nil, err
	}
	roots := access.identity.Space.Roots
	// S1 adopts the registered locations. Neither a CLI override nor a config
	// reload may silently attach this runtime to a different business root.
	expected := spaceRoots{Home: filepath.Dir(startup.path)}
	if startup.Server.DataDir != nil {
		expected.Data = *startup.Server.DataDir
	}
	if startup.singleRoomDir != "" {
		expected.Data = startup.singleRoomDir
	}
	if startup.Server.TeamsDir != nil {
		expected.Teams = *startup.Server.TeamsDir
	}
	for _, value := range []*string{&expected.Home, &expected.Data, &expected.Teams} {
		if *value == "" {
			continue
		}
		resolved, err := resolveFutureDirectory(*value)
		if err != nil {
			return nil, err
		}
		*value = resolved
	}
	if roots != expected || roots.Data == "" {
		return nil, ErrIdentity
	}
	access.privateLark = config.hostTools.restricted
	access.privateScion = config.hostTools.scionAvailable(config.platformCatalog)
	// Preserve the original config document for atomic edits without passing
	// deployment policy as mutable Space settings. Existing files stay in place.
	if access.control != nil && access.space.SpaceID != access.control.space.SpaceID {
		startup.spaceID = access.space.SpaceID
	}
	startup.Access = accessConfig{}
	startup.Server.Listen, startup.Server.RemoteEnabled = nil, nil
	startup.Server.PublicBaseURL, startup.Server.AccessMode = "", ""
	startup.Server.TrustedProxies = nil
	startup.installationID = ""
	r := &SpaceRuntime{SpaceContext: access.space, roots: roots, startup: startup, config: config, defaults: startup.Runtime, factories: factories}
	if config.execution != nil {
		retained, err := retainedSpaceExecutionConfig(access.authority().config, startup)
		if err != nil {
			return nil, err
		}
		if retained != nil {
			selected, ok := config.execution.(*srt.Factory)
			if !ok {
				return nil, errors.New("retained execution requires the deployment SRT factory")
			}
			retainedFactory, err := selected.WithConfig(context.Background(), *retained)
			if err != nil {
				return nil, err
			}
			binding := agent.ExecutionBinding{UserID: r.OwnerUserID, SpaceID: r.SpaceID, Probe: true}
			if err := inspectExecutionLauncher(context.Background(), retainedFactory.ForSpace(r.OwnerUserID, r.SpaceID), binding, startup.Runtime); err != nil {
				return nil, err
			}
			r.config.execution = retainedFactory
			r.config.cliPath = filepath.Join(filepath.Dir(startup.Runtime.DSH.BinaryPath), "teamx")
		}
	}
	if scoped, ok := r.config.execution.(interface {
		ForSpace(string, string) agent.ExecutionFactory
	}); ok {
		r.config.execution = scoped.ForSpace(r.OwnerUserID, r.SpaceID)
	}
	r.startup.execution = r.config.execution
	r.startup.executionBinding = agent.ExecutionBinding{UserID: r.OwnerUserID, SpaceID: r.SpaceID}
	r.config.ownerUserID = r.OwnerUserID
	r.config.modelStartup = &r.startup
	r.config.access.document = config.settings.configDocument
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.done = make(chan struct{})
	r.assemblyDone = make(chan struct{})
	r.state.Store("created")
	return r, nil
}

func (r *SpaceRuntime) State() string { return r.state.Load().(string) }

func (r *SpaceRuntime) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var handler http.Handler
	if r.manager != nil {
		handler = r.manager
	} else if r.single != nil {
		handler = r.single
	}
	if handler == nil || r.State() == "closing" || r.State() == "stopped" {
		writeAPIError(w, ErrClosing)
		return
	}
	r.config.access.wrap(handler).ServeHTTP(w, req)
}

// Serve is single-use, including setup and interrupted migration. Their locks
// and workers settle before the server constructs a fresh business runtime.
func (r *SpaceRuntime) Serve(ctx context.Context, listener net.Listener, ready func(any) error, stderr io.Writer) (runErr error) {
	if r.config.access.identity.Space.Status != "active" || r.config.access.revoked.Load() {
		return ErrIdentity
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), r.closeWithTimeout())
	}
	if !r.state.CompareAndSwap("created", "starting") {
		return errSpaceStopped
	}
	stop := context.AfterFunc(ctx, r.cancel)
	defer stop()
	ctx = r.ctx
	defer close(r.done)
	defer r.cancel()
	if r.migrationErr != nil {
		r.state.Store("migration_required")
		defer func() { runErr = errors.Join(runErr, r.finishSetup()) }()
		return r.factories.migration(ctx, listener, r.startup, r.config.access, ready)
	}
	if r.setupRequired {
		r.state.Store("setup_required")
		r.config.access.mu.Lock()
		r.config.access.setupRequired = true
		r.config.access.mu.Unlock()
		defer func() { runErr = errors.Join(runErr, r.finishSetup()) }()
		return r.factories.setup(ctx, listener, r.startup, r.config.settings, r.config.access, ready, stderr, r.config.modelProbe)
	}
	defer func() { r.finishAssembly(); runErr = errors.Join(runErr, r.closeWithTimeout()) }()
	if r.startup.Models != nil && len(r.startup.Models.Entries) > 0 {
		models, err := r.factories.models(r.startup)
		if err != nil {
			return err
		}
		if r.startup.Runtime == nil {
			return errors.New("managed model launcher missing")
		}
		runtime := *r.startup.Runtime
		runtime.ManagedModels = true
		runtime.Model = dsh.ModelConfig{}
		r.startup.Runtime, r.defaults, r.config.models = &runtime, &runtime, models
	}
	if r.singleRoom {
		return r.serveSingle(ctx, listener, ready, stderr)
	}
	return r.serveManager(ctx, listener, ready, stderr)
}

func (r *SpaceRuntime) closeWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	return r.Close(ctx)
}

func (r *SpaceRuntime) Close(ctx context.Context) error {
	r.cancel()
	state := r.State()
	if state == "setup_required" || state == "migration_required" {
		select {
		case <-r.done:
			// Setup workers and their data lock are settled; retry any retained home lock.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if r.state.CompareAndSwap("created", "closing") {
		r.finishAssembly()
	}
	select {
	case <-r.assemblyDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	for !r.closeMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer r.closeMu.Unlock()
	if r.State() == "stopped" {
		return nil
	}
	r.state.Store("closing")
	var err error
	if r.manager != nil {
		err = r.manager.shutdown(ctx)
	} else if r.single != nil {
		err = r.single.close(ctx)
	}
	if !backendCleanupSettled(err) {
		return err
	} // retain this runtime and all remaining handles for retry
	settledErr := err // preserve recovery diagnostics while releasing settled owners
	if r.manager == nil && r.computer != nil {
		if err := r.computer.closeContext(ctx); err != nil {
			return errors.Join(settledErr, err)
		}
	}
	if r.homeLock != nil {
		if err := unlockDirectory(r.homeLock); err != nil {
			return errors.Join(settledErr, err)
		}
		r.homeLock = nil
	}
	r.state.Store("stopped")
	return settledErr
}

func (r *SpaceRuntime) finishSetup() error {
	r.closeMu.Lock()
	defer r.closeMu.Unlock()
	r.state.Store("closing")
	r.finishAssembly()
	// The setup/migration factory has joined its workers and released its data lock.
	if r.homeLock != nil {
		if err := unlockDirectory(r.homeLock); err != nil {
			return err
		}
		r.homeLock = nil
	}
	r.state.Store("stopped")
	return nil
}

func (r *SpaceRuntime) finishAssembly() { r.assemblyOnce.Do(func() { close(r.assemblyDone) }) }
