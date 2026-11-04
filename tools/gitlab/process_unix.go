//go:build unix

package main

import (
	"os/exec"
	"syscall"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	// Keep a killable process group without inheriting the caller's controlling
	// terminal. A separate group in the same terminal session receives SIGTTIN
	// when glab probes /dev/tty for terminal colors, leaving help stopped forever.
	// Standard input/output still pass through the explicitly supplied streams.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
}
