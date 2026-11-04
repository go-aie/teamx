package dsh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// ACP may advertise the adapter's version rather than the installed CLI's.
// Query the same configured launcher, with bounded output and no shell.
func installedVersion(ctx context.Context, opts Options) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.Limits.HandshakeTimeout)
	defer cancel()
	args := append(append([]string(nil), opts.BinaryArgs...), "--version")
	cmd := exec.CommandContext(ctx, opts.BinaryPath, args...)
	cmd.Dir, cmd.Env = opts.Workspace, environment(opts)
	output := &versionOutput{}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	cmd.WaitDelay = time.Second
	ownProcess(cmd)
	cmd.Cancel = func() error { return signalPID(-cmd.Process.Pid, syscall.SIGKILL) }
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("DSH version query failed: %w", err)
	}
	version := strings.TrimSpace(string(output.data))
	if output.overflow || version == "" || strings.IndexFunc(version, unicode.IsSpace) >= 0 {
		return "", errors.New("DSH --version must return one nonempty version string")
	}
	return version, nil
}

type versionOutput struct {
	data     []byte
	overflow bool
}

func (w *versionOutput) Write(data []byte) (int, error) {
	n := min(len(data), 1024-len(w.data))
	w.data = append(w.data, data[:n]...)
	w.overflow = w.overflow || n < len(data)
	return len(data), nil
}
