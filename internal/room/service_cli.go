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
	return runServiceWithManager(args, stdout, stderr, nil)
}

func runServiceWithManager(args []string, stdout, stderr io.Writer, runner serviceRunner) (resultErr error) {
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
	case "install", "inspect", "start", "status", "stop", "restart", "enable", "disable", "backup", "restore", "upgrade", "uninstall", "logs":
	default:
		return fmt.Errorf("未知服务命令 %q；使用 teamx service --help 查看可用命令", action)
	}
	flags := flag.NewFlagSet("service "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { writeServiceCommandHelp(stdout, action, flags) }
	root := flags.String("server-root", "", "安装根目录；默认从当前 teamx 命令发现")
	home, account, archive, digest, backup, source, version := new(string), new(string), new(string), new(string), new(string), new(string), new(string)
	lines, follow := new(int), new(bool)
	if action == "install" {
		flags.StringVar(home, "home", "", "独立的私有配置与数据目录")
		if runtime.GOOS == "linux" {
			flags.StringVar(account, "user", "", "已存在的专用普通服务账号")
		}
	}
	if action == "install" || action == "upgrade" {
		flags.StringVar(archive, "archive", "", "本机 standard .tar.gz；升级时可省略以联网下载")
		flags.StringVar(digest, "sha256", "", "归档 SHA-256；默认读取 FILE.sha256")
	}
	if action == "upgrade" {
		flags.StringVar(version, "version", "", "正式版本 vX.Y.Z；省略或 latest 使用最新稳定版")
	}
	if action == "backup" || action == "upgrade" || action == "restore" {
		flags.StringVar(backup, "backup-dir", "", "安装根和 home 外的新目录；升级默认自动生成")
	}
	if action == "restore" {
		flags.StringVar(source, "from", "", "同一实例、原路径的一致备份目录")
	}
	if action == "logs" {
		flags.IntVar(lines, "lines", 100, "显示最近多少行日志（1–10000）")
		flags.BoolVar(follow, "follow", false, "持续查看新日志，Ctrl-C 退出")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("多余的位置参数；使用 teamx service %s --help 查看示例", action)
	}
	if action == "install" && *archive == "" {
		return errors.New("安装需要 --archive FILE；首次安装请运行官方 install.sh，已有安装请使用 teamx service upgrade")
	}
	if (action == "backup" || action == "restore") && *backup == "" {
		return errors.New("需要 --backup-dir DIR，使用安装根和 home 外尚不存在的目录；父目录须已存在")
	}
	if action == "restore" && *source == "" {
		return errors.New("恢复需要 --from DIR 和 --backup-dir DIR；使用 teamx service restore --help 查看示例")
	}
	if *archive != "" && *version != "" || *digest != "" && *archive == "" {
		return errors.New("--version 与 --archive 不能同时使用；--sha256 需要 --archive")
	}
	if *version != "" && *version != "latest" && !serviceStableVersion.MatchString(*version) {
		return errors.New("--version 需要 latest 或正式版本号，例如 v0.0.3")
	}
	if action == "logs" && (*lines < 1 || *lines > 10000) {
		return errors.New("--lines 必须为 1–10000")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	defaultRoot, _, err := defaultServerDirectories(runtime.GOOS, userHome)
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
	// A long-running log reader must not block lifecycle/upgrade commands.
	if action != "logs" {
		lock, err := lockDirectoryFile(*root, ".service.lock")
		if err != nil {
			return fmt.Errorf("无法锁定安装根 %s；请核对 --server-root、部署账号及是否有其他维护命令运行。首次安装请运行官方 install.sh: %w", *root, err)
		}
		defer unlockDirectory(lock)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if (action == "install" || action == "upgrade") && *archive != "" {
		if *digest == "" {
			b, err := os.ReadFile(*archive + ".sha256")
			if err != nil {
				return fmt.Errorf("请保留归档旁的 .sha256 文件，或传 --sha256 HEX: %w", err)
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
			*home, err = selectedServerHome(*root, runtime.GOOS, userHome)
			if err != nil {
				return err
			}
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
			return errors.New("已安装其他版本；尚未切换版本或停止服务。请使用 teamx service upgrade，或查看 teamx service upgrade --help")
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
		if err := checkServicePort(ctx, r, false); err != nil {
			return err
		}
		if err := serviceManager(ctx, runner, r, "register"); err != nil {
			return err
		}
		if err := serviceManager(ctx, runner, r, "enable"); err != nil {
			return err
		}
		if err := serviceManager(ctx, runner, r, "start"); err != nil {
			return err
		}
		status, err := waitServiceStatus(ctx, r)
		if err != nil {
			_ = serviceManager(context.Background(), runner, r, "stop")
			return err
		}
		return writeServiceInstallResult(r, status, stdout, stderr)
	}
	r, err := loadServiceRegistration(*root)
	if err != nil {
		return fmt.Errorf("无法读取安装登记（%s）；首次安装请运行官方 install.sh，已有安装请核对 --server-root 和部署账号: %w", *root, err)
	}
	if action == "inspect" {
		return writeJSON(stdout, r)
	}
	if action == "logs" {
		return writeServiceLogs(ctx, r, *lines, *follow, stdout, stderr)
	}
	if err := checkServiceUnit(r); err != nil {
		return err
	}
	manager := func(action string) error { return serviceManager(ctx, runner, r, action) }
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
		if err := checkServicePort(ctx, r, true); err != nil {
			return err
		}
		if action == "restart" {
			if err := manager("stop"); err != nil {
				return err
			}
			release, err := acquireStoppedHome(ctx, r)
			if err != nil {
				return err
			}
			release()
			if err := checkServicePort(ctx, r, false); err != nil {
				return err
			}
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
		stopped, backedUp, selected := false, false, false
		defer func() {
			if resultErr == nil || action == "stop" || action == "uninstall" {
				return
			}
			state := "尚未停止服务或切换版本"
			if stopped {
				state = "服务已请求停止，尚未切换版本"
			}
			if selected {
				state = "版本/数据已切换；请先检查日志和服务状态，保留一致备份"
			}
			fmt.Fprintln(stderr, state+"。")
			if backedUp {
				fmt.Fprintf(stderr, "一致备份：%s\n恢复命令：%s\n", *backup, serviceRestoreCommand(r, *backup))
			}
			fmt.Fprintf(stderr, "查看状态：%s\n查看日志：%s\n", serviceDisplayCommand(r, "status"), serviceDisplayCommand(r, "logs"))
			if stopped && !selected {
				fmt.Fprintf(stderr, "核对原因后启动原版本：%s\n", serviceDisplayCommand(r, "start"))
			}
		}()
		if action == "upgrade" || action == "restore" {
			if err := checkServicePort(ctx, r, true); err != nil {
				return err
			}
		}
		if *backup != "" {
			*backup, err = resolveFutureDirectory(*backup)
			if err != nil {
				return err
			}
		}
		if *source != "" {
			*source, err = resolveFutureDirectory(*source)
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
			if *backup != "" {
				if err := validateServiceBackupDestination(r, *backup); err != nil {
					return err
				}
			}
		}
		if action == "upgrade" {
			if *archive == "" {
				next, err = prepareServiceOnlineUpgrade(ctx, r, *version, stderr, officialServiceReleases())
			} else {
				fmt.Fprintln(stderr, "正在校验离线升级包…")
				next, err = publishServiceArchive(*root, *archive, *digest)
				if err == nil {
					if current, linkErr := filepath.EvalSymlinks(filepath.Join(r.Root, "current")); linkErr == nil && current == next {
						fmt.Fprintln(stderr, "已选用同一归档，无需升级；未停止服务。")
						return nil
					}
				}
			}
			if err != nil {
				return err
			}
			if next == "" {
				return nil
			}
			if *backup == "" {
				*backup, err = defaultServiceUpgradeBackup(r)
				if err != nil {
					return err
				}
			}
			if err := validateServiceBackupDestination(r, *backup); err != nil {
				return err
			}
			fmt.Fprintf(stderr, "升级包已就绪。即将停服并备份至：%s\n", *backup)
		}
		if action == "upgrade" || action == "restore" {
			// Downloads and validation may take time; check again before stop.
			if err := checkServicePort(ctx, r, true); err != nil {
				return err
			}
		}
		stopped = true
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
		if action == "upgrade" || action == "restore" {
			if err := checkServicePort(ctx, r, false); err != nil {
				return err
			}
		}
		if err := createServiceBackup(r, *backup); err != nil {
			return fmt.Errorf("service remains stopped; backup: %w", err)
		}
		backedUp = true
		if action == "backup" {
			return writeJSON(stdout, map[string]string{"backup": *backup, "state": "stopped"})
		}
		if action == "restore" {
			selected = true // Restore may partially replace data before returning an error.
			if err := restoreServiceBackup(ctx, r, *source); err != nil {
				return fmt.Errorf("service remains stopped; restore: %w", err)
			}
		} else {
			if err := checkServicePort(ctx, r, false); err != nil {
				return err
			}
			if err := selectServiceRelease(r.Root, next); err != nil {
				return err
			}
		}
		selected = true
		release()
		release = nil
		if err := checkServicePort(ctx, r, false); err != nil {
			return err
		}
		if err := manager("start"); err != nil {
			return fmt.Errorf("backup preserved at %s; startup: %w", *backup, err)
		}
		status, err := waitServiceStatus(ctx, r)
		if err != nil {
			_ = manager("stop")
			return fmt.Errorf("新版已选用，服务启动失败并已停止；备份保留在 %s: %w", *backup, err)
		}
		status["backup"] = *backup
		status["recovery_review"] = "Re-login and inspect model operations and member sessions; retry interrupted targets explicitly. No validation or platform action was replayed by the deployment command."
		fmt.Fprintf(stderr, "TeamX 已完成%s并启动。备份：%s\n请重新登录网页，检查模型与原会话。\n", map[string]string{"upgrade": "升级", "restore": "恢复"}[action], *backup)
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
