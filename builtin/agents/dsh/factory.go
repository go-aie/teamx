// Package dsh supplies TeamX's current DSH backend. NewFactory is pure; only
// Open and InspectRuntime can start a process. It does not manage a Gordis host.
package dsh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/agent"
	component "github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/persistence"
)

type factory struct{}

func NewFactory() agent.Factory { return factory{} }

func (f factory) OpenSession(ctx context.Context, host agent.Host, resume bool) (*agent.Session, error) {
	return agent.OpenSession(ctx, f, host, resume)
}

func (factory) Open(ctx context.Context, launch agent.Launch, resume bool) (agent.Component, error) {
	open := component.Open
	if resume {
		open = component.Resume
	}
	session, err := open(ctx, options(launch))
	if session == nil {
		return nil, err
	}
	return session, err
}

func (factory) CheckResume(path string) error { return component.CheckResume(path) }
func (factory) CheckReset(path string) error  { return component.CheckReset(path) }
func (factory) CheckResumeConfig(launch agent.Launch) error {
	return component.CheckResumeConfig(options(launch))
}
func (factory) InspectRuntime(ctx context.Context, launch agent.Launch) (agent.RuntimeIdentity, error) {
	id, err := component.InspectRuntime(ctx, options(launch))
	return agent.RuntimeIdentity(id), err
}

func (factory) Reference(host agent.Checkpoints, id agent.MemberIdentity, statePath string) (agent.Reference, bool, error) {
	ref, err := host.LoadReference()
	base := filepath.Base(statePath) + ".dsh"
	if err == nil {
		generation := strings.TrimPrefix(ref.Directory, base+"-")
		rotated := ref.Directory != generation && len(generation) == 32 && strings.Trim(generation, "0123456789abcdef") == ""
		if ref.Version != 1 || ref.MemberIdentity != id || (ref.Directory != base && !rotated) || (ref.Pending && !rotated) {
			return ref, false, fmt.Errorf("%w: DSH directory reference", persistence.ErrInvalid)
		}
		if ref.Pending {
			exists, err := host.StateExists(ref.Directory)
			if err != nil || exists {
				return ref, false, fmt.Errorf("%w: pending DSH generation already exists", persistence.ErrInvalid)
			}
		}
		return ref, !ref.Pending, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ref, false, err
	}
	exists, err := host.StateExists(base)
	if err != nil {
		return ref, false, err
	}
	if !exists {
		return ref, false, nil
	}
	return ref, false, fmt.Errorf("%w: DSH state directory has no adapter reference", persistence.ErrInvalid)
}

func options(l agent.Launch) component.Options {
	var credential *component.Credential
	if l.Credential != nil {
		credential = &component.Credential{Ref: l.Credential.Ref, Value: l.Credential.Value}
	}
	return component.Options{BinaryPath: l.BinaryPath, BinaryArgs: l.BinaryArgs, Artifact: l.Artifact,
		Workspace: l.Workspace, StateDir: l.StateDir, Instructions: l.Instructions,
		Model: component.ModelConfig(l.Model), Credential: credential, ToolEnv: l.ToolEnv,
		ExecutionToken: l.ExecutionToken, MCPServers: l.MCPServers, InputBridge: l.InputBridge,
		ObserveActivity: l.ObserveActivity, Limits: component.Limits{TextBytes: l.TextBytes, TurnTimeout: l.TurnTimeout}}
}
