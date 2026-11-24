package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/agent"
	dshfactory "github.com/go-aie/teamx/builtin/agents/dsh"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

// DSH remains the only accepted backend. This selection creates no processes,
// reads no configuration/credentials and does not add a second runtime registry.
var defaultBackendFactory = dshfactory.NewFactory()

// Both execution and closed-session adapters start from this same selection.
func newSessionBackend(store *Store) dshBackend {
	return dshBackend{store: store, factory: defaultBackendFactory}
}

func (b dshBackend) selectedFactory() agent.Factory {
	factory := b.factory
	if factory == nil {
		factory = defaultBackendFactory
	}
	if b.open != nil {
		return fixtureComponentFactory{Factory: factory, open: b.open}
	}
	return factory
}

// Existing adapter fixtures replace only component acquisition. State and
// lifecycle operations still pass through the same selected factory contract.
type fixtureComponentFactory struct {
	agent.Factory
	open func(context.Context, dsh.Options) (dshComponent, error)
}

func (f fixtureComponentFactory) Open(ctx context.Context, launch agent.Launch, _ bool) (agent.Component, error) {
	return f.open(ctx, componentOptions(launch))
}

type sessionHost struct {
	*dshSession
	prepared bool
}

func (h *sessionHost) PrepareTurn(ctx context.Context, input agent.Input, capabilities agent.TurnCapabilities) (*agent.PreparedTurn, error) {
	return h.prepareTurn(ctx, input, capabilities)
}
func (h *sessionHost) Release() {
	if h.revoke != nil {
		h.revoke()
	}
}

// A checkpoint operation runs under its caller's original Store/directory
// lock. It supplies no general file or Store access to the selected backend.
type sessionCheckpoints struct{ path string }

func (c sessionCheckpoints) LoadReference() (agent.Reference, error) {
	var ref agent.Reference
	err := readSnapshot(c.path, &ref, maxPrivateFileBytes)
	return ref, err
}
func (c sessionCheckpoints) StateExists(directory string) (bool, error) {
	_, err := os.Lstat(filepath.Join(filepath.Dir(c.path), directory))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func launchOptions(o dsh.Options) agent.Launch {
	var credential *agent.Credential
	if o.Credential != nil {
		credential = &agent.Credential{Ref: o.Credential.Ref, Value: o.Credential.Value}
	}
	return agent.Launch{BinaryPath: o.BinaryPath, BinaryArgs: o.BinaryArgs, Artifact: o.Artifact,
		Workspace: o.Workspace, StateDir: o.StateDir, Instructions: o.Instructions,
		Model: agent.ModelConfig(o.Model), Credential: credential, ToolEnv: o.ToolEnv,
		ExecutionToken: o.ExecutionToken, MCPServers: o.MCPServers, InputBridge: o.InputBridge, CapabilityDigest: o.CapabilityDigest,
		ObserveActivity: o.ObserveActivity, TextBytes: o.Limits.TextBytes, TurnTimeout: o.Limits.TurnTimeout}
}

func componentOptions(l agent.Launch) dsh.Options {
	var credential *dsh.Credential
	if l.Credential != nil {
		credential = &dsh.Credential{Ref: l.Credential.Ref, Value: l.Credential.Value}
	}
	return dsh.Options{BinaryPath: l.BinaryPath, BinaryArgs: l.BinaryArgs, Artifact: l.Artifact,
		Workspace: l.Workspace, StateDir: l.StateDir, Instructions: l.Instructions,
		Model: dsh.ModelConfig(l.Model), Credential: credential, ToolEnv: l.ToolEnv,
		ExecutionToken: l.ExecutionToken, MCPServers: l.MCPServers, InputBridge: l.InputBridge, CapabilityDigest: l.CapabilityDigest,
		ObserveActivity: l.ObserveActivity, Limits: dsh.Limits{TextBytes: l.TextBytes, TurnTimeout: l.TurnTimeout}}
}

func (f fixtureComponentFactory) OpenSession(ctx context.Context, host agent.Host, resume bool) (*agent.Session, error) {
	return agent.OpenSession(ctx, f, host, resume)
}

func turnCapabilities(component agent.Component) agent.TurnCapabilities {
	c := agent.TurnCapabilities{Images: component.SupportsImages()}
	if private, ok := component.(interface{ AddRedactionSecret(string) }); ok {
		c.AddRedactionSecret = private.AddRedactionSecret
	}
	return c
}

func inspectSelectedRuntime(ctx context.Context, opts dsh.Options) (dsh.RuntimeIdentity, error) {
	id, err := defaultBackendFactory.InspectRuntime(ctx, launchOptions(opts))
	return dsh.RuntimeIdentity(id), err
}
