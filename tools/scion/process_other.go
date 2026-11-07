//go:build !unix

package main

import (
	"os/exec"
	"time"
)

// Only darwin/arm64 is in the verified release lock. Other systems do not yet
// promise process-tree settlement; verify a release and that contract first.
func configureProcess(cmd *exec.Cmd) { cmd.WaitDelay = time.Second }
func settleProcess(cmd *exec.Cmd)    {}
