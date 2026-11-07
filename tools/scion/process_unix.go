//go:build unix

package main

import (
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	// A separate session prevents terminal prompts and gives cancellation a
	// process group to kill, including children started by the native CLI.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
}

func settleProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// Also remove descendants when the direct child exited first.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
