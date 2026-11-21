package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
)

// Session resources and input proofs stay with their original application
// owners. The selected factory sees only effective values and these operations.
func (h *sessionHost) PrepareOpen(ctx context.Context) (agent.Launch, error) {
	s := h.dshSession
	b, opts := s.backend, s.opts
	if b.checkWorkspace != nil {
		if err := b.checkWorkspace(); err != nil {
			return agent.Launch{}, err
		}
	}
	if b.inboxMCP != nil {
		b.prepare = func(ctx context.Context, input UnreadChanged, turn uint64, images bool) (*dshMaterials, error) {
			return b.inboxMCP.prepareRoomMaterials(ctx, input, turn, images)
		}
	}
	if err := ctx.Err(); err != nil {
		return agent.Launch{}, errors.Join(errBackendPaused, err)
	}
	ref, exists, err := b.reference(opts)
	if err != nil {
		return agent.Launch{}, errors.Join(errBackendPaused, err)
	}
	if exists && !b.recover {
		return agent.Launch{}, dshFailure(dsh.ErrRecoveryUnsupported)
	}
	if b.recover && !exists {
		return agent.Launch{}, dshFailure(dsh.ErrRecoveryUnsupported)
	}
	trustedWorkspace := !b.recover
	if ref.Directory != "" {
		frozen, loadErr := b.restoreModelSnapshot(opts, ref)
		if loadErr == nil {
			b = frozen
			trustedWorkspace = true
		} else if b.model != nil || ref.ManagedModel || !errors.Is(loadErr, os.ErrNotExist) {
			return agent.Launch{}, dshFailure(loadErr)
		}
	} else {
		ref = sessionReference{Version: formatVersion, MemberIdentity: opts.MemberIdentity, Directory: filepath.Base(dshStateDir(opts.StatePath))}
	}
	if b.model != nil && b.model.Legacy && (!b.recover || !exists) {
		return agent.Launch{}, dshFailure(dsh.ErrRecoveryUnsupported)
	}
	if !filepath.IsAbs(opts.CLIPath) {
		return agent.Launch{}, errors.Join(errBackendPaused, errors.New("absolute TeamX CLI path required"))
	}
	componentOpts, err := b.componentOptions(opts, ref)
	if err != nil {
		return agent.Launch{}, err
	}
	if err := b.checkModelRuntime(ctx, componentOpts); err != nil {
		return agent.Launch{}, dshFailure(err)
	}
	if !b.recover {
		if err := b.saveModelSnapshot(opts, ref); err != nil {
			return agent.Launch{}, dshFailure(err)
		}
	}
	// Saving a new generation freezes this hint with the existing profile.
	// Recovery never re-evaluates a saved alias to choose a different root.
	// Older snapshots need a canonical saved path as evidence; otherwise only
	// the optional file hint is suppressed, leaving execution/send unchanged.
	rootID := ""
	if b.model != nil {
		rootID = b.model.WorkspaceRootID
	}
	if rootID == "" && trustedWorkspace {
		canonical, canonicalErr := filepath.EvalSymlinks(componentOpts.Workspace)
		if canonicalErr == nil && filepath.IsAbs(canonical) && (b.model == nil && !b.recover || canonical == componentOpts.Workspace) {
			rootID = workspaceRootID(opts.RoomIdentity, opts.MembershipID, canonical)
		}
	}

	// Publish the adapter reference before new execution. This also guards a crash
	// before the component can publish opening.json or backend.json.
	path := opts.StatePath
	if !b.recover {
		b.store.mu.Lock()
		path, err = b.store.dshPath(opts.MemberIdentity, "session.json", true)
		if err == nil {
			ref.Pending = false // Consume fresh-generation authority before launch.
			ref.ManagedModel = b.model != nil
			err = b.store.save(path, ref, maxPrivateFileBytes)
		}
		b.store.mu.Unlock()
		if b.model != nil {
			b.store.changed("models", opts.MemberIdentity)
		}
	}
	if err != nil {
		return agent.Launch{}, errors.Join(errBackendPaused, err)
	}
	var revoke func()
	var inbox *mcpInboxSession
	if b.access != nil {
		binding := executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: opts.RoomIdentity}, Caller: Caller{Author: Author{Kind: "agent", ID: opts.AgentID}, MembershipID: opts.MembershipID}}
		// componentOpts uses the restored generation's profile on recovery.
		// Legacy recovery without a snapshot must never borrow today's cwd.
		binding.WorkspaceRootID = rootID
		for _, entry := range opts.CLIEnv {
			if strings.HasPrefix(entry, "TEAMX_SERVICE_EPOCH=") {
				binding.ServiceEpoch = strings.TrimPrefix(entry, "TEAMX_SERVICE_EPOCH=")
			}
		}
		token, release, issueErr := b.access.issueExecution(binding)
		if issueErr != nil {
			return agent.Launch{}, issueErr
		}
		revoke = release
		componentOpts.ExecutionToken = token
		if svc := b.mcpService(); svc != nil {
			token, releaseMCP, issueErr := b.access.issueMCPInbox(binding, svc, ref.Directory, slices.Contains(componentOpts.Model.InputModalities, "image"))
			if issueErr != nil {
				revoke()
				return agent.Launch{}, issueErr
			}
			revoke = sync.OnceFunc(func() { releaseMCP(); release() })
			componentOpts.MCPServers = []dsh.MCPHTTPServer{mcpHTTPConfiguration(componentOpts.ToolEnv["TEAMX_ENDPOINT"], token)}
			b.store.mcpOperations.mu.Lock()
			inbox = b.store.mcpOperations.inboxes[opts.MemberIdentity]
			b.store.mcpOperations.mu.Unlock()
		}
	} else if b.mcpService() != nil {
		return agent.Launch{}, ErrIdentity
	}
	s.backend, s.revoke, s.inbox = b, revoke, inbox
	h.prepared = true
	return launchOptions(componentOpts), nil
}

