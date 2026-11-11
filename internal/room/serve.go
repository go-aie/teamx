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
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configFile := flags.String("config", "", "startup TOML file (default: ~/.teamx/config.toml; optional unless explicitly specified)")
	roomID := flags.String("room", "design-review", "Room name")
	dir := flags.String("state-dir", "", "single-room directory (default: <config-dir>/data/design-review)")
	listen := flags.String("listen", "127.0.0.1:8088", "loopback address")
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
	if !explicit["config"] {
		path, err := defaultStartupConfigPath()
		if err != nil {
			return err
		}
		*configFile = path
	}
	startup, err := loadStartupConfig(*configFile, explicit["config"])
	if err != nil {
		return err
	}
	dataDir, teamsDir := startup.Server.DataDir, startup.Server.TeamsDir
	settings := newSystemSettings(startup)
	if err := applyStartupSettings(flags, explicit, startup); err != nil {
		return err
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
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("listen must use a loopback IP and port")
	}
	restoreEnv, err := applyStartupEnv(startup.Env)
	if err != nil {
		return err
	}
	defer restoreEnv()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	if !singleRoom {
		return serveRoomManager(listener, *dataDir, *teamsDir, serviceConfig{settings: settings, endpoint: "http://" + listener.Addr().String(), mergeWindow: *mergeWindow, maxTurns: *maxTurns, dshTurnTimeout: *dshTurnTimeout, inboxExpiry: inboxExpiry, imageMaterials: startup.Server.ImageMaterials}, startup.Runtime, create, stdout, stderr)
	}
	uiProfiles := profileNames(profiles)
	cliPath, err := os.Executable()
	if err != nil {
		return err
	}
	svc, err := create(*dir, *roomID, serviceConfig{
		settings: settings,
		endpoint: "http://" + listener.Addr().String(), cliPath: cliPath, mergeWindow: *mergeWindow, maxTurns: *maxTurns,
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
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	server := &http.Server{Handler: svc, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	if err := svc.resumeMembers(signalCtx); err != nil {
		_ = server.Close()
		return err
	}
	ready := struct {
		ServiceInfo
		Endpoint string `json:"endpoint"`
	}{svc.info, "http://" + listener.Addr().String()}
	if err := writeJSON(stdout, ready); err != nil {
		_ = server.Close()
		return err
	}
	fmt.Fprintf(stderr, "Room UI: %s/\n", ready.Endpoint)
	fmt.Fprintln(stderr, "new members start paused; normally stopped completed sessions resume lazily; interrupted or unsafe sessions remain paused")
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
	case <-signalCtx.Done():
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
func serveRoomManager(listener net.Listener, dir, teamsDir string, config serviceConfig, defaults *teamRuntime, create func(string, string, serviceConfig) (*service, error), stdout, stderr io.Writer) (runErr error) {
	manager, err := newTeamRoomManager(dir, teamsDir, config.maxTurns, defaults)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, manager.close()) }()
	config.cliPath, err = os.Executable()
	if err != nil {
		return err
	}
	manager.config, manager.factory = config, create
	if config.settings != nil {
		manager.settings = config.settings
	}
	if err := manager.notifications.Start(); err != nil {
		return err
	}
	manager.mu.Lock()
	manager.startConnectNotificationsLocked()
	manager.mu.Unlock()
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: manager, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
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
	ready := struct {
		ServiceEpoch string `json:"service_epoch"`
		Endpoint     string `json:"endpoint"`
		DataDir      string `json:"data_dir"`
		TeamsDir     string `json:"teams_dir"`
	}{manager.epoch, "http://" + listener.Addr().String(), filepath.Clean(manager.store.dir), manager.teamsDir}
	if err := writeJSON(stdout, ready); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Room UI: %s/\nRoom catalog: %s/v1/rooms\nData directory: %s\nTeams directory: %s\n", ready.Endpoint, ready.Endpoint, ready.DataDir, ready.TeamsDir)
	select {
	case <-signalCtx.Done():
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	return errors.Join(err, manager.shutdown(ctx), server.Shutdown(ctx))
}
