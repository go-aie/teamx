// Package srt provides the explicitly selected Linux execution environment.
// It does not own ACP, Session recovery, credentials or business settlement.
package srt

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

const Version = "0.0.83"

//go:embed package-lock.json
var dependencyLock []byte

//go:embed supervisor.mjs
var supervisor string

// Artifact pins installed bytes. Directory digests include sorted relative
// names, modes and contents; symlinks must remain inside that release tree.
type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Config struct {
	Node         Artifact   `json:"node"`
	Bwrap        Artifact   `json:"bwrap"`
	Socat        Artifact   `json:"socat"`
	Ripgrep      Artifact   `json:"ripgrep"`
	Runtime      Artifact   `json:"runtime"`   // complete locked npm installation
	ReadOnly     []Artifact `json:"read_only"` // pinned DSH/release trees
	Endpoints    []string   `json:"endpoints"` // exact deployment-authorized origins
	PrivateRoots []string   `json:"private_roots"`
}

type Factory struct {
	config      Config
	native      agent.ExecutionFactory
	user, space string
	leases      *leases
}
type leases struct {
	sync.Mutex
	active map[string]bool
}

func ReadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, errors.New("invalid SRT deployment manifest")
	}
	if dec.Decode(new(any)) != io.EOF {
		return c, errors.New("invalid SRT deployment manifest")
	}
	return c, nil
}

// New acquires nothing. Check is called only for the selected deployment,
// before credential access, listeners and recovery workers.
func New(c Config) *Factory {
	c.ReadOnly = slices.Clone(c.ReadOnly)
	c.Endpoints = slices.Clone(c.Endpoints)
	c.PrivateRoots = slices.Clone(c.PrivateRoots)
	return &Factory{config: c, native: dsh.NativeExecution(), leases: &leases{active: map[string]bool{}}}
}

func (f *Factory) ForSpace(user, space string) agent.ExecutionFactory {
	next := *f
	next.user, next.space = user, space
	return &next
}

func (f *Factory) Check(ctx context.Context) error {
	if err := hostCheck(); err != nil {
		return err
	}
	if _, err := f.runtimeIdentity(); err != nil {
		return err
	}
	if len(f.config.Endpoints) == 0 {
		return errors.New("SRT deployment requires explicit endpoint authority")
	}
	for _, raw := range f.config.Endpoints {
		if _, err := endpoint(raw); err != nil {
			return err
		}
	}
	for _, root := range f.config.PrivateRoots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return errors.New("invalid SRT protected root")
		}
		for _, read := range f.basePolicy().Filesystem.AllowRead {
			if within(read, root) || within(root, read) {
				return errors.New("SRT runtime grant overlaps a private root")
			}
		}
	}
	// The exact supervisor checks warnings and actually enters namespaces.
	// No model key, business environment or delegated token is supplied.
	scratch, err := supervisorDirectory()
	if err != nil {
		return err
	}
	r, err := f.launch(agent.ExecutionRequest{Workspace: "/"}, true, scratch)
	if err != nil {
		os.RemoveAll(scratch)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p, err := f.native.Start(ctx, r, func(error) {})
	for _, file := range r.Files {
		_ = file.Close()
	}
	if p == nil {
		os.RemoveAll(scratch)
		return err
	}
	_ = p.Stdin().Close()
	read := make(chan error, 1)
	go func() { _, e := io.Copy(io.Discard, p.Stdout()); read <- e }()
	select {
	case e := <-read:
		err = errors.Join(err, e)
	case <-ctx.Done():
		err = errors.Join(err, ctx.Err())
	}
	done := make(chan struct{})
	go func() { p.Stop(time.Second); close(done) }()
	select {
	case <-done:
		return errors.Join(err, p.Result(), os.RemoveAll(scratch))
	case <-ctx.Done():
		go func() { <-done; os.RemoveAll(scratch) }()
		return errors.New("SRT prerequisite cleanup is pending; refuse startup")
	}
}

func (f *Factory) Identify(r agent.ExecutionRequest) (agent.ExecutionIdentity, error) {
	id, err := f.runtimeIdentity()
	if err != nil {
		return id, err
	}
	if r.StateDir != "" {
		policy, err := f.policy(r)
		if err != nil {
			return id, err
		}
		data, err := json.Marshal(struct {
			Binding agent.ExecutionBinding
			Policy  any
		}{r.Binding, policy})
		if err != nil {
			return id, err
		}
		id.PolicySHA256 = digest(data)
	}
	return id, nil
}

