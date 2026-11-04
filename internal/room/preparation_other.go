//go:build !darwin && !linux

package room

import (
	"errors"
	"os/exec"
)

// Managed rooms already reject platforms without directory locking support.
func configurePreparation(*exec.Cmd) {}

func settlePreparation(*exec.Cmd) error {
	return errors.New("workspace process cleanup is unsupported on this platform")
}

func preparationGroupSettled(int) bool { return false }
