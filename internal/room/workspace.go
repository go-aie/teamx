package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/workspace"
)

type preparedWorkspaces struct {
	ToolEnv      map[string]string
	Instructions string
}

type workspacePreparer struct {
	run        platform.Execute
	write      func(string, any) error
	remove     func(string) error
	repository func(platform.Execute, workspaceConfig) (workspace.Repository, error)
	access     func(context.Context, string, managedRoomConfig, bool) (workspace.Access, error)
}

func (p workspacePreparer) ensure(ctx context.Context, dir string, saved managedRoomConfig, initial bool) (preparedWorkspaces, error) {
	result := preparedWorkspaces{ToolEnv: map[string]string{}}
	if err := validateWorkspaceSnapshot(saved); err != nil {
		return result, err
	}
	w := *saved.Workspace
	var repository workspace.Repository
	if w.Repository != nil {
		var err error
		repository, err = p.repository(p.run, w)
		if err != nil {
			return result, err
		}
	}
	// Check every target before mutating any directory or querying the platform.
	exists := make([]bool, len(saved.Members))
	for i, member := range saved.Members {
		if !safeMembershipID(member.MembershipID) {
			return result, ErrIdentity
		}
		for _, other := range saved.Members[:i] {
			if workspacePathsOverlap(member.Workspace, other.Workspace) {
				return result, ErrInvalid
			}
		}
		if err := checkWorkspaceDirectory(dir, member.Workspace); err != nil {
			return result, err
		}
		target := filepath.Join(dir, filepath.FromSlash(member.Workspace))
		if w.Repository != nil {
			if _, err := os.Lstat(target + ".clone-pending"); !errors.Is(err, os.ErrNotExist) {
				return result, errors.New("incomplete workspace clone; preserve and inspect target")
			}
			var err error
			exists[i], err = repository.Check(ctx, target, initial)
			if err != nil {
				return result, err
			}
		}
	}
	access, err := p.access(ctx, dir, saved, initial)
	if err != nil {
		return result, err
	}
	result.ToolEnv, result.Instructions = access.Env, access.Instructions
	for i, member := range saved.Members {
		if err := ctx.Err(); err != nil {
			return result, errors.New("workspace preparation cancelled")
		}
		target := filepath.Join(dir, filepath.FromSlash(member.Workspace))
		if w.Repository == nil {
			if err := os.MkdirAll(target, 0700); err != nil {
				return result, err
			}
			continue
		}
		env := result.ToolEnv
		if !exists[i] {
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return result, err
			}
			marker := target + ".clone-pending"
			// The marker survives failed clone commands, including removal of
			// an incomplete repository by its CLI. Explicit retry cannot silently overwrite it.
			if err := p.write(marker, struct {
				URL string `json:"url"`
			}{w.Repository.URL}); err != nil {
				return result, err
			}
			if err := repository.Clone(ctx, dir, target, env); err != nil {
				return result, err
			}
			if _, err := repository.Check(ctx, target, false); err != nil {
				return result, err
			}
			if err := p.remove(marker); err != nil {
				return result, err
			}
		}
		if err := repository.Configure(ctx, target, initial, access); err != nil {
			return result, err
		}
	}
	return result, nil
}
