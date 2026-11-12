package dsh

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// RuntimeIdentity includes interpreter and script bytes, not just Node's hash.
type RuntimeIdentity struct {
	BinaryPath   string   `json:"binary_path"`
	BinarySHA256 string   `json:"binary_sha256"`
	BinaryArgs   []string `json:"binary_args,omitempty"`
	ScriptSHA256 []string `json:"script_sha256,omitempty"`
	Version      string   `json:"version"`
	Agent        Artifact `json:"agent,omitzero"`
}

func InspectRuntime(ctx context.Context, opts Options) (RuntimeIdentity, error) {
	var id RuntimeIdentity
	path, err := filepath.EvalSymlinks(opts.BinaryPath)
	if err != nil {
		return id, errors.New("DSH executable unavailable")
	}
	id.BinaryPath = path
	id.BinaryArgs = append([]string(nil), opts.BinaryArgs...)
	id.BinarySHA256, err = executableDigest(path)
	if err != nil {
		return RuntimeIdentity{}, errors.New("DSH executable unavailable")
	}
	for _, arg := range opts.BinaryArgs {
		if strings.HasSuffix(arg, ".js") || strings.HasSuffix(arg, ".mjs") {
			hash, err := executableDigest(arg)
			if err != nil {
				return RuntimeIdentity{}, errors.New("DSH script unavailable")
			}
			id.ScriptSHA256 = append(id.ScriptSHA256, hash)
		}
	}
	if err := opts.Limits.defaults(); err != nil {
		return RuntimeIdentity{}, err
	}
	id.Version, err = installedVersion(ctx, opts)
	return id, err
}
