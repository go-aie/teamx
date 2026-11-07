// Package scion prepares native Scion working copies. Authentication and remote
// repository verification belong to the caller's platform access bridge.
package scion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/workspace"
)

type Adapter struct {
	Source workspace.Source
	Run    workspace.Execute
}

var _ workspace.Repository = Adapter{}

// Normalize accepts canonical HTTPS repository names, without Git suffix rules.
func Normalize(s workspace.Source) (workspace.Source, error) {
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || len(s.URL) > 4096 {
		return s, errors.New("invalid Scion repository URL")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	if !validName(strings.TrimPrefix(u.Path, "/")) || s.Branch != "" && !validName(s.Branch) {
		return s, errors.New("invalid Scion repository or branch")
	}
	s.URL = u.String()
	return s, nil
}
func validName(s string) bool {
	if s == "" || len(s) > 2048 {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
				return false
			}
		}
	}
	return true
}
func (a Adapter) Clone(ctx context.Context, parent, target string, env map[string]string) error {
	if a.Source.Branch == "" {
		return errors.New("Scion branch must be verified before clone")
	}
	_, err := a.Run(ctx, parent, env, "scion", "clone", a.Source.URL, target, "--ref", a.Source.Branch)
	if err != nil {
		return err
	}
	if _, err := a.Check(ctx, target, false); err != nil {
		return err
	}
	var state struct {
		Ref      string `json:"ref"`
		Detached bool
		Sparse   []string
	}
	if readJSON(filepath.Join(target, ".scion/state.json"), &state) != nil || state.Ref != a.Source.Branch || state.Detached || state.Sparse != nil {
		return errors.New("Scion clone differs from the verified initial branch or full checkout")
	}
	return nil
}
func (a Adapter) Configure(context.Context, string, bool, workspace.Access) error { return nil }

func (a Adapter) Check(_ context.Context, target string, initial bool) (bool, error) {
	entries, err := os.ReadDir(target)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(entries) == 0 {
		if initial {
			return false, nil
		}
		return false, errors.New("Scion workspace missing; explicit application required")
	}
	if err != nil {
		return false, err
	}
	fail := errors.New("Scion workspace identity or metadata mismatch; preserve and inspect target")
	// A mixed working copy is not a managed native Scion clone.
	if _, err := os.Lstat(filepath.Join(target, ".git")); !errors.Is(err, os.ErrNotExist) {
		return false, fail
	}
	meta := filepath.Join(target, ".scion")
	info, err := os.Lstat(meta)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, fail
	}
	if filepath.WalkDir(meta, func(_ string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSymlink != 0 {
			return fail
		}
		return nil
	}) != nil {
		return false, fail
	}
	var state struct {
		Format         int
		Endpoint, Repo string
		Ref            string `json:"ref"`
		Base, Head     *string
	}
	if readJSON(filepath.Join(meta, "state.json"), &state) != nil {
		return false, fail
	}
	u, err := url.Parse(a.Source.URL)
	if err != nil {
		return false, fail
	}
	if state.Format != 1 || state.Ref == "" || state.Endpoint != u.Scheme+"://"+u.Host || state.Repo != strings.TrimPrefix(u.Path, "/") {
		return false, fail
	}
	// An absent base is an empty/nonexistent ref, even when native clone exited 0.
	if state.Base == nil || !objectID(*state.Base) || state.Head != nil && !objectID(*state.Head) {
		return false, fail
	}
	var dir struct {
		Format int
		Files  map[string]json.RawMessage
	}
	if readJSON(filepath.Join(meta, "dirstate.json"), &dir) != nil || dir.Format != 1 || dir.Files == nil {
		return false, fail
	}
	return true, nil
}

// v0.1.13 changesets use a c1 prefix followed by the full 32-byte digest.
func objectID(s string) bool {
	if len(s) != 66 || !strings.HasPrefix(s, "c1") {
		return false
	}
	for _, c := range s[2:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return errors.New("invalid Scion metadata")
	}
	d := json.NewDecoder(io.LimitReader(f, 8<<20))
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid Scion metadata")
	}
	return nil
}
