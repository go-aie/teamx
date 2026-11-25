package srt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-aie/teamx/agent"
)

// Verification belongs to this deployment process, and is shared by its Space
// factories. A new process verifies every release again, including retained ones.
type verifiedRuntime struct {
	mu          sync.Mutex
	identity    *agent.ExecutionIdentity
	checking    chan struct{}
	versions    map[string]string
	versionGate chan struct{}
}

func newVerifiedRuntime() *verifiedRuntime {
	return &verifiedRuntime{versions: map[string]string{}, versionGate: make(chan struct{}, 1)}
}

func (v *verifiedRuntime) check(ctx context.Context, verify func() (agent.ExecutionIdentity, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		v.mu.Lock()
		if v.identity != nil {
			v.mu.Unlock()
			return nil
		}
		if pending := v.checking; pending != nil {
			v.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		v.checking = make(chan struct{})
		v.mu.Unlock()
		id, err := verify()
		if err == nil {
			err = ctx.Err()
		}
		v.mu.Lock()
		if err == nil {
			v.identity = &id
		}
		close(v.checking)
		v.checking = nil
		v.mu.Unlock()
		return err
	}
}

func (f *Factory) runtimeIdentity() (agent.ExecutionIdentity, error) {
	f.verified.mu.Lock()
	defer f.verified.mu.Unlock()
	if f.verified.identity == nil {
		return agent.ExecutionIdentity{}, errors.New("SRT deployment has not passed startup verification")
	}
	return *f.verified.identity, nil
}

type runtimeSet struct {
	sync.Mutex
	factories map[string]*Factory
}

func configKey(c Config) string { data, _ := json.Marshal(c); return string(data) }

// WithConfig selects an archived deployment descriptor. It verifies each exact
// configuration once, before Space activation, rather than once per Agent.
func (f *Factory) WithConfig(ctx context.Context, c Config) (*Factory, error) {
	f.runtimes.Lock()
	key := configKey(c)
	next := f.runtimes.factories[key]
	if next == nil {
		next = New(c)
		next.runtimes, next.leases = f.runtimes, f.leases
		f.runtimes.factories[key] = next
	}
	f.runtimes.Unlock()
	if err := next.Check(ctx); err != nil {
		return nil, err
	}
	return next, nil
}

// RuntimeVersion implements the DSH adapter's optional version cache. Only
// executables/scripts inside the verified, protected dependency tree qualify.
// Failed or incompletely cleaned-up queries are never cached.
func (f *Factory) RuntimeVersion(ctx context.Context, r agent.ExecutionRequest, query func(context.Context) (string, error)) (string, error) {
	if _, err := f.runtimeIdentity(); err != nil {
		return "", err
	}
	if f.user == "" || f.space == "" || r.Binding.UserID != f.user || r.Binding.SpaceID != f.space {
		return "", errors.New("SRT execution space binding mismatch")
	}
	paths := []string{r.BinaryPath}
	for _, arg := range r.Args {
		if strings.HasSuffix(arg, ".js") || strings.HasSuffix(arg, ".mjs") {
			paths = append(paths, arg)
		}
	}
	for _, path := range paths {
		resolved, err := physical(path)
		if err != nil {
			return "", err
		}
		pinned := resolved == f.config.Node.Path
		for _, artifact := range f.config.ReadOnly {
			pinned = pinned || within(artifact.Path, resolved)
		}
		if !pinned {
			return query(ctx)
		}
	}
	data, _ := json.Marshal(struct {
		Binary string
		Args   []string
	}{r.BinaryPath, r.Args})
	key := string(data)
	v := f.verified
	select {
	case v.versionGate <- struct{}{}:
		defer func() { <-v.versionGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if version, ok := v.versions[key]; ok {
		return version, nil
	}
	version, err := query(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && version != "" {
		v.versions[key] = version
	}
	return version, err
}

// Protect both contents and pathname replacement. Read-only files owned by the
// core are insufficient: their owner could chmod them. Writable ancestors can
// replace an otherwise protected tree, so verify them up to the filesystem root.
func protectedDigest(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	for _, path := range []string{root, resolved} {
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			info, err := os.Stat(parent)
			if err != nil {
				return "", err
			}
			if err := checkProtectedPath(parent, info); err != nil {
				return "", err
			}
			if parent == filepath.Dir(parent) {
				break
			}
		}
	}
	return digestPath(root, checkProtectedPath)
}
