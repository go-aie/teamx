package room

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Allow prompt cancellation, protocol close, and process exit to finish with
// their separate DSH budgets (five seconds each by default), plus assembly.
const serviceShutdownTimeout = 30 * time.Second

func serveWithCatalog(args []string, stdout, stderr io.Writer, create func(string, string, serviceConfig) (*service, error), catalog *platforms.Catalog) (runErr error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runConfiguredServer(ctx, args, stdout, stderr, serverOptions{create: create, platformCatalog: catalog})
}

// Process signals belong to the foreground adapter. The core can also be owned
// by a supervisor or a test without signalling the host process.
type serverOptions struct {
	platformCatalog *platforms.Catalog
	create          func(string, string, serviceConfig) (*service, error)
	listen          func(string, string) (net.Listener, error)
	ready           func(any) error
	probe           func(context.Context, dsh.Options) (dsh.ProbeResult, error)
	deployment      *deploymentComposition
	access          func(startupConfig, string) (*serverAccess, error)     // Internal test seam; production always uses newServerAccess.
	execution       func(context.Context, *startupConfig, ...string) error // Internal assembly test seam; not a config capability.
}

func runConfiguredServer(ctx context.Context, args []string, stdout, stderr io.Writer, options serverOptions) error {
	// Setup replaces only its settled SpaceRuntime; the entry listener and
	// other Spaces keep running throughout the transition.
	return runServerOnce(ctx, args, stdout, stderr, options)
}

