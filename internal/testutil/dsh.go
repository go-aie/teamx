// Package testutil contains helpers shared by acceptance tests.
package testutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// DSHInstallation records the launcher actually used by a real acceptance run.
// It labels the evidence; it does not assert a particular source revision.
type DSHInstallation struct {
	BinaryPath string   `json:"binaryPath"`
	BinaryArgs []string `json:"binaryArgs,omitempty"`
	Version    string   `json:"version"`
	SHA256     string   `json:"binarySHA256"`
}

// InstalledDSH uses an explicit executable or the installed dsh on PATH.
// Optional arguments are a JSON string array, never shell text.
func InstalledDSH(ctx context.Context) (DSHInstallation, error) {
	var installation DSHInstallation
	binary := os.Getenv("TEAMX_DSH_BINARY_PATH")
	if binary == "" {
		binary = "dsh"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return installation, errors.New("installed DSH unavailable; set TEAMX_DSH_BINARY_PATH")
	}
	path, err = filepath.Abs(path)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return installation, errors.New("cannot resolve installed DSH executable")
	}
	installation.BinaryPath = path
	if raw := os.Getenv("TEAMX_DSH_BINARY_ARGS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &installation.BinaryArgs); err != nil || installation.BinaryArgs == nil {
			return installation, errors.New("TEAMX_DSH_BINARY_ARGS must be a JSON string array")
		}
	}
	executable, err := os.Open(path)
	if err != nil {
		return installation, errors.New("cannot hash installed DSH executable")
	}
	defer executable.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, executable); err != nil {
		return installation, errors.New("cannot hash installed DSH executable")
	}
	installation.SHA256 = hex.EncodeToString(hash.Sum(nil))
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := append(append([]string(nil), installation.BinaryArgs...), "--version")
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	output := &versionBuffer{}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return installation, errors.New("installed DSH --version failed")
	}
	installation.Version = strings.TrimSpace(output.buffer.String())
	if installation.Version == "" || strings.IndexFunc(installation.Version, unicode.IsSpace) >= 0 {
		return installation, errors.New("installed DSH --version must return one nonempty version string")
	}
	return installation, nil
}

type versionBuffer struct{ buffer bytes.Buffer }

func (b *versionBuffer) Write(data []byte) (int, error) {
	if b.buffer.Len()+len(data) > 1024 {
		return 0, errors.New("DSH version output exceeds 1024 bytes")
	}
	return b.buffer.Write(data)
}
