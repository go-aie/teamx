//go:build darwin || linux

package room

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func writeServiceCommandHelp(out io.Writer, action string, flags *flag.FlagSet) {
	fmt.Fprintf(out, "Usage: teamx service %s [OPTIONS]\n\n", action)
	switch action {
	case "upgrade":
		fmt.Fprintln(out, `升级已有安装；先下载校验，再停服、一致备份、切换版本并检查启动。
  teamx service upgrade                         升级到最新稳定版
  teamx service upgrade --version v0.0.3         指定正式版本
  teamx service upgrade --archive /path/to/package.tar.gz
省略 --backup-dir 时，在登记 home 旁的 <home>-backups 中生成新目录。
离线包默认读取同名 .sha256。已经是目标稳定版本时不下载应用包或停服。
备份父目录须已存在且可写；升级失败按输出检查状态、日志或恢复整份备份。`)
	case "backup":
		fmt.Fprintln(out, "一致备份会停服并保持停止。父目录须已存在且可写，目标目录须尚不存在。\n  teamx service backup --backup-dir /path/to/new-backup\n完成后需要继续运行时，执行 teamx service start。")
	case "restore":
		fmt.Fprintln(out, "恢复同一实例与原绝对路径；同时为恢复前状态创建新的备份。\n  teamx service restore --from /path/to/backup --backup-dir /path/to/new-pre-restore-backup\n不要仅切回旧程序代替数据恢复。")
	case "install":
		fmt.Fprintln(out, "从本机 standard 归档安装/重新登记服务。首次安装优先使用官方 install.sh。\n  teamx service install --archive /path/to/package.tar.gz\n已有安装升级请使用 teamx service upgrade。")
	case "logs":
		fmt.Fprintln(out, "查看系统服务日志；macOS 读取 server.log，Linux 使用 journald。\n  teamx service logs\n  teamx service logs --lines 200 --follow")
	case "uninstall":
		fmt.Fprintln(out, "停止并注销服务，保留配置、数据、版本和命令入口。\n重新登记原版本可运行官方 install.sh --repair 并提供原归档。")
	default:
		fmt.Fprintln(out, map[string]string{
			"status":  "查看管理器、认证和业务状态；端口可连接不等于业务就绪。",
			"inspect": "查看安装登记、数据主目录与服务账号。",
			"stop":    "停止服务并等待数据结清；使用 teamx service start 恢复。",
			"start":   "启动已登记服务并检查认证状态。",
			"restart": "等待旧服务停止后重新启动。",
			"enable":  "启用下次登录/开机自启动。",
			"disable": "禁用后续自启动；当前运行中的服务继续运行。",
		}[action])
	}
	fmt.Fprintln(out, "\n参数：")
	previous := flags.Output()
	flags.SetOutput(out)
	flags.PrintDefaults()
	flags.SetOutput(previous)
	fmt.Fprintln(out, "\nmacOS 在已登录普通用户下运行，不加 sudo；Linux 管理命令前加 sudo。")
}

func serviceShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func serviceDisplayCommand(r serviceRegistration, action string) string {
	command := serviceShellQuote(filepath.Join(r.Root, "current", "bin", "teamx")) + " service " + action + " --server-root " + serviceShellQuote(r.Root)
	if r.Platform == "linux" {
		command = "sudo " + command
	}
	return command
}

func serviceRestoreCommand(r serviceRegistration, backup string) string {
	return serviceDisplayCommand(r, "restore") + " --from " + serviceShellQuote(backup) + " --backup-dir " + serviceShellQuote(backup+"-before-restore-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
}

func writeServiceLogs(ctx context.Context, r serviceRegistration, lines int, follow bool, stdout, stderr io.Writer) error {
	name := "/usr/bin/tail"
	args := []string{"-n", strconv.Itoa(lines)}
	if follow {
		args = append(args, "-F")
	}
	args = append(args, filepath.Join(r.LogRoot, "server.log"))
	if r.Platform == "linux" {
		name = "/usr/bin/journalctl"
		args = []string{"--unit=teamx.service", "--no-pager", "--lines=" + strconv.Itoa(lines)}
		if follow {
			args = append(args, "--follow")
		}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = serviceEnvironment(r)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