func runServerOnce(ctx context.Context, args []string, stdout, stderr io.Writer, options serverOptions) (runErr error) {
	if options.deployment == nil {
		selected := personalDeployment(options.create)
		options.deployment = &selected
	}
	if err := options.deployment.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
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
	resetTeamsSource := flags.String("reset-teams-source", "", "trusted current-version teams seed for source deployments")
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
	var homeLock *os.File
	defer func() {
		if homeLock != nil {
			runErr = errors.Join(runErr, unlockDirectory(homeLock))
		}
	}()
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
		homeLock = lock
		if layout.Root != "" {
			if err := registerServerInstallation(layout); err != nil {
				return err
			}
			pending, err := homeHasPendingReset(layout.Home)
			if err != nil {
				return err
			}
			if !pending {
				if err := initializeBundleRuntime(layout.Release, layout.Home, filepath.Join(layout.Root, "current", "bin", "dsh")); err != nil {
					return err
				}
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
	if err := checkManagedExecution(startup, layout); err != nil {
		return err
	}
	selectedFactories := options.deployment.factories
	if startup.Deployment.Mode == "multi-user" {
		if selectedFactories.authentication == nil {
			return errors.New("authentication binding missing")
		}
		provider, err := selectedFactories.authentication(startup.Deployment.Authentication)
		if err != nil {
			return err
		}
		if provider == nil {
			return errors.New("authentication provider missing")
		}
		selectedFactories.authentication = func(externalAuthConfig) (authn.Provider, error) { return provider, nil }
	}
	plannedListen := *listen
	if !explicit["listen"] && startup.Server.Listen != nil {
		plannedListen = *startup.Server.Listen
	}
	selectExecution := options.execution
	if selectExecution == nil {
		selectExecution = selectAgentExecution
	}
	if err := selectExecution(ctx, &startup, internalAccessEndpoint(plannedListen)); err != nil {
		return err
	}
	selectedExecution := startup.execution
	if startup.Deployment.Mode == "multi-user" && homeLock == nil {
		if err := checkDeploymentRoot(startup); err != nil {
			return err
		}
		homeLock, err = lockBundleHome(filepath.Dir(startup.path))
		if err != nil {
			return err
		}
	}
	if layout.Home != "" {
		selectedSource := startup.source
		selectedMode := startup.Deployment.Mode
		if err := prepareFirstAccess(startup); err != nil {
			return err
		}
		startup, err = readStartupConfig(*configFile, false, false)
		if err != nil {
			return err
		}
		if err := checkManagedExecution(startup, layout); err != nil {
			return err
		}
		if selectedMode != startup.Deployment.Mode || selectedMode == "multi-user" && !bytes.Equal(selectedSource, startup.source) {
			return ErrConflict
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
	resetPending, err := startupResetPending(startup)
	if err != nil {
		return err
	}
	if startup.Deployment.Mode != "multi-user" && !explicit["room"] && !explicit["state-dir"] && !resetPending {
		installationID := startup.installationID
		startup, migrationErr = migrateModelConfiguration(startup, nil)
		startup.installationID = installationID
	}
	startup.execution = selectedExecution
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
	if singleRoom && startup.Deployment.Mode == "multi-user" {
		return errors.New("multi-user deployment does not accept single-room overrides")
	}
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
	if singleRoom {
		startup.singleRoomDir, err = filepath.Abs(*dir)
		if err != nil {
			return err
		}
	}
	access, err := accessFactory(startup, internalAccessEndpoint(listener.Addr().String()))
	if err != nil {
		return err
	}
	if access == nil {
		return errors.New("server identity binding missing")
	}
	hostTools, err := options.deployment.factories.hostTools(startup.Deployment.Mode)
	if err != nil {
		return err
	}
	config := serviceConfig{hostTools: hostTools, execution: startup.execution, executionResources: newExecutionResources(hostTools.resourceLimits(startup.Deployment.Resources)), platformCatalog: hostTools.catalog(options.platformCatalog),
		workspaceProtected: []string{layout.Root, layout.Home},
		environment:        startupEnvironment(startup.Env),
		unifiedInputs:      options.create == nil, inputUpgradeLog: stderr,
		modelStartup: &startup, modelProbe: options.probe, access: access, settings: settings,
		endpoint: internalAccessEndpoint(listener.Addr().String()), mergeWindow: *mergeWindow, maxTurns: *maxTurns,
		dshProfiles: profiles, dshTurnTimeout: *dshTurnTimeout, inboxExpiry: inboxExpiry,
		inboxMode: startup.Server.InboxMode, imageMaterials: startup.Server.ImageMaterials,
	}
	if *resetTeamsSource != "" {
		config.resetTeamsSource, err = resolveFutureDirectory(*resetTeamsSource)
		if err != nil {
			return err
		}
	} else if layout.Release != "" {
		config.resetTeamsSource = filepath.Join(layout.Release, "share", "teamx", "teams")
	} else if startup.Runtime != nil && startup.Runtime.DSH.BinaryPath != "" {
		config.resetTeamsSource = filepath.Join(filepath.Dir(filepath.Dir(startup.Runtime.DSH.BinaryPath)), "share", "teamx", "teams")
	}
	if access.users != nil {
		if err := config.hostTools.validateCatalog(config.platformCatalog); err != nil {
			return err
		}
		spaces, err := newHostedSpaceServer(ctx, startup, config, selectedFactories, stderr)
		if err != nil {
			return err
		}
		spaces.homeLock, homeLock = homeLock, nil
		options.deployment.spaces = spaces
		return spaces.Serve(ctx, listener, ready)
	}
	runtime, err := newSpaceRuntime(startup, config, selectedFactories)
	if err != nil {
		return err
	}
	runtime.homeLock, homeLock = homeLock, nil
	options.deployment.runtime = runtime
	runtime.roomID = *roomID
	runtime.singleRoom = singleRoom
	runtime.setupRequired = layout.Home != "" && (*setupModel || modelSetupRequired(startup))
	runtime.migrationErr = migrationErr
	if singleRoom {
		return runtime.Serve(ctx, listener, ready, stderr)
	}
	spaces, err := newSpaceServer(ctx, runtime, stderr)
	if err != nil {
		return errors.Join(err, runtime.closeWithTimeout())
	}
	options.deployment.spaces = spaces
	return spaces.Serve(ctx, listener, ready)
}