func (h *sessionHost) Opened(artifact *agent.Artifact) error {
	b := h.backend
	if b.model != nil && !b.model.Legacy {
		if artifact != nil && *artifact != b.model.Runtime.Agent {
			return errors.New("DSH ACP identity changed after model validation")
		}
	}
	return nil
}

func (s *dshSession) prepareTurn(ctx context.Context, input UnreadChanged, capabilities agent.TurnCapabilities) (prepared *agent.PreparedTurn, err error) {
	if s.backend.checkWorkspace != nil {
		if err := s.backend.checkWorkspace(); err != nil {
			return nil, err
		}
	}
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	defer func() {
		if err != nil {
			s.timeZone = ""
			release()
		}
	}()
	// Keep one settings snapshot even if a save occurs during preparation/run.
	settings, started := s.backend.settings.snapshot(), time.Now()
	if input.MemberIdentity != s.opts.MemberIdentity {
		return nil, errors.Join(errBackendPaused, ErrIdentity)
	}
	policy, err := s.backend.store.readAgentPolicy(s.opts.MemberIdentity)
	if err != nil {
		return nil, dshFailure(err)
	}
	var supplied any = policy.policyContext
	if s.policyToken != policy.PolicyToken {
		supplied = policy
	}
	body, err := json.Marshal(supplied)
	if err != nil {
		return nil, dshFailure(err)
	}
	s.backend.store.activeSessions.Store(s.opts.MemberIdentity, s)
	s.backend.store.changed("activity", s.opts.MemberIdentity)
	releases = append(releases, func() {
		s.backend.store.activeSessions.CompareAndDelete(s.opts.MemberIdentity, s)
		s.backend.store.changed("activity", s.opts.MemberIdentity)
	})
	notification := dshNotification(input)
	if s.backend.inputService != nil {
		svc := s.backend.inputService
		ref, _, e := s.backend.reference(s.opts)
		if e != nil {
			return nil, dshFailure(e)
		}
		if s.inbox != nil {
			s.input, err = s.inbox.inputFor(ctx, policy.CurrentTurn, ref.Directory)
		} else {
			p, e := svc.inputStorage(s.opts.MemberIdentity)
			if e != nil {
				return nil, dshFailure(e)
			}
			s.input, err = newInputSession(ctx, svc, p, ref.Directory, policy.CurrentTurn, capabilities.Images, func(ctx context.Context) error { return ctx.Err() })
		}
		if err != nil {
			return nil, dshFailure(err)
		}
		c, e := s.input.catalog(ctx)
		if e != nil {
			return nil, dshFailure(e)
		}
		if e := s.input.storage.run(ctx, "cache_catalog", func(context.Context) error { s.input.storage.keepInputCatalog(c); return nil }); e != nil {
			return nil, dshFailure(e)
		}
		directory, exposure, e := c.Directory()
		if e != nil {
			return nil, dshFailure(e)
		}
		directoryValue, e := s.input.chargeDiscovery(directory)
		if e != nil {
			return nil, dshFailure(e)
		}
		if _, supplied := directoryValue.(inputstream.Directory); supplied {
			if e = s.input.storage.saveInputExposure(s.input.execution, exposure); e != nil {
				return nil, dshFailure(e)
			}
		}
		b, e := json.Marshal(directoryValue)
		if e != nil {
			return nil, dshFailure(e)
		}
		notification = "Input directory (JSON):\n" + string(b)
	}
	if svc := s.backend.inputService; svc != nil {
		work, e := svc.supplyCalendar(ctx, s.opts.MemberIdentity, policy.CurrentTurn)
		if e != nil {
			return nil, dshFailure(e)
		}
		if len(work) > 0 {
			b, _ := json.Marshal(work)
			notification += "\nPending Calendar work (JSON):\n" + string(b) + "\nRead latest human intent, then read \"$TEAMX_CLI\" calendar --help and use calendar claim/finish with --turn current_turn. Claim at most one; never also send the report."
		}
	}
	prompt := []dsh.ContentBlock{{Type: "text", Text: "Member policy context (JSON):\n" + string(body) + "\n\n" + dshTimeContext(settings, started, s.timeZone) + "\n" + notification}}
	var materials *dshMaterials
	if s.backend.prepare != nil {
		materials, err = s.backend.prepare(ctx, input, policy.CurrentTurn, capabilities.Images)
		if err != nil {
			return nil, dshFailure(err)
		}
		prompt = materials.blocks(prompt[0].Text)
	}
	if !materialPromptFits(prompt) {
		return nil, dshFailure(errors.New("DSH material prompt exceeds text/frame budget"))
	}
	if !s.recoverySupplied && s.backend.recoveryContext != nil {
		recovery, err := s.backend.recoveryContext(ctx, input, policy.CurrentTurn)
		if err != nil {
			return nil, dshFailure(err)
		}
		prompt, err = recovery.appendTo(prompt)
		if err != nil {
			return nil, dshFailure(err)
		}
	}
	if materials != nil {
		if err := materials.check(ctx); err != nil {
			return nil, dshFailure(err)
		}
	}
	if s.inbox != nil && (s.backend.inputService == nil || s.backend.inputService.inboxMode != "cli") {
		for i := range prompt {
			prompt[i].Text = strings.ReplaceAll(prompt[i].Text, "inbox read", "the discovered inbox_read MCP tool")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, dshFailure(err)
	}
	if s.input != nil && s.backend.access != nil {
		s.input.executionContext = ctx
		token, revoke, e := s.backend.access.issueInputBinding(s.input)
		if e != nil {
			return nil, dshFailure(e)
		}
		releases = append(releases, revoke)
		if capabilities.AddRedactionSecret != nil {
			capabilities.AddRedactionSecret(token)
		}
		ref, _, e := s.backend.reference(s.opts)
		if e != nil {
			return nil, dshFailure(e)
		}
		path := filepath.Join(filepath.Dir(s.opts.StatePath), ref.Directory, "input-execution.json")
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return nil, dshFailure(e)
		}
		if e = saveDSHDiagnostic(path, map[string]string{"token": token, "endpoint": s.backend.inputService.endpoint}); e != nil {
			return nil, dshFailure(e)
		}
		releases = append(releases, func() { _ = os.Remove(path) })
	}
	return &agent.PreparedTurn{Prompt: prompt, Close: release, Finish: func(result agent.TurnResult, runErr error) error {
		if s.input != nil {
			runErr = errors.Join(runErr, s.input.confirmationError())
		} else if s.inbox != nil {
			runErr = errors.Join(runErr, s.inbox.confirmationError())
		}
		if runErr == nil {
			s.recoverySupplied = true
			s.policyToken = policy.PolicyToken
			s.timeZone = settings.TimeZone
		} else {
			// A failed prompt is not evidence that its body remains in context.
			s.policyToken = ""
		}
		// Keep only the last bounded private result. It is never a Room message,
		// and this file has no scheduling or backend recovery authority.
		if result.Events == nil {
			result.Events = []dsh.ActivityEvent{}
		}
		private := backendDiagnostic{Result: result}
		if runErr != nil {
			private.Error = runErr.Error()
		}
		s.backend.store.mu.Lock()
		path, saveErr := s.backend.store.dshPath(s.opts.MemberIdentity, "dsh-turn.json", true)
		if saveErr == nil {
			saveErr = saveDSHDiagnostic(path, private)
		}
		s.backend.store.mu.Unlock()
		s.backend.store.changed("activity", s.opts.MemberIdentity)
		if saveErr != nil {
			// Optional output must neither change the prompt's terminal outcome
			// nor freeze Room's authoritative snapshot commits.
			fmt.Fprintf(os.Stderr, "DSH member %q private output not saved: %v\n", s.opts.AgentID, saveErr)
		}
		if runErr != nil {
			s.timeZone = ""
		}
		return dshFailure(runErr)
	}}, nil
}
