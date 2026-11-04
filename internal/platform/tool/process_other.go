//go:build !unix

package tool

import "os/exec"

func configureToolProcess(cmd *exec.Cmd) {}
func settleToolProcess(cmd *exec.Cmd)    {}
