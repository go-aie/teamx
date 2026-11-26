package room

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
	"github.com/go-aie/teamx/internal/web"
)

// spaceServer owns routing and runtime references, never Room business state.
// All requests use the original HTTP transport (including streaming/hijacking).
type spaceRuntimeAssembly struct {
	roots     spaceRoots
	config    serviceConfig
	defaults  *teamRuntime
	factories spaceFactories
}

type spaceServer struct {
	gitlabApplications *gitlaboauth.Catalog
	gitlabCallbacks    gitlabCallbackRoutes
	assembly           spaceRuntimeAssembly
	adminMu            sync.Mutex // serializes local user status changes and settlement
	mu                 sync.Mutex
	directory          spaceDirectory
	writer             snapshotWriter
	access             *serverAccess
	initial            *SpaceRuntime
	entries            map[string]*spaceServerEntry
	ctx                context.Context
	cancel             context.CancelFunc
	stderr             io.Writer
	homeLock           *os.File
	registryLock       *os.File
	closeMu            sync.Mutex
	closing            bool
	lifecycle          sync.WaitGroup
	claims             *spaceDirectoryClaims
	resetBusy          map[string]bool // guarded by mu; operation authority remains spaces.json
}

type spaceServerEntry struct {
	ctx            context.Context
	cancel         context.CancelFunc
	lifecycleBusy  bool   // guarded by spaceServer.mu
	lifecycleError string // guarded by spaceServer.mu
	ready          chan any
	mu             sync.RWMutex
	runtime        *SpaceRuntime
	listener       *spaceHTTPListener
	err            error
	done           chan struct{}
}

type spaceLogWriter struct {
	sync.Mutex
	output io.Writer
}

func (w *spaceLogWriter) Write(data []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	return w.output.Write(data)
}

// Existing lifecycle factories retain their HTTP server and Shutdown contract.
// A routed listener publishes its handler to the single external HTTP server;
// it has no sockets or proxy and never buffers a stream or WebSocket.
type spaceHTTPListener struct {
	mu      sync.RWMutex
	handler http.Handler
	active  int
	idle    chan struct{}
	closed  bool
	addr    net.Addr
	done    chan struct{}
	once    sync.Once
}

func (l *spaceHTTPListener) Accept() (net.Conn, error) { <-l.done; return nil, net.ErrClosed }
func (l *spaceHTTPListener) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.once.Do(func() { close(l.done) })
	return nil
}
func (l *spaceHTTPListener) Addr() net.Addr { return l.addr }
func serveRuntimeHTTP(server *http.Server, listener net.Listener) error {
	if l, ok := listener.(*spaceHTTPListener); ok {
		l.mu.Lock()
		l.handler = server.Handler
		l.mu.Unlock()
	}
	err := server.Serve(listener)
	if _, ok := listener.(*spaceHTTPListener); ok && errors.Is(err, net.ErrClosed) {
		return http.ErrServerClosed
	}
	return err
}
func (l *spaceHTTPListener) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	handler := l.handler
	if l.closed || handler == nil {
		l.mu.Unlock()
		accessError(w, 503, "space_starting")
		return
	}
	if l.active == 0 {
		l.idle = make(chan struct{})
	}
	l.active++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.active--
		if l.active == 0 {
			close(l.idle)
		}
		l.mu.Unlock()
	}()
	handler.ServeHTTP(w, r)
}
func shutdownRuntimeHTTP(server *http.Server, listener net.Listener, ctx context.Context) error {
	if l, ok := listener.(*spaceHTTPListener); ok {
		_ = l.Close()
	}
	err := server.Shutdown(ctx)
	if l, ok := listener.(*spaceHTTPListener); ok {
		l.mu.RLock()
		idle, active := l.idle, l.active
		l.mu.RUnlock()
		if active > 0 {
			select {
			case <-idle:
			case <-ctx.Done():
				err = errors.Join(err, ctx.Err())
				// SpaceRuntime.Close and spaceServer.Close enforce their own
				// caller deadlines. Keep this factory owner alive until its
				// admitted handlers settle, so setup defers cannot release
				// data/home locks while a handler can still add a worker.
				<-idle
			}
		}
	}
	return err
}

