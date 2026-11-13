//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func runService(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		_, err := io.WriteString(stdout, serviceCLIHelp)
		return err
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(stdout, serviceCLIHelp)
		return err
	}
	action := args[0]
	if action == "__run" {
		return superviseService(args[1:], stdout, stderr)
	}
	switch action {
	case "install", "inspect", "start", "status", "stop", "restart", "enable", "disable", "backup", "restore", "upgrade", "uninstall":
	default:
		return ErrRequest
	}
	flags := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("server-root", "", "managed installation root")
	home := flags.String("home", "", "independent private home (install only)")
	account := flags.String("user", "", "existing dedicated ordinary service account (Linux install only)")
	archive := flags.String("archive", "", "verified native standard .tar.gz (install/upgrade)")
	digest := flags.String("sha256", "", "expected archive SHA-256; defaults to ARCHIVE.sha256")
	backup := flags.String("backup-dir", "", "new private backup directory (backup/upgrade/restore)")
	source := flags.String("from", "", "consistent backup directory (restore only)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (*archive != "" || *digest != "") && action != "install" && action != "upgrade" || (*home != "" || *account != "") && action != "install" || *source != "" && action != "restore" || *backup != "" && action != "backup" && action != "upgrade" && action != "restore" {
		return ErrRequest
	}
	if (action == "install" || action == "upgrade") && *archive == "" {
		return errors.New("--archive is required")
	}
	if (action == "backup" || action == "upgrade" || action == "restore") && *backup == "" {
		return errors.New("--backup-dir is required and must be a new directory outside server-root and home")
	}
	if action == "restore" && *source == "" {
		return errors.New("restore requires --from and --backup-dir for the pre-restore backup")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultRoot, defaultHome, err := defaultServerDirectories(runtime.GOOS, userHome)
	if err != nil {
		return err
	}
	if *root == "" {
		*root = defaultRoot
		if executable, err := os.Executable(); err == nil {
			if installed := installedServiceRoot(executable); installed != "" {
				*root = installed
			}
		}
	}
	*root, err = resolveFutureDirectory(*root)
	if err != nil {
		return err
	}
	if action == "install" {
		if err := os.MkdirAll(*root, 0755); err != nil {
			return err
		}
	}
	lock, err := lockDirectoryFile(*root, ".service.lock")
	if err != nil {
		return err
	}
	defer unlockDirectory(lock)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if action == "install" || action == "upgrade" {
		if *digest == "" {
			b, err := os.ReadFile(*archive + ".sha256")
			if err != nil {
				return fmt.Errorf("supply --sha256 or archive sidecar: %w", err)
			}
			fields := strings.Fields(string(b))
			if len(fields) == 0 {
				return errors.New("empty archive checksum sidecar")
			}
			*digest = fields[0]
		}
	}
	if action == "install" {
		if *home == "" {
			*home = defaultHome
		}
		*home, err = resolveFutureDirectory(*home)
		if err != nil {
			return err
		}
		if pathWithin(*root, *home) || pathWithin(*home, *root) {
			return errors.New("server-root and home must be separate, non-overlapping directories")
		}
		var r serviceRegistration
		if _, err := os.Stat(filepath.Join(*root, "service.json")); err == nil {
			r, err = loadServiceRegistration(*root)
			if err != nil {
				return err
			}
			if r.Home != *home || *account != "" && *account != r.User {
				return errors.New("installation is bound to a different home or account")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else {
			r, err = newServiceRegistration(*root, *home, *account)
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(*root, "installation.json")); errors.Is(err, os.ErrNotExist) {
				entries, err := os.ReadDir(*home)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if len(entries) != 0 {
					return errors.New("new installation requires a new home; importing an existing bundle/developer home is not supported")
				}
			}
		}
		release, err := publishServiceArchive(*root, *archive, *digest)
		if err != nil {
			return err
		}
		if selected, err := filepath.EvalSymlinks(filepath.Join(*root, "current")); err == nil && selected != release {
			return errors.New("installation already selects another build; use service upgrade")
		}
		if err := selectServiceRelease(*root, release); err != nil {
			return err
		}
		if err := os.MkdirAll(r.Home, 0700); err != nil {
			return err
		}
		layout := serverLayout{Root: *root, Home: r.Home, Release: release}
		if err := registerServerInstallation(layout); err != nil {
			return err
		}
		var i serverInstallation
		if err := readSnapshot(filepath.Join(*root, "installation.json"), &i, 16<<10); err != nil {
			return err
		}
		r.InstallationID = i.InstanceID
		if err := os.Chmod(filepath.Join(*root, "installation.json"), 0644); err != nil {
			return err
		}
		if err := writeServiceJSON(filepath.Join(*root, "service.json"), r); err != nil {
			return err
		}
		if err := initializeServiceHome(ctx, r, release); err != nil {
			// A repeated install may encounter the live Server's home lock.
			// Attach only after checking the exact registered unit and identity.
			if checkServiceUnit(r) == nil {
				if status, statusErr := authenticatedServiceStatus(ctx, r); statusErr == nil {
					return writeServiceInstallResult(r, status, stdout, stderr)
				}
			}
			return err
		}
		if _, err := serviceDataRoot(r); err != nil {
			return err
		}
		unit, err := serviceTemplate(r)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(r.Unit); err == nil && !info.Mode().IsRegular() {
			return errors.New("existing service unit is not a regular file")
		}
		if got, err := os.ReadFile(r.Unit); err == nil && !bytes.Equal(got, unit) {
			return errors.New("service name is already registered to a different unit; refusing to overwrite")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(r.Unit), 0755); err != nil {
			return err
		}
		if r.LogRoot != "" {
			if err := os.MkdirAll(r.LogRoot, 0700); err != nil {
				return err
			}
		}
		if err := writeServiceFile(r.Unit, unit, 0644); err != nil {
			return err
		}
		if err := serviceManager(ctx, nil, r, "register"); err != nil {
			return err
		}
		if err := serviceManager(ctx, nil, r, "enable"); err != nil {
			return err
		}
		if err := serviceManager(ctx, nil, r, "start"); err != nil {
			return err
		}
		status, err := waitServiceStatus(ctx, r)
		if err != nil {
			_ = serviceManager(context.Background(), nil, r, "stop")
			return err
		}
		return writeServiceInstallResult(r, status, stdout, stderr)
	}
	r, err := loadServiceRegistration(*root)
	if err != nil {
		return err
	}
	if action == "inspect" {
		return writeJSON(stdout, r)
	}
	if err := checkServiceUnit(r); err != nil {
		return err
	}
	manager := func(action string) error { return serviceManager(ctx, nil, r, action) }
	switch action {
	case "status":
		managerState, managerErr := serviceManagerState(ctx, r)
		status, err := authenticatedServiceStatus(ctx, r)
		if err != nil {
			status = map[string]any{"authenticated": false, "ready": false, "detail": err.Error(), "installation_id": r.InstallationID}
		}
		status["manager"] = managerState
		if managerErr != nil {
			status["manager_error"] = managerErr.Error()
		}
		return writeJSON(stdout, status)
	case "enable", "disable":
		return manager(action)
	case "start", "restart":
		if action == "restart" {
			if err := manager("stop"); err != nil {
				return err
			}
			release, err := acquireStoppedHome(ctx, r)
			if err != nil {
				return err
			}
			release()
		}
		if err := manager("start"); err != nil {
			return err
		}
		status, err := waitServiceStatus(ctx, r)
		if err != nil {
			_ = manager("stop")
			return err
		}
		return writeJSON(stdout, status)
	case "stop", "uninstall", "backup", "upgrade", "restore":
		// Validate all input material before taking a working service down.
		var next string
		if action == "upgrade" {
			next, err = publishServiceArchive(*root, *archive, *digest)
			if err != nil {
				return err
			}
		}
		if action == "restore" {
			if _, err := verifyServiceBackup(*source, r); err != nil {
				return err
			}
		}
		if action != "stop" && action != "uninstall" {
			if _, err := serviceDataRoot(r); err != nil {
				return err
			}
		}
		if err := manager("stop"); err != nil {
			return err
		}
		acquire := acquireStoppedService
		if action == "stop" || action == "uninstall" {
			acquire = acquireStoppedHome
		}
		release, err := acquire(ctx, r)
		if err != nil {
			return err
		}
		defer func() {
			if release != nil {
				release()
			}
		}()
		if action == "uninstall" {
			return manager("unregister")
		}
		if action == "stop" {
			return nil
		}
		if err := createServiceBackup(r, *backup); err != nil {
			return fmt.Errorf("service remains stopped; backup: %w", err)
		}
		if action == "backup" {
			return writeJSON(stdout, map[string]string{"backup": *backup, "state": "stopped"})
		}
		if action == "restore" {
			if err := restoreServiceBackup(ctx, r, *source); err != nil {
				return fmt.Errorf("service remains stopped; restore: %w", err)
			}
		} else if err := selectServiceRelease(r.Root, next); err != nil {
			return err
		}
		release()
		release = nil
		if err := manager("start"); err != nil {
			return fmt.Errorf("backup preserved at %s; startup: %w", *backup, err)
		}
		status, err := waitServiceStatus(ctx, r)
		if err != nil {
			_ = manager("stop")
			return fmt.Errorf("new selection retained and service stopped; restore the consistent backup at %s: %w", *backup, err)
		}
		status["backup"] = *backup
		status["recovery_review"] = "Re-login and inspect model operations and member sessions; retry interrupted targets explicitly. No validation or platform action was replayed by the deployment command."
		return writeJSON(stdout, status)
	}
	return ErrRequest
}

// launchd starts one bounded supervisor. A persistent configuration error uses
// at most five attempts per registration, then requires an explicit start.
// KeepAlive is false, so bootout/stop cannot be undone by the supervisor.
func superviseService(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("service __run", flag.ContinueOnError)
	root := flags.String("server-root", "", "installation root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" || runtime.GOOS != "darwin" {
		return ErrRequest
	}
	r, err := loadServiceRegistration(*root)
	if err != nil {
		return err
	}
	log := &serviceLog{path: filepath.Join(r.LogRoot, "server.log"), limit: 10 << 20, keep: 5}
	stdout, stderr = log, log
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	for attempt := 0; attempt < 5; attempt++ {
		cmd := exec.Command(filepath.Join(r.Root, "current/bin/teamx"), "serve", "--server-root", r.Root, "--home", r.Home)
		cmd.Env, cmd.Stdout, cmd.Stderr = serviceEnvironment(r), stdout, stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
				return nil
			case <-time.After(40 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				return errors.New("server exceeded shutdown budget")
			}
		case err := <-done:
			if err == nil {
				return nil
			}
			fmt.Fprintf(stderr, "Server exited (%d/5): %v\n", attempt+1, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(10 * time.Second):
		}
	}
	return errors.New("service restart budget exhausted; inspect logs and explicitly start")
}
