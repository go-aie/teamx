package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func (r *SpaceRuntime) serveSingle(ctx context.Context, listener net.Listener, ready func(any) error, stderr io.Writer) (runErr error) {
	config, startup, settings, access := r.config, r.startup, r.config.settings, r.config.access
	config.models = nil // Explicit single-Room profiles retain their original model binding.
	profiles, dir, roomID, create := config.dshProfiles, r.roots.Data, r.roomID, r.factories.service
	uiProfiles := profileNames(profiles)
	cliPath, err := spaceCLIPath(config.cliPath)
	if err != nil {
		return err
	}
	config.cliPath, config.uiProfiles = cliPath, uiProfiles
	svc, err := create(dir, roomID, config)
	if err != nil {
		return err
	}
	r.single = svc
	if err := ctx.Err(); err != nil {
		return err
	}
	computerControl, err := r.factories.computer(ctx, startup, settings, svc.info, access.installationID, access.space.OwnerUserID, func(member MemberIdentity) (bool, error) {
		if member.RoomID != svc.info.RoomID || member.RoomInstance != svc.info.RoomInstance {
			return false, nil
		}
		svc.store.mu.Lock()
		defer svc.store.mu.Unlock()
		state, err := svc.store.loadRoom()
		if err != nil {
			return false, err
		}
		for _, live := range state.Members {
			if live.AgentID == member.AgentID && live.MembershipID == member.MembershipID {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	computerControl.members, err = newComputerMembers(computerControl, nil, svc)
	if err != nil {
		computerControl.cancel()
		_ = computerControl.writer.Close(context.Background())
		_ = unlockDirectory(computerControl.lock)
		return err
	}
	settings.computer = computerControl
	svc.ownComputer = computerControl
	computerControl.start()
	r.computer = computerControl
	server := &http.Server{Handler: r, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- serveRuntimeHTTP(server, listener) }()
	if err := svc.resumeMembers(ctx); err != nil {
		_ = server.Close()
		return err
	}
	info := struct {
		ServiceInfo
		Endpoint string `json:"endpoint"`
	}{svc.info, internalAccessEndpoint(listener.Addr().String())}
	r.state.Store("ready")
	r.finishAssembly()
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
	cleanupErr := r.Close(shutdownCtx)
	if shutdownErr := shutdownRuntimeHTTP(server, listener, shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		return errors.Join(err, cleanupErr, fmt.Errorf("HTTP cleanup incomplete: %w", shutdownErr))
	}
	return errors.Join(err, cleanupErr)
}

// The manager owns all opened services and every admitted lifecycle operation.
func (r *SpaceRuntime) serveManager(ctx context.Context, listener net.Listener, ready func(any) error, stderr io.Writer) (runErr error) {
	dir, teamsDir, config, defaults, create := r.roots.Data, r.roots.Teams, r.config, r.defaults, r.factories.service
	var upgrade *inputUpgradeOptions
	if config.unifiedInputs {
		upgrade = &inputUpgradeOptions{log: config.inputUpgradeLog}
	}
	manager, err := r.factories.manager(dir, teamsDir, config.maxTurns, defaults, upgrade, config.platformCatalog)
	if err != nil {
		return err
	}
	r.manager = manager
	if err := ctx.Err(); err != nil {
		return err
	}
	config.cliPath, err = spaceCLIPath(config.cliPath)
	if err != nil {
		return err
	}
	manager.config, manager.factory = config, create
	if config.gitlabApplications != nil && config.gitlabApplications.Managed() {
		manager.platforms.SetOAuthOwner(r.OwnerUserID, r.SpaceID)
	}

	manager.larkClients = r.factories.platformClients()
	if config.hostTools.restricted {
		if err := checkWorkspaceDirectory(r.roots.Home, "platform-tmp"); err != nil {
			return err
		}
		config.platformTempRoot = filepath.Join(r.roots.Home, "platform-tmp")
		if err := makeDurableDir(config.platformTempRoot); err != nil {
			return err
		}
		manager.config.platformTempRoot = config.platformTempRoot
		manager.larkClients, err = manager.larkClients.WithTemporaryRoot(config.platformTempRoot)
		if err != nil {
			return err
		}
		// Same platform implementation; only its CLI storage/executable binding
		// changes. Legacy personal profiles remain on their original backend.
		if err := checkWorkspaceDirectory(r.roots.Home, "platform-cli/lark"); err != nil {
			return err
		}
		executable := tool.ResolveExecutable("lark-cli", "")
		executable, err = exec.LookPath(executable)
		if err != nil {
			return fmt.Errorf("Lark CLI dependency unavailable")
		}
		executable, err = filepath.Abs(executable)
		if err != nil {
			return err
		}
		manager.larkClients, err = manager.larkClients.WithStorage(filepath.Join(r.roots.Home, "platform-cli", "lark"), executable)
		if err != nil {
			return err
		}
	}
	manager.workspaceProtected = append(manager.workspaceProtected, config.workspaceProtected...)
	if config.modelStartup != nil {
		startup := config.modelStartup
		if startup.Server.WorkspaceRoots != nil {
			roots := append([]string{}, (*startup.Server.WorkspaceRoots)...)
			manager.workspaceRoots = &roots
		}
		home := filepath.Dir(startup.path)
		manager.workspaceProtected = append(manager.workspaceProtected, home)
	}
	if config.settings != nil {
		manager.settings = config.settings
		manager.settings.observe = func() { manager.updates.mark(uiUpdate{Topic: "settings"}) }
	}
	if config.models != nil && config.modelStartup != nil {
		manager.modelControl, err = r.factories.modelController(ctx, manager, *config.modelStartup, config.modelProbe)
		if err != nil {
			return err
		}
	}
	if config.modelStartup != nil && config.access != nil {
		control, err := r.factories.computer(ctx, *config.modelStartup, manager.settings, ServiceInfo{ServiceEpoch: manager.epoch}, config.access.installationID, config.access.space.OwnerUserID, func(member MemberIdentity) (bool, error) {
			manager.mu.Lock()
			defer manager.mu.Unlock()
			for _, room := range manager.index.Rooms {
				if room.RoomIdentity != member.RoomIdentity {
					continue
				}
				var state RoomSnapshot
				if err := readSnapshot(filepath.Join(manager.roomDir(room.RoomID), "room.json"), &state, maxRoomFileBytes); err != nil {
					return false, err
				}
				if state.RoomIdentity != member.RoomIdentity {
					return false, ErrIdentity
				}
				for _, live := range state.Members {
					if live.AgentID == member.AgentID && live.MembershipID == member.MembershipID {
						return true, nil
					}
				}
			}
			return false, nil
		})
		if err != nil {
			return err
		}
		if err := r.bindComputerClaims(control); err != nil {
			control.cancel()
			_ = control.writer.Close(context.Background())
			_ = unlockDirectory(control.lock)
			return err
		}
		control.members, err = newComputerMembers(control, manager, nil)
		if err != nil {
			control.cancel()
			_ = control.writer.Close(context.Background())
			_ = unlockDirectory(control.lock)
			return err
		}
		manager.settings.computer = control
		manager.ownComputer = control
		control.start()
		r.computer = control
	}
	if err := manager.startPlatformNotifications(); err != nil {
		return err
	}
	manager.mu.Lock()
	manager.startConnectNotificationsLocked()
	manager.mu.Unlock()
	server := &http.Server{Handler: r, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- serveRuntimeHTTP(server, listener) }()
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
	r.state.Store("ready")
	r.finishAssembly()
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
	cleanupErr := r.Close(shutdownCtx)
	if shutdownErr := shutdownRuntimeHTTP(server, listener, shutdownCtx); shutdownErr != nil {
		_ = server.Close()
		return errors.Join(err, cleanupErr, fmt.Errorf("HTTP cleanup incomplete: %w", shutdownErr))
	}
	return errors.Join(err, cleanupErr)
}

func spaceCLIPath(selected string) (string, error) {
	if selected == "" {
		var err error
		selected, err = os.Executable()
		if err != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(selected)
}