func newSpaceServer(ctx context.Context, initial *SpaceRuntime, stderr io.Writer) (*spaceServer, error) {
	return openSpaceServer(ctx, initial, spaceRuntimeAssembly{initial.roots, initial.config, initial.defaults, initial.factories}, stderr)
}

func newHostedSpaceServer(ctx context.Context, startup startupConfig, config serviceConfig, factories spaceFactories, stderr io.Writer) (*spaceServer, error) {
	return openSpaceServer(ctx, nil, spaceRuntimeAssembly{spaceRoots{Home: config.access.users.home}, config, startup.Runtime, factories}, stderr)
}

func openSpaceServer(ctx context.Context, initial *SpaceRuntime, assembly spaceRuntimeAssembly, stderr io.Writer) (*spaceServer, error) {
	access := assembly.config.access
	s := &spaceServer{assembly: assembly, access: access, initial: initial, entries: map[string]*spaceServerEntry{}, stderr: &spaceLogWriter{output: stderr}}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.writer.ops = defaultSnapshotOps()
	lock, err := lockDirectoryFile(assembly.roots.Home, ".spaces-runtime.lock")
	if err != nil {
		s.cancel()
		return nil, err
	}
	s.registryLock = lock
	initialized := false
	defer func() {
		if !initialized {
			_ = unlockDirectory(lock)
		}
	}()
	if err := readIdentitySnapshot(filepath.Join(assembly.roots.Home, "spaces.json"), &s.directory); err != nil {
		s.cancel()
		return nil, err
	}
	if err := s.validateDirectory(s.directory); err != nil {
		s.cancel()
		return nil, err
	}
	if access.users != nil {
		if err := s.openGitLabApplications(); err != nil {
			return nil, err
		}
		access.users.mu.Lock()
		reserved := append([]externalUser{}, access.users.saved.Users...)
		access.users.mu.Unlock()
		for _, entry := range reserved {
			if err := s.publishDefaultSpaceLocked(entry); err != nil {
				s.cancel()
				return nil, err
			}
		}
		// The process lock proves no old runtime remains. Complete a disable
		// interrupted after its durable user fence without opening business.
		for i, record := range s.directory.Spaces {
			user, ok := access.users.user(record.OwnerUserID)
			if ok && user.Status == "disabled" && record.Status != "suspended" && !record.resetting() {
				record.Status = "suspended"
				record.Version++
				if err := s.saveSpaceLocked(i, record); err != nil {
					return nil, err
				}
			}
		}
		if assembly.factories.authentication == nil {
			return nil, errors.New("authentication binding missing")
		}
		provider, err := assembly.factories.authentication(access.config.Deployment.Authentication)
		if err != nil {
			return nil, err
		}
		access.login, err = newExternalLogin(provider, access.public, s.admitExternal)
		if err != nil {
			return nil, err
		}
		if err := s.configureModelDefaults(provider); err != nil {
			s.cancel()
			return nil, err
		}
	}
	if err := assembly.config.executionResources.checkSpaces(s.directory.Spaces); err != nil {
		s.cancel()
		return nil, err
	}
	s.claims = &spaceDirectoryClaims{home: assembly.roots.Home, directory: s.directory, writer: &s.writer}
	if err := s.claims.restore(); err != nil {
		s.cancel()
		return nil, err
	}
	access.spaces = s
	initialized = true
	return s, nil
}

func spaceRoute(id string) string { return "/s/" + id }

