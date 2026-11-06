//go:build unix

package lark

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if e == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return e
	}
	cmd.WaitDelay = time.Second
}
func settleProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
