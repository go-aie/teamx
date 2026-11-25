package dsh

import (
	"context"
	"errors"
	"github.com/go-aie/teamx/agent"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

// ACP may advertise the adapter version. Version queries use the same
// execution selection and ownership contract as ACP, including partial starts.
func queryVersion(ctx context.Context, opts Options) (string, agent.Execution, error) {
	// Only an execution factory that verified immutable deployment files may
	// reuse a version. Native and other factories retain the original query path.
	if cache, ok := selectedExecution(opts).(interface {
		RuntimeVersion(context.Context, agent.ExecutionRequest, func(context.Context) (string, error)) (string, error)
	}); ok {
		var owned agent.Execution
		version, err := cache.RuntimeVersion(ctx, executionRequest(opts, opts.BinaryArgs), func(ctx context.Context) (string, error) {
			var version string
			var err error
			version, owned, err = queryUncachedVersion(ctx, opts)
			return version, err
		})
		return version, owned, err
	}
	return queryUncachedVersion(ctx, opts)
}

func queryUncachedVersion(ctx context.Context, opts Options) (string, agent.Execution, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.Limits.HandshakeTimeout)
	defer cancel()
	args := append(append([]string(nil), opts.BinaryArgs...), "--version")
	p, err := selectedExecution(opts).Start(ctx, executionRequest(opts, args), func(error) {})
	if p == nil {
		return "", nil, err
	}
	if err != nil {
		return "", p, err
	}
	_ = p.Stdin().Close()
	output := &versionOutput{}
	read := make(chan error, 1)
	go func() { _, err := io.Copy(output, p.Stdout()); read <- err }()
	select {
	case err = <-read:
	case <-ctx.Done():
		err = ctx.Err()
	}
	stopped := make(chan struct{})
	go func() { p.Stop(opts.Limits.CancelTimeout); close(stopped) }()
	cleanup, stop := context.WithTimeout(context.Background(), opts.Limits.CleanupTimeout)
	defer stop()
	select {
	case <-stopped:
		// Stop closes stdout even if an exited process left it inherited.
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if err != nil {
			return "", nil, err
		}
		if err := p.Result(); err != nil {
			return "", nil, err
		}
	case <-cleanup.Done():
		// The caller retains this exact handle and its state-directory lock.
		return "", p, errors.Join(ErrCleanupPending, cleanup.Err(), err)
	}
	version := strings.TrimSpace(string(output.data))
	if output.overflow || version == "" || strings.IndexFunc(version, unicode.IsSpace) >= 0 {
		return "", nil, errors.New("DSH --version must return one nonempty version string")
	}
	return version, nil, nil
}

var pendingRuntimeInspections = struct {
	sync.Mutex
	owners map[string]int
}{owners: map[string]int{}}

func installedVersion(ctx context.Context, opts Options) (string, error) {
	key := opts.ExecutionBinding.UserID + "\x00" + opts.ExecutionBinding.SpaceID + "\x00" + opts.BinaryPath
	pendingRuntimeInspections.Lock()
	pending := pendingRuntimeInspections.owners[key] != 0
	pendingRuntimeInspections.Unlock()
	if pending {
		return "", errors.Join(ErrCleanupPending, errors.New("runtime inspection cleanup is pending for this space"))
	}

	// Inspection has no business Session/lock. Own a separate private scratch
	// root until every acquired resource settles, even after the caller leaves.
	root, err := os.MkdirTemp("", "teamx-runtime-inspection-*")
	if err != nil {
		return "", err
	}
	opts.Workspace, opts.StateDir = filepath.Join(root, "workspace"), filepath.Join(root, "state")
	for _, dir := range []string{opts.Workspace, filepath.Join(opts.StateDir, "home"), filepath.Join(opts.StateDir, "agents")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			os.RemoveAll(root)
			return "", err
		}
	}
	opts.ExecutionBinding.Probe = true
	version, owned, err := queryVersion(ctx, opts)
	cleanup := func() {
		if owned != nil {
			owned.Stop(opts.Limits.CancelTimeout)
			pendingRuntimeInspections.Lock()
			pendingRuntimeInspections.owners[key]--
			if pendingRuntimeInspections.owners[key] == 0 {
				delete(pendingRuntimeInspections.owners, key)
			}
			pendingRuntimeInspections.Unlock()
		}
		_ = os.RemoveAll(root)
	}
	if owned != nil {
		pendingRuntimeInspections.Lock()
		pendingRuntimeInspections.owners[key]++
		pendingRuntimeInspections.Unlock()
		go cleanup()
	} else {
		cleanup()
	}
	return version, err
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