func (s *spaceServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := s.access.space.SpaceID // immutable
	if s.access.users != nil {
		if subject, ok := s.access.browserSubject(r); ok {
			id = s.access.defaultSpace(subject)
		}
	}
	path := r.URL.Path
	isGitLabCallback := path == gitlabCallbackPath
	var callbackSpace SpaceContext
	if isGitLabCallback {
		selected, ok := s.gitlabCallbacks.lookup(r.URL.Query().Get("state"))
		if !ok {
			accessError(w, 403, "identity_mismatch")
			return
		}
		id, callbackSpace = selected.SpaceID, selected
	}
	prefix := ""
	if strings.HasPrefix(path, "/s/") {
		selected, suffix, ok := strings.Cut(strings.TrimPrefix(path, "/s/"), "/")
		if !ok {
			http.Redirect(w, r, path+"/", http.StatusTemporaryRedirect)
			return
		}
		id, prefix, path = selected, spaceRoute(selected), "/"+suffix
	} else if strings.HasPrefix(path, "/v1/spaces/") && !spaceLifecyclePath(path) {
		selected, suffix, ok := strings.Cut(strings.TrimPrefix(path, "/v1/spaces/"), "/")
		if ok {
			id, path = selected, "/v1/"+suffix
		}
	}
	if path == gitlabCallbackPath && !isGitLabCallback {
		http.NotFound(w, r)
		return
	}
	if path == "/" && prefix == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		if id == "" {
			http.Redirect(w, r, "/access", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, spaceRoute(id)+"/", http.StatusSeeOther)
		return
	}
	copyURL := *r.URL
	copyURL.Path, copyURL.RawPath = path, ""
	r = r.Clone(r.Context())
	r.URL = &copyURL
	// Control-plane operations remain available while any runtime is in setup,
	// starting or failed. Execution credentials have no control-plane rights.
	if path == "/healthz" && prefix == "" || path == "/v1/spaces" || strings.HasPrefix(path, "/v1/spaces/") || strings.HasPrefix(path, "/v1/deployment/") || strings.HasPrefix(path, "/v1/access/") && (path != "/v1/access/session" || s.access.users != nil) || strings.HasPrefix(path, "/ui/") || path == "/access" {
		s.access.wrap(http.HandlerFunc(s.serveDirectory)).ServeHTTP(w, r)
		return
	}
	s.mu.Lock()
	entry := s.entries[id]
	closing := s.closing
	active, known := false, false
	for _, record := range s.directory.Spaces {
		if record.ID == id {
			if s.access.users != nil {
				subject, ok := s.access.browserSubject(r)
				// Execution requests are authenticated against the Space's own
				// credential map by its original transport below.
				if r.Header.Get("Authorization") == "" && (!ok || subject.UserID != record.OwnerUserID) {
					break
				}
			}
			active = record.Status == "active" && !record.resetting()
			known = true
			break
		}
	}
	s.mu.Unlock()
	if !known {
		accessError(w, 403, "identity_mismatch")
		return
	}
	var runtime *SpaceRuntime
	var listener *spaceHTTPListener
	var entryErr error
	if entry != nil {
		entry.mu.RLock()
		runtime, listener, entryErr = entry.runtime, entry.listener, entry.err
		entry.mu.RUnlock()
	}
	if isGitLabCallback && runtime != nil && runtime.SpaceContext != callbackSpace {
		accessError(w, 403, "identity_mismatch")
		return
	}
	if prefix != "" {
		w = &spaceResponseWriter{ResponseWriter: w, prefix: prefix}
	}
	available := false
	if listener != nil {
		listener.mu.RLock()
		available = listener.handler != nil
		listener.mu.RUnlock()
	}
	if !active || closing || runtime == nil || !available || entryErr != nil || runtime.ctx.Err() != nil || runtime.State() == "closing" || runtime.State() == "created" || runtime.State() == "starting" || runtime.State() == "stopped" {
		if path == "/v1/access/session" {
			s.access.wrap(http.HandlerFunc(s.serveDirectory)).ServeHTTP(w, r)
			return
		}
		if path == "/" && r.Method == http.MethodGet {
			copy := r.Clone(r.Context())
			url := *r.URL
			url.Path = "/space-loading"
			copy.URL = &url
			s.access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { web.Serve(w, r) })).ServeHTTP(w, copy)
		} else {
			accessError(w, 503, "space_unavailable")
		}
		return
	}
	listener.ServeHTTP(w, r)
}

// Preserve native ResponseController and WebSocket capabilities.
type spaceResponseWriter struct {
	http.ResponseWriter
	prefix string
	wrote  bool
}

