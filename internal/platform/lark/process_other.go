//go:build !unix

package lark

import (
	"os/exec"
	"time"
)

func configureProcess(cmd *exec.Cmd) { cmd.WaitDelay = time.Second }
func settleProcess(cmd *exec.Cmd)    {}
