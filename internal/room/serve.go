package room

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Allow prompt cancellation, protocol close, and process exit to finish with
// their separate DSH budgets (five seconds each by default), plus assembly.
const serviceShutdownTimeout = 30 * time.Second

func serve(args []string, stdout, stderr io.Writer) error {
	return serveWithFactory(args, stdout, stderr, newService)
}

func serveWithFactory(args []string, stdout, stderr io.Writer, create func(string, string, serviceConfig) (*service, error)) (runErr error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServer(ctx, args, stdout, stderr, serverOptions{create: create})
}

// Process signals belong to the foreground adapter. The core can also be owned
// by a supervisor or a test without signalling the host process.
type serverOptions struct {
	create         func(string, string, serviceConfig) (*service, error)
	listen         func(string, string) (net.Listener, error)
	ready          func(any) error
	setupCompleted bool
	probe          func(context.Context, dsh.Options) (dsh.ProbeResult, error)
	access         func(startupConfig, string) (*serverAccess, error) // Internal test seam; production always uses newServerAccess.
}

func runServer(ctx context.Context, args []string, stdout, stderr io.Writer, options serverOptions) error {
	originalListen := options.listen
	if originalListen == nil {
		originalListen = net.Listen
	}
	accessFactory := options.access
	if accessFactory == nil {
		accessFactory = newServerAccess
	}
	var sharedAccess *serverAccess
	options.access = func(c startupConfig, endpoint string) (*serverAccess, error) {
		if sharedAccess != nil {
			sharedAccess.setupRequired = false
			return sharedAccess, nil
		}
		var err error
		sharedAccess, err = accessFactory(c, endpoint)
		return sharedAccess, err
	}
	selected := ""
	options.listen = func(network, address string) (net.Listener, error) {
		if selected != "" {
			address = selected
		}
		listener, err := originalListen(network, address)
		if err == nil {
			selected = listener.Addr().String()
		}
		return listener, err
	}
	for {
		err := runServerOnce(ctx, args, stdout, stderr, options)
		if !errors.Is(err, errModelSetupSaved) {
			return err
		}
		options.setupCompleted = true
	}
}