func (w *spaceResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *spaceResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}
func (w *spaceResponseWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *spaceResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if location := w.Header().Get("Location"); strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "//") && !strings.HasPrefix(location, "/s/") {
		w.Header().Set("Location", w.prefix+location)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *spaceResponseWriter) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (s *spaceServer) start(record SpaceRecord, initial *SpaceRuntime, addr net.Addr) *spaceServerEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked(record, initial, addr)
}
func (s *spaceServer) startLocked(record SpaceRecord, initial *SpaceRuntime, addr net.Addr) *spaceServerEntry {
	entry := &spaceServerEntry{done: make(chan struct{}), ready: make(chan any, 1)}
	if s.closing {
		return nil
	}
	if old := s.entries[record.ID]; old != nil {
		return old
	}
	entry.ctx, entry.cancel = context.WithCancel(s.ctx)
	s.entries[record.ID] = entry
	go func() {
		defer close(entry.done)
		runtime := initial
		for {
			if entry.ctx.Err() != nil {
				if runtime != nil {
					_ = runtime.closeWithTimeout()
				}
				return
			}
			var err error
			if runtime == nil {
				runtime, err = s.assemble(record)
			}
			if err != nil {
				entry.mu.Lock()
				entry.err = err
				entry.mu.Unlock()
				return
			}
			listener := &spaceHTTPListener{addr: addr, done: make(chan struct{})}
			s.mu.Lock()
			entry.mu.Lock()
			entry.runtime, entry.listener, entry.err = runtime, listener, nil
			entry.mu.Unlock()
			admitted := entry.ctx.Err() == nil && !s.closing
			s.mu.Unlock()
			if !admitted {
				runtime.config.access.revokeSpace()
				err = runtime.closeWithTimeout()
				entry.mu.Lock()
				entry.err = err
				entry.mu.Unlock()
				return
			}
			err = runtime.Serve(entry.ctx, listener, func(info any) error {
				select {
				case entry.ready <- info:
				default:
				}
				return nil
			}, s.stderr)
			_ = listener.Close()
			s.mu.Lock()
			active := false
			for _, current := range s.directory.Spaces {
				if current.ID == record.ID {
					active = current.Status == "active"
					break
				}
			}
			s.mu.Unlock()
			if entry.ctx.Err() != nil || !active || !errors.Is(err, errModelSetupSaved) || s.ctx.Err() != nil || runtime.State() != "stopped" {
				entry.mu.Lock()
				entry.err = err
				entry.mu.Unlock()
				return
			}
			// Setup workers and both locks have settled. Never restart old objects.
			if record.ID == s.access.space.SpaceID && record.Reset == nil {
				startup, loadErr := readStartupConfig(s.assembly.config.settings.path, true, false)
				if loadErr != nil {
					entry.mu.Lock()
					entry.err = loadErr
					entry.mu.Unlock()
					return
				}
				config := s.assembly.config
				config.settings = newSystemSettings(startup)
				s.access.mu.Lock()
				s.access.document = config.settings.configDocument
				s.access.setupRequired = false
				s.access.mu.Unlock()
				config.access, err = s.access.forSpace(record)
				if err != nil {
					entry.mu.Lock()
					entry.err = err
					entry.mu.Unlock()
					return
				}
				runtime, err = newSpaceRuntime(startup, config, s.assembly.factories)
				if err != nil {
					entry.mu.Lock()
					entry.err = err
					entry.mu.Unlock()
					return
				}
				runtime.config.directoryClaims = s.claims
			} else {
				runtime = nil
			}
		}
	}()
	return entry
}

