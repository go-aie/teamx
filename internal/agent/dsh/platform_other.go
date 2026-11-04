//go:build !darwin && !linux

package dsh

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func lockDirectory(string) (*os.File, error) {
	return nil, errors.New("DSH sessions support macOS/Linux only")
}
func ownProcess(*exec.Cmd)                {}
func signalPID(int, syscall.Signal) error { return errors.New("unsupported platform") }