func runServerOnce(ctx context.Context, args []string, stdout, stderr io.Writer, options serverOptions) (runErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	create := options.create
	if create == nil {
		create = newService
	}
	listenTCP := options.listen
	if listenTCP == nil {
		listenTCP = net.Listen
	}
	ready := options.ready
	if ready == nil {
		ready = func(value any) error { return writeJSON(stdout, value) }
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "", "startup TOML file (default: ~/.teamx/config.toml; optional unless explicitly specified)")
	home := flags.String("home", "", "private server home; enables protected first-run setup")
	setupModel := flags.Bool("setup-model", false, "enter authenticated model repair before normal startup; requires --home or --server-root")
	serverRoot := flags.String("server-root", "", "versioned server installation root; uses current release and platform home by default")
	roomID := flags.String("room", "design-review", "Room name")
	dir := flags.String("state-dir", "", "single-room directory (default: <config-dir>/data/design-review)")
	listen := flags.String("listen", "127.0.0.1:8088", "listen IP and port; non-loopback requires remote access and login")
	mergeWindow := flags.Duration("merge-window", 100*time.Millisecond, "member notification merge window")
	flags.Func("max-turns", "removed; use --session-max-turns", func(string) error {
		return fmt.Errorf("--max-turns was removed; use --session-max-turns: cumulative hard stop is now per-session rotation")
	})
	maxTurns := flags.Uint64("session-max-turns", defaultMaxTurns, "DSH attempts per Session before automatic rotation (positive; frozen for new managed rooms)")
	dshTurnTimeout := flags.Duration("dsh-turn-timeout", dsh.DefaultTurnTimeout, "maximum duration of a DSH turn, including model and tools (must be positive)")
	inboxDays := flags.Int("inbox-expire-after-days", 3, "Inbox expiration in days (positive; original event time)")
	inboxSkip := flags.Bool("inbox-skip-expired", true, "skip expired unconsumed Inbox input")
	dshConfig := flags.String("dsh-config", "", "JSON file of named DSH member profiles")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return ErrRequest
	}
	explicit := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if explicit["config"] && *configFile == "" {
		return fmt.Errorf("%w: --config requires a file", ErrRequest)
	}
	if explicit["home"] && *home == "" || explicit["server-root"] && *serverRoot == "" {
		return ErrRequest
	}
	if *setupModel && *home == "" && *serverRoot == "" {
		return fmt.Errorf("--setup-model requires --home or --server-root")
	}
	var layout serverLayout
	if *home != "" || *serverRoot != "" {
		if explicit["config"] || explicit["room"] || explicit["state-dir"] || explicit["dsh-config"] {
			return fmt.Errorf("%w: --home/--server-root cannot be combined with --config or single-room flags", ErrRequest)
		}
		var err error
		layout, err = resolveServerLayout(*serverRoot, *home)
		if err != nil {
			return err
		}
		lock, err := lockBundleHome(layout.Home)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, unlockDirectory(lock)) }()
		if layout.Root != "" {
			if err := registerServerInstallation(layout); err != nil {
				return err
			}
			if err := initializeBundleRuntime(layout.Release, layout.Home, filepath.Join(layout.Root, "current", "bin", "dsh")); err != nil {
				return err
			}
		}
		*configFile = filepath.Join(layout.Home, "config.toml")
	} else if !explicit["config"] {
		path, err := defaultStartupConfigPath()
		if err != nil {
			return err
		}
		*configFile = path
	}
	startup, err := readStartupConfig(*configFile, explicit["config"], layout.Home == "")
	if err != nil {
		return err
	}
	if layout.Home != "" {
		if err := prepareFirstAccess(startup); err != nil {
			return err
		}
		startup, err = readStartupConfig(*configFile, false, false)
		if err != nil {
			return err
		}
		if startup.Access.LoginEnabled == nil || *startup.Access.LoginEnabled {
			fmt.Fprintf(stderr, "管理 token 文件：%s\n", adminTokenPath(startup))
		}
	}
	if layout.Root != "" {
		var installation serverInstallation
		if err := readSnapshot(filepath.Join(layout.Root, "installation.json"), &installation, 16<<10); err != nil {
			return err
		}
		startup.installationID = installation.InstanceID
	}
	var migrationErr error
	if !explicit["room"] && !explicit["state-dir"] {
		installationID := startup.installationID
		startup, migrationErr = migrateModelConfiguration(startup, nil)
		startup.installationID = installationID
	}
	if err := pinBundledRuntime(&startup, layout); err != nil {
		return err
	}
	dataDir, teamsDir := startup.Server.DataDir, startup.Server.TeamsDir
	if layout.Root != "" {
		resolved, err := resolveFutureDirectory(*dataDir)
		if err != nil {
			return err
		}
		if pathWithin(layout.Root, resolved) {
			return fmt.Errorf("server data_dir must be outside the installation root")
		}
		*dataDir = resolved
		// Direct foreground serve gets the same packaged tool discovery as
		// start, pinned to the selected release rather than its current alias.
		previous, existed := os.LookupEnv("PATH")
		if err := os.Setenv("PATH", filepath.Join(layout.Release, "bin")+string(os.PathListSeparator)+filepath.Join(layout.Release, "runtime/node/bin")+string(os.PathListSeparator)+previous); err != nil {
			return err
		}
		defer func() {
			if existed {
				_ = os.Setenv("PATH", previous)
			} else {
				_ = os.Unsetenv("PATH")
			}
		}()
	}
	settings := newSystemSettings(startup)
	if err := applyStartupSettings(flags, explicit, startup); err != nil {
		return err
	}
	if layout.Root != "" {
		if startup.Access.LoginEnabled != nil && !*startup.Access.LoginEnabled {
			return errors.New("managed service requires login verification")
		}
		if err := validateManagedAccess(startup, *listen); err != nil {
			return err
		}
	}
	if *mergeWindow < 0 || *maxTurns == 0 {
		return fmt.Errorf("%w: session-max-turns must be positive and merge-window nonnegative", ErrRequest)
	}
	if *dshTurnTimeout <= 0 {
		return fmt.Errorf("%w: dsh-turn-timeout must be positive", ErrRequest)
	}
	inboxExpiry := notification.ExpiryPolicy{AfterDays: *inboxDays, SkipExpired: *inboxSkip}
	if err := inboxExpiry.Validate(); err != nil {
		return err
	}
	singleRoom := explicit["room"] || explicit["state-dir"]
	if singleRoom && !explicit["state-dir"] {
		if !validText(*dataDir) {
			return ErrRequest
		}
		*dir = filepath.Join(*dataDir, "design-review")
	}
	if !singleRoom && explicit["dsh-config"] {
		return fmt.Errorf("%w: managed rooms use --teams-dir and team.json; --dsh-config is only for single-room mode", ErrRequest)
	}
	if (!singleRoom && (!validText(*dataDir) || !validText(*teamsDir))) || (singleRoom && (!validText(*roomID) || !validText(*dir))) {
		return ErrRequest
	}
	profiles, err := loadDSHProfiles(*dshConfig)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || net.ParseIP(host) == nil {
		return fmt.Errorf("listen must use an IP and port")
	}
	if err := validateAccessListen(startup, *listen); err != nil {
		return err
	}
	restoreEnv, err := applyStartupEnv(startup.Env)
	if err != nil {
		return err
	}
	defer restoreEnv()
	network := "tcp4"
	if net.ParseIP(host).To4() == nil {
		network = "tcp6"
	}
	listener, err := listenTCP(network, *listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *listen, err)
	}
	defer listener.Close()
	accessFactory := options.access
	if accessFactory == nil {
		accessFactory = newServerAccess
	}
	access, err := accessFactory(startup, internalAccessEndpoint(listener.Addr().String()))
	if err != nil {
		return err
	}
	if migrationErr != nil {
		return serveModelMigrationBlocked(ctx, listener, startup, access, ready)
	}
	if access != nil {
		access.document = settings.configDocument
	}
	if layout.Home != "" && ((*setupModel && !options.setupCompleted) || modelSetupRequired(startup)) {
		access.setupRequired = true
		err := serveSetupRequired(ctx, listener, startup, settings, access, ready, stderr, options.probe)
		if !errors.Is(err, errModelSetupSaved) {
			return err
		}
		// The setup listener and data lock are settled before normal startup.
		_ = listener.Close()
		// Re-enter after releasing the home lock through the outer restart loop.
		return errModelSetupSaved
	}
	var models *modelExecution
	if startup.Models != nil && len(startup.Models.Entries) > 0 {
		models, err = loadModelExecution(startup)
		if err != nil {
			return err
		}
		if startup.Runtime == nil {
			return errors.New("managed model launcher missing")
		}
		runtime := *startup.Runtime
		runtime.ManagedModels = true
		runtime.Model = dsh.ModelConfig{}
		startup.Runtime = &runtime
	}
	if !singleRoom {
		return serveRoomManager(ctx, listener, *dataDir, *teamsDir, serviceConfig{modelStartup: &startup, modelProbe: options.probe, models: models, access: access, settings: settings, endpoint: internalAccessEndpoint(listener.Addr().String()), mergeWindow: *mergeWindow, maxTurns: *maxTurns, dshTurnTimeout: *dshTurnTimeout, inboxExpiry: inboxExpiry, imageMaterials: startup.Server.ImageMaterials}, startup.Runtime, create, ready, stderr)
	}
	uiProfiles := profileNames(profiles)
	cliPath, err := os.Executable()
	if err != nil {
		return err
	}
	cliPath, err = filepath.EvalSymlinks(cliPath)
	if err != nil {
		return err
	}
	svc, err := create(*dir, *roomID, serviceConfig{
		access: access, settings: settings,
		endpoint: internalAccessEndpoint(listener.Addr().String()), cliPath: cliPath, mergeWindow: *mergeWindow, maxTurns: *maxTurns,
		dshProfiles: profiles, uiProfiles: uiProfiles, dshTurnTimeout: *dshTurnTimeout, inboxExpiry: inboxExpiry, imageMaterials: startup.Server.ImageMaterials,
	})
	if err != nil {
		return err
	}
	defer func() {
		// Also runs on readiness output or HTTP startup errors.
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		defer cancel()
		runErr = errors.Join(runErr, svc.close(ctx))
	}()
	server := &http.Server{Handler: access.wrap(svc), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err := svc.resumeMembers(ctx); err != nil {
		_ = server.Close()
		return err
	}
	info := struct {
		ServiceInfo
		Endpoint string `json:"endpoint"`
	}{svc.info, internalAccessEndpoint(listener.Addr().String())}
	if err := ready(info); err != nil {
		_ = server.Close()
		return err
	}
	fmt.Fprintf(stderr, "Room UI: %s/\n", info.Endpoint)
	fmt.Fprintln(stderr, "new members start paused; saved enable/pause intent survives restart; settled recoverable faults receive one automatic recovery attempt")
	for _, member := range svc.recovered {
		if member.notice != "" {
			fmt.Fprintf(stderr, "restore %s: %s\n", member.id.AgentID, member.notice)
		}
	}
	roomExit := make(chan error, 1)
	go func() {
		exit, err := svc.room.Wait(svc.ctx)
		if err == nil {
			err = exit.Reason
		}
		roomExit <- err
	}()
	select {
	case <-ctx.Done():
	case err = <-roomExit:
		if err == nil {
			err = fmt.Errorf("Room exited")
		}
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	// Establish the admission boundary and stop member scheduling immediately.
	// HTTP remains owned until member tasks and Room have been joined; waiting
	// for HTTP first would let pending notifications start more backend turns.
	cleanupErr := svc.close(shutdownCtx)
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		return errors.Join(err, cleanupErr, fmt.Errorf("HTTP cleanup incomplete: %w", shutdownErr))
	}
	return errors.Join(err, cleanupErr)
}