func (s *spaceServer) Serve(ctx context.Context, listener net.Listener, ready func(any) error) error {
	if s.initial != nil {
		s.homeLock, s.initial.homeLock = s.initial.homeLock, nil
	}
	s.mu.Lock()
	records := append([]SpaceRecord{}, s.directory.Spaces...)
	s.mu.Unlock()
	for _, record := range records {
		if record.resetting() {
			if record.ID == s.access.space.SpaceID {
				s.access.configFenced.Store(true)
			}
			s.mu.Lock()
			if s.resetBusy == nil {
				s.resetBusy = map[string]bool{}
			}
			s.resetBusy[record.ID] = true
			s.lifecycle.Add(1)
			s.mu.Unlock()
			go s.runSpaceReset(record, nil)
			continue
		}
		if record.Status == "archiving" {
			if _, err := s.changeSpaceStatus(record.ID, "archive", record.Version); err != nil {
				return errors.Join(err, s.closeWithTimeout())
			}
		}
	}
	for _, record := range records {
		if record.Status != "active" || record.resetting() {
			continue
		}
		var initial *SpaceRuntime
		if record.ID == s.access.space.SpaceID && record.Reset == nil {
			initial = s.initial
			initial.config.directoryClaims = s.claims
		}
		s.start(record, initial, listener.Addr())
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	// Report the stable control endpoint; a missing model does not delay it.
	s.mu.Lock()
	defaultEntry := s.entries[s.access.space.SpaceID]
	s.mu.Unlock()
	var info any
	if defaultEntry == nil {
		info = map[string]any{"endpoint": s.access.internal, "status": "space_suspended", "ready": false}
		if s.access.users != nil {
			info = map[string]any{"endpoint": s.access.internal, "status": "login_required", "ready": true}
		}
	} else {
		select {
		case info = <-defaultEntry.ready:
		case <-defaultEntry.done:
			info = map[string]any{"endpoint": s.access.internal, "status": "space_unavailable", "ready": false}
		case <-ctx.Done():
			_ = server.Close()
			return s.closeWithTimeout()
		}
	}
	if err := ready(info); err != nil {
		_ = server.Close()
		return errors.Join(err, s.closeWithTimeout())
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	cleanup := s.closeWithTimeout()
	shutdown, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	if httpErr := server.Shutdown(shutdown); httpErr != nil {
		_ = server.Close()
		err = errors.Join(err, httpErr)
	}
	return errors.Join(err, cleanup)
}

func (s *spaceServer) closeWithTimeout() error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	return s.Close(ctx)
}
func (s *spaceServer) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.cancel()
	for !s.closeMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer s.closeMu.Unlock()
	s.mu.Lock()
	s.closing = true
	entries := make([]*spaceServerEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	results := make(chan error, len(entries))
	for _, entry := range entries {
		go func() {
			select {
			case <-entry.done:
			case <-ctx.Done():
				results <- ctx.Err()
				return
			}
			entry.mu.RLock()
			r := entry.runtime
			entry.mu.RUnlock()
			if r != nil {
				results <- r.Close(ctx)
			} else {
				results <- nil
			}
		}()
	}
	var err error
	for range entries {
		err = errors.Join(err, <-results)
	}
	if err != nil {
		return err
	}
	joined := make(chan struct{})
	go func() { s.lifecycle.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.gitlabApplications != nil {
		if err := s.gitlabApplications.Close(ctx); err != nil {
			return err
		}
	}
	if err := s.writer.closeWriter(ctx); err != nil {
		return err
	}
	if s.access.users != nil {
		if err := s.access.users.writer.closeWriter(ctx); err != nil {
			return err
		}
		if err := s.access.sessionStore.closeWriter(ctx); err != nil {
			return err
		}
	}
	if s.registryLock != nil {
		if err := unlockDirectory(s.registryLock); err != nil {
			return err
		}
		s.registryLock = nil
	}
	if s.homeLock != nil {
		if err := unlockDirectory(s.homeLock); err != nil {
			return err
		}
		s.homeLock = nil
	}
	return nil
}

func (s *spaceServer) assemble(record SpaceRecord) (*SpaceRuntime, error) {
	if record.ID != s.access.space.SpaceID {
		if err := s.initializeSpace(record); err != nil {
			return nil, err
		}
	}
	if record.ID == s.access.space.SpaceID {
		record.Roots = s.assembly.roots
	}
	path := filepath.Join(record.Roots.Home, "config.toml")
	if record.ID == s.access.space.SpaceID {
		path = s.assembly.config.settings.path
	}
	startup, err := readStartupConfig(path, true, false)
	if err != nil {
		return nil, err
	}
	access, err := s.access.forSpace(record)
	if err != nil {
		return nil, err
	}
	config := s.assembly.config
	config.access, config.settings = access, newSystemSettings(startup)
	if record.ID == s.access.space.SpaceID {
		s.access.mu.Lock()
		s.access.document = config.settings.configDocument
		s.access.setupRequired = modelSetupRequired(startup)
		s.access.configFenced.Store(false)
		s.access.mu.Unlock()
	}
	config, err = mapSpaceBusinessConfig(config, startup)
	if err != nil {
		return nil, err
	}
	if s.gitlabApplications != nil {
		config.gitlabApplications = s.gitlabApplications.ForSpace(record.OwnerUserID, record.ID)
	}
	config.endpoint = s.access.internal + spaceRoute(record.ID)
	if record.ID == s.access.space.SpaceID {
		config.endpoint = s.assembly.config.endpoint
	} // preserve the original Session endpoint fingerprint
	config.environment = startupEnvironment(startup.Env)
	config.directoryClaims = s.claims
	config.workspaceProtected = append(append([]string{}, config.workspaceProtected...), s.assembly.roots.Home)
	r, err := newSpaceRuntime(startup, config, s.assembly.factories)
	if err != nil {
		return nil, err
	}
	r.setupRequired = modelSetupRequired(startup)
	if record.ID == s.access.space.SpaceID {
		return r, nil
	} // deployment owns the original Home lock
	r.homeLock, err = lockBundleHome(record.Roots.Home)
	if err != nil {
		r.cancel()
		return nil, err
	}
	return r, nil
}

func (a *serverAccess) authority() *serverAccess {
	if a.control != nil {
		return a.control
	}
	return a
}
func (a *serverAccess) forSpace(record SpaceRecord) (*serverAccess, error) {
	epoch, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var registration personalRegistration
	if a.users != nil {
		user, ok := a.users.user(record.OwnerUserID)
		if !ok || user.Status != "active" {
			return nil, ErrIdentity
		}
		registration.User = user.User
	} else {
		registration = *a.identity
	}
	registration.Space = record
	return &serverAccess{identity: &registration, space: SpaceContext{record.ID, record.OwnerUserID, epoch}, control: a, spaces: a.spaces,
		internal: a.internal, public: a.public, installationID: a.installationID, now: a.now,
		config: a.config, document: a.document, executions: map[[32]byte]executionBinding{}}, nil
}

func validateSpaceDirectory(directory spaceDirectory, original SpaceRecord, subject userSubject) error {
	if directory.Version != 1 || directory.DefaultSpaceID != original.ID || len(directory.Spaces) == 0 {
		return ErrIdentity
	}
	seen := map[string]bool{}
	found := false
	for _, record := range directory.Spaces {
		if !validSpaceID(record.ID) || seen[record.ID] || record.OwnerUserID != subject.UserID || !validSpaceStatus(record.Status) || record.Version == 0 || !validSpaceName(record.Name) {
			return ErrIdentity
		}
		seen[record.ID] = true
		if record.ID == original.ID {
			found = true
			if record.Roots != original.Roots {
				return ErrIdentity
			}
			continue
		}
		home := filepath.Join(original.Roots.Home, "spaces", record.ID)
		if record.Roots != (spaceRoots{home, filepath.Join(home, "data"), filepath.Join(home, "teams")}) {
			return ErrIdentity
		}
		for _, root := range []string{original.Roots.Data, original.Roots.Teams} {
			if root != "" && absoluteDirectoriesOverlap(root, home) {
				return ErrIdentity
			}
		}
		for _, root := range []string{record.Roots.Home, record.Roots.Data, record.Roots.Teams} {
			real, err := resolveFutureDirectory(root)
			if err != nil || real != root {
				return fmt.Errorf("space roots changed: %w", ErrIdentity)
			}
		}
	}
	if !found {
		return ErrIdentity
	}
	return nil
}

func validSpaceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && strings.ToLower(id) == id
}