func (f *Factory) Start(ctx context.Context, r agent.ExecutionRequest, failed func(error)) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := hostCheck(); err != nil {
		return nil, err
	}
	if _, err := f.Identify(r); err != nil {
		return nil, err
	}
	key := r.StateDir
	f.leases.Lock()
	if f.leases.active[key] {
		f.leases.Unlock()
		return nil, errors.New("SRT generation still owns execution resources")
	}
	f.leases.active[key] = true
	f.leases.Unlock()
	release := func() { f.leases.Lock(); delete(f.leases.active, key); f.leases.Unlock() }
	scratch, err := supervisorDirectory()
	if err != nil {
		release()
		return nil, err
	}
	free := release
	release = func() { os.RemoveAll(scratch); free() }
	command, err := f.launch(r, false, scratch)
	if err != nil {
		release()
		return nil, err
	}
	p, err := f.native.Start(ctx, command, failed)
	for _, file := range command.Files {
		_ = file.Close()
	}
	if p == nil {
		release()
		return nil, err
	}
	return &execution{Execution: p, release: release}, err
}

type execution struct {
	agent.Execution
	once    sync.Once
	release func()
}

func (e *execution) Stop(grace time.Duration) {
	e.Execution.Stop(grace)
	e.once.Do(e.release)
}

func (f *Factory) runtimeIdentity() (agent.ExecutionIdentity, error) {
	id := agent.ExecutionIdentity{Kind: "srt", Version: Version}
	if runtime.GOOS != "linux" {
		return id, errors.New("SRT execution requires Linux")
	}
	artifacts := []Artifact{f.config.Node, f.config.Bwrap, f.config.Socat, f.config.Ripgrep, f.config.Runtime}
	artifacts = append(artifacts, f.config.ReadOnly...)
	if len(f.config.ReadOnly) == 0 || len(f.config.PrivateRoots) == 0 {
		return id, errors.New("SRT requires pinned release and protected roots")
	}
	for _, a := range artifacts {
		if !filepath.IsAbs(a.Path) || filepath.Clean(a.Path) == "/" || len(a.SHA256) != 64 {
			return id, errors.New("SRT requires absolute pinned dependency paths")
		}
		hash, err := DigestPath(a.Path)
		if err != nil || hash != a.SHA256 {
			return id, errors.New("SRT installed dependency digest mismatch")
		}
	}
	lock, err := os.ReadFile(filepath.Join(f.config.Runtime.Path, "package-lock.json"))
	if err != nil || !bytes.Equal(bytes.TrimSpace(lock), bytes.TrimSpace(dependencyLock)) {
		return id, errors.New("SRT dependency lock differs from verified release")
	}
	var pkg struct{ Version string }
	data, err := os.ReadFile(filepath.Join(f.config.Runtime.Path, "node_modules/@anthropic-ai/sandbox-runtime/package.json"))
	if err != nil || json.Unmarshal(data, &pkg) != nil || pkg.Version != Version {
		return id, errors.New("SRT version differs from verified release")
	}
	data, _ = json.Marshal(struct {
		Artifacts  []Artifact
		Supervisor string
	}{artifacts, supervisor})
	id.RuntimeSHA256 = digest(data)
	return id, nil
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// DigestPath is also used by deployment preparation; it never installs files.
func DigestPath(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode().IsRegular() {
		file, err := os.Open(root)
		if err != nil {
			return "", err
		}
		defer file.Close()
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		return hex.EncodeToString(hash.Sum(nil)), err
	}
	if !info.IsDir() {
		return "", errors.New("dependency root must not be a symlink")
	}
	hash := sha256.New()
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", rel, info.Mode(), info.Size())
		switch {
		case info.IsDir():
		case info.Mode().IsRegular():
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(hash, file)
			_ = file.Close()
			if err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := filepath.EvalSymlinks(path)
			if err != nil || !within(root, target) {
				return errors.New("dependency symlink leaves pinned release")
			}
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprint(hash, link)
		default:
			return errors.New("unsupported dependency file")
		}
		fmt.Fprint(hash, "\x00")
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func sortedUnique(paths []string) []string { sort.Strings(paths); return slices.Compact(paths) }

// Linux pathname sockets have a short address limit. Keep host proxy sockets
// out of deep Session paths and out of the Agent's writable temp directory.
func supervisorDirectory() (string, error) {
	path, err := os.MkdirTemp("", "sx-")
	if err != nil {
		return "", err
	}
	if len(path) > 72 {
		os.RemoveAll(path)
		return "", errors.New("SRT host TMPDIR is too long for supervised Unix sockets")
	}
	return path, nil
}