// The manager owns all opened services and every admitted lifecycle operation.
func serveRoomManager(ctx context.Context, listener net.Listener, dir, teamsDir string, config serviceConfig, defaults *teamRuntime, create func(string, string, serviceConfig) (*service, error), ready func(any) error, stderr io.Writer) (runErr error) {
	manager, err := newTeamRoomManager(dir, teamsDir, config.maxTurns, defaults)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, manager.close()) }()
	config.cliPath, err = os.Executable()
	if err != nil {
		return err
	}
	config.cliPath, err = filepath.EvalSymlinks(config.cliPath)
	if err != nil {
		return err
	}
	manager.config, manager.factory = config, create
	if config.settings != nil {
		manager.settings = config.settings
	}
	if config.models != nil && config.modelStartup != nil {
		manager.modelControl, err = newModelController(ctx, manager, *config.modelStartup, config.modelProbe)
		if err != nil {
			return err
		}
	}
	if err := manager.notifications.Start(); err != nil {
		return err
	}
	manager.mu.Lock()
	manager.startConnectNotificationsLocked()
	manager.mu.Unlock()
	server := &http.Server{Handler: config.access.wrap(manager), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	// Default creation is local and independent of the browser selection. A
	// blocked assistant must not prevent existing Rooms from being served.
	manager.mu.Lock()
	assistant, assistantErr := manager.ensureDefaultAssistantLocked()
	manager.mu.Unlock()
	if assistantErr != nil {
		fmt.Fprintf(stderr, "Default assistant initialization: %v\n", assistantErr)
		if assistant != nil && assistant.NextStep != "" {
			fmt.Fprintf(stderr, "Configure the default assistant with `%s`, then restart TeamX to retry the original creation.\n", assistant.NextStep)
		}
	}
	if err := manager.restoreRuntimes(); err != nil {
		return err
	}
	info := struct {
		ServiceEpoch string `json:"service_epoch"`
		Endpoint     string `json:"endpoint"`
		DataDir      string `json:"data_dir"`
		TeamsDir     string `json:"teams_dir"`
	}{manager.epoch, internalAccessEndpoint(listener.Addr().String()), filepath.Clean(manager.store.dir), manager.teamsDir}
	if err := ready(info); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Room UI: %s/\nRoom catalog: %s/v1/rooms\nData directory: %s\nTeams directory: %s\n", info.Endpoint, info.Endpoint, info.DataDir, info.TeamsDir)
	select {
	case <-ctx.Done():
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	cleanupErr := manager.shutdown(shutdownCtx)
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		return errors.Join(err, cleanupErr, fmt.Errorf("HTTP cleanup incomplete: %w", shutdownErr))
	}
	return errors.Join(err, cleanupErr)
}
