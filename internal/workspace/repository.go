// Package workspace defines repository preparation independently of Room,
// platform providers and version control commands. The caller owns path safety,
// durable clone markers, configuration commits and Session lifecycle.
package workspace

import (
	"context"
	"errors"
)

// Execute must bound output, supply a controlled environment, honor cancellation
// and settle the process group before returning. It must not expose credentials
// in errors. Exit errors may implement ExitCode() int.
type Execute func(context.Context, string, map[string]string, string, ...string) ([]byte, error)

type Source struct {
	URL    string
	Branch string
}

// Access contains public runtime references only. Author is a lazy platform
// lookup; it must never return access material or be called during a revisit.
type Access struct {
	Env          map[string]string
	ResourceDir  string // Immutable platform resources; no credentials.
	Instructions string
	Author       func(context.Context) (string, string, error)
}

type Repository interface {
	// Check inspects local structure and identity without contacting the remote.
	// Missing/empty targets are accepted only for explicit initial preparation.
	Check(ctx context.Context, target string, initial bool) (exists bool, err error)
	Clone(ctx context.Context, parent, target string, env map[string]string) error
	// Configure may fill missing local settings on initial application. Revisit
	// must only validate them, preserving branch, files and author settings.
	Configure(ctx context.Context, target string, initial bool, access Access) error
}

type Registry map[string]func(Source) Repository

// Open treats the existing kind-less repository snapshots as Git. Resolving a
// backend never rewrites a snapshot; unsupported kinds cannot fall back to Git.
func (r Registry) Open(kind string, source Source) (Repository, error) {
	if kind == "" {
		kind = "git"
	}
	factory := r[kind]
	if factory == nil {
		return nil, errors.New("unsupported workspace repository kind")
	}
	return factory(source), nil
}
