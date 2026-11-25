package dsh

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/agent"
)

// RuntimeIdentity includes interpreter and script bytes, not just Node's hash.
type RuntimeIdentity struct {
	Execution    *agent.ExecutionIdentity `json:"execution,omitempty"`
	BinaryPath   string                   `json:"binary_path"`
	BinarySHA256 string                   `json:"binary_sha256"`
	BinaryArgs   []string                 `json:"binary_args,omitempty"`
	ScriptSHA256 []string                 `json:"script_sha256,omitempty"`
	Version      string                   `json:"version"`
	Agent        Artifact                 `json:"agent,omitzero"`
}

func InspectRuntime(ctx context.Context, opts Options) (RuntimeIdentity, error) {
	var id RuntimeIdentity
	path, err := filepath.EvalSymlinks(opts.BinaryPath)
	if err != nil {
		return id, errors.New("DSH executable unavailable")
	}
	id.BinaryPath = path
	// Query the same physical executable that was inspected. Managed current
	// links are outside an isolated execution's read-only release mounts.
	opts.BinaryPath = path
	id.BinaryArgs = append([]string(nil), opts.BinaryArgs...)
	id.BinarySHA256, err = executableDigest(path)
	if err != nil {
		return RuntimeIdentity{}, errors.New("DSH executable unavailable")
	}
	id.ScriptSHA256, err = scriptDigests(opts.BinaryArgs)
	if err != nil {
		return RuntimeIdentity{}, err
	}

	if err := opts.Limits.defaults(); err != nil {
		return RuntimeIdentity{}, err
	}
	id.Execution, err = executionIdentity(opts, false)
	if err != nil {
		return RuntimeIdentity{}, err
	}
	id.Version, err = installedVersion(ctx, opts)
	return id, err
}

func scriptDigests(args []string) ([]string, error) {
	var hashes []string
	for _, arg := range args {
		if strings.HasSuffix(arg, ".js") || strings.HasSuffix(arg, ".mjs") {
			hash, err := executableDigest(arg)
			if err != nil {
				return nil, errors.New("DSH script unavailable")
			}
			hashes = append(hashes, hash)
		}
	}
	return hashes, nil
}
