package room

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
)

var errDSHPaused = errors.New("DSH member paused; inspect backend failure before continuing")
var errDSHDisabled = errors.New("DSH backend disabled; replacement execution is forbidden")

// Profiles are supplied by the operator, never accepted from member HTTP
// requests. Single-room membership persists a profile name; managed creation
// freezes the team runtime and roles in its Room configuration.
type dshProfile struct {
	modelError   string
	managedModel bool
	BinaryPath   string            `json:"binary_path"`
	BinaryArgs   []string          `json:"binary_args,omitempty"`
	Artifact     dsh.Artifact      `json:"artifact,omitzero"`
	Workspace    string            `json:"workspace"`
	Instructions string            `json:"instructions"`
	Model        dsh.ModelConfig   `json:"model"`
	ToolEnv      map[string]string `json:"tool_env,omitempty"`
}

func (p dshProfile) checkCredentials() error {
	if p.modelError != "" {
		return errors.New(p.modelError)
	}
	if !p.managedModel && os.Getenv(p.Model.APIKeyEnv) == "" {
		return fmt.Errorf("DSH credential environment %q is empty; export it before starting TeamX or set it in config.toml [env] (serve does not load .env)", p.Model.APIKeyEnv)
	}
	return nil
}

func loadDSHProfiles(path string) (map[string]dshProfile, error) {
	if path == "" {
		return nil, nil
	}
	var profiles map[string]dshProfile
	if err := readSnapshot(path, &profiles, 1<<20); err != nil {
		return nil, err
	}
	if err := validateDSHProfiles(profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}

func validateDSHProfiles(profiles map[string]dshProfile) error {
	if len(profiles) == 0 || len(profiles) > maxMembers {
		return fmt.Errorf("DSH config requires 1..%d profiles", maxMembers)
	}
	for name, p := range profiles {
		if !validText(name) || !filepath.IsAbs(p.BinaryPath) || !filepath.IsAbs(p.Workspace) ||
			(p.Artifact.Source == "") != (p.Artifact.Version == "") || p.Model.ModelID == "" || p.Model.APIKeyEnv == "" {
			return fmt.Errorf("invalid DSH profile %q: absolute paths and model required; artifact source/version must be supplied together or omitted", name)
		}
		model := p.Model
		if model.Protocol == "" {
			model.Protocol = "deepseek-messages"
		}
		if err := dsh.CheckConfig(dsh.Options{BinaryPath: p.BinaryPath, BinaryArgs: p.BinaryArgs, Artifact: p.Artifact, Model: model}); err != nil {
			return fmt.Errorf("invalid DSH profile %q: %w", name, err)
		}
		for key := range p.ToolEnv {
			if isRoomToolEnv(key) || key == "TEAMX_CLI" {
				return fmt.Errorf("DSH profile %q overrides Room tool identity", name)
			}
		}
	}
	return nil
}

type dshComponent interface {
	Run(context.Context, string) (dsh.TurnResult, error)
	RunPrompt(context.Context, []dsh.ContentBlock) (dsh.TurnResult, error)
	SupportsImages() bool
	Close(context.Context) error
}

type dshBackend struct {
	checkWorkspace func() error
	inputService   *service
	models         *modelExecution
	model          *modelSessionSnapshot
	credentials    *modelCredentials
	access         *serverAccess
	// Nil only for explicit CLI compatibility or isolated adapter tests.
	inboxMCP        *service
	store           *Store
	settings        *systemSettings
	prepare         func(context.Context, UnreadChanged, uint64, bool) (*dshMaterials, error)
	recoveryContext func(context.Context, UnreadChanged, uint64) (*dshRecoveryContext, error)
	profile         dshProfile
	turnTimeout     time.Duration // Service policy; not part of the frozen Room runtime.
	recover         bool          // Only set by startup after checking normal shutdown evidence.
	// A narrow seam for offline adapter tests; production always uses dsh.Open.
	open func(context.Context, dsh.Options) (dshComponent, error)
}

// Reject missing credentials before admitting a turn or publishing a Session
// reference. A validation failure must not become ambiguous execution state.
func (b dshBackend) CheckRun() error {
	if b.checkWorkspace != nil {
		if err := b.checkWorkspace(); err != nil {
			return err
		}
	}
	if b.model != nil {
		_, err := b.credentials.get(b.model.CredentialRef)
		return err
	}
	if b.open != nil { // Offline adapters supply their own component.
		return nil
	}
	return b.profile.checkCredentials()
}

// session.json belongs to the adapter and contains only the stable directory
// reference. backend.json and the ACP SessionID remain owned by the component.
type dshReference struct {
	Version int `json:"version"`
	MemberIdentity
	Directory    string `json:"directory"`
	Pending      bool   `json:"pending,omitempty"` // Authorized fresh generation, not yet opened.
	ManagedModel bool   `json:"managed_model,omitempty"`
}

func dshStateDir(path string) string { return path + ".dsh" }

func (s *Store) dshPath(id MemberIdentity, name string, writing bool) (string, error) {
	path, _, err := s.memberPath(id, name, writing)
	return path, err
}

// Check both files before any process launch. Even a failed/incomplete Open
// leaves evidence that requires inspection, rather than a replacement session.
func (b dshBackend) checkState(opts SessionOptions) (bool, error) {
	_, exists, err := b.reference(opts)
	return exists, err
}

func (b dshBackend) reference(opts SessionOptions) (dshReference, bool, error) {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	path, err := b.store.dshPath(opts.MemberIdentity, "session.json", false)
	if err != nil {
		return dshReference{}, false, err
	}
	if path != opts.StatePath {
		return dshReference{}, false, ErrIdentity
	}
	return readDSHReference(path, opts.MemberIdentity)
}

// Read the adapter-owned reference without loading Room messages. Callers own
// the Room directory lock and validate the member identity before choosing path.
func readDSHReference(path string, identity MemberIdentity) (dshReference, bool, error) {
	var ref dshReference
	err := readSnapshot(path, &ref, maxPrivateFileBytes)
	if err == nil {
		base := filepath.Base(dshStateDir(path))
		generation := strings.TrimPrefix(ref.Directory, base+"-")
		rotated := ref.Directory != generation && len(generation) == 32 && strings.Trim(generation, "0123456789abcdef") == ""
		if ref.Version != formatVersion || ref.MemberIdentity != identity || (ref.Directory != base && !rotated) || (ref.Pending && !rotated) {
			return ref, false, fmt.Errorf("%w: DSH directory reference", ErrInvalid)
		}
		if ref.Pending {
			if _, err := os.Lstat(filepath.Join(filepath.Dir(path), ref.Directory)); !errors.Is(err, os.ErrNotExist) {
				return ref, false, fmt.Errorf("%w: pending DSH generation already exists", ErrInvalid)
			}
		}
		return ref, !ref.Pending, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return ref, false, err
	}
	_, err = os.Lstat(dshStateDir(path))
	if errors.Is(err, os.ErrNotExist) {
		return ref, false, nil
	}
	if err != nil {
		return ref, false, err
	}
	return ref, false, fmt.Errorf("%w: DSH state directory has no adapter reference", ErrInvalid)
}

func (b dshBackend) OpenSession(ctx context.Context, opts SessionOptions) (Session, error) {
	if b.checkWorkspace != nil {
		if err := b.checkWorkspace(); err != nil {
			return nil, err
		}
	}
	if b.inboxMCP != nil {
		b.prepare = func(ctx context.Context, input UnreadChanged, turn uint64, images bool) (*dshMaterials, error) {
			return b.inboxMCP.prepareRoomMaterials(ctx, input, turn, images)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(errDSHPaused, err)
	}
	ref, exists, err := b.reference(opts)
	if err != nil {
		return nil, errors.Join(errDSHPaused, err)
	}
	if exists && !b.recover {
		return nil, dshFailure(dsh.ErrRecoveryUnsupported)
	}
	if b.recover && !exists {
		return nil, dshFailure(dsh.ErrRecoveryUnsupported)
	}
	if ref.Directory != "" {
		frozen, loadErr := b.restoreModelSnapshot(opts, ref)
		if loadErr == nil {
			b = frozen
		} else if b.model != nil || ref.ManagedModel || !errors.Is(loadErr, os.ErrNotExist) {
			return nil, dshFailure(loadErr)
		}
	} else {
		ref = dshReference{Version: formatVersion, MemberIdentity: opts.MemberIdentity, Directory: filepath.Base(dshStateDir(opts.StatePath))}
	}
	if b.model != nil && b.model.Legacy && (!b.recover || !exists) {
		return nil, dshFailure(dsh.ErrRecoveryUnsupported)
	}
	if !filepath.IsAbs(opts.CLIPath) {
		return nil, errors.Join(errDSHPaused, errors.New("absolute TeamX CLI path required"))
	}
	componentOpts, err := b.componentOptions(opts, ref)
	if err != nil {
		return nil, err
	}
	if err := b.checkModelRuntime(ctx, componentOpts); err != nil {
		return nil, dshFailure(err)
	}
	if !b.recover {
		if err := b.saveModelSnapshot(opts, ref); err != nil {
			return nil, dshFailure(err)
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
		return nil, errors.Join(errDSHPaused, err)
	}
	open := b.open
	if open == nil {
		open = func(ctx context.Context, options dsh.Options) (dshComponent, error) {
			start := dsh.Open
			if b.recover {
				start = dsh.Resume
			}
			session, err := start(ctx, options)
			if session == nil {
				return nil, err
			}
			return session, err
		}
	}
	var revoke func()
	var inbox *mcpInboxSession
	if b.access != nil {
		binding := executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: opts.RoomIdentity}, Caller: Caller{Author: Author{Kind: "agent", ID: opts.AgentID}, MembershipID: opts.MembershipID}}
		for _, entry := range opts.CLIEnv {
			if strings.HasPrefix(entry, "TEAMX_SERVICE_EPOCH=") {
				binding.ServiceEpoch = strings.TrimPrefix(entry, "TEAMX_SERVICE_EPOCH=")
			}
		}
		token, release, issueErr := b.access.issueExecution(binding)
		if issueErr != nil {
			return nil, issueErr
		}
		revoke = release
		componentOpts.ExecutionToken = token
		if b.inboxMCP != nil {
			token, releaseMCP, issueErr := b.access.issueMCPInbox(binding, b.inboxMCP, ref.Directory, slices.Contains(componentOpts.Model.InputModalities, "image"))
			if issueErr != nil {
				revoke()
				return nil, issueErr
			}
			revoke = sync.OnceFunc(func() { releaseMCP(); release() })
			componentOpts.MCPServers = []dsh.MCPHTTPServer{mcpHTTPConfiguration(componentOpts.ToolEnv["TEAMX_ENDPOINT"], token)}
			b.store.mcpOperations.mu.Lock()
			inbox = b.store.mcpOperations.inboxes[opts.MemberIdentity]
			b.store.mcpOperations.mu.Unlock()
		}
	} else if b.inboxMCP != nil {
		return nil, ErrIdentity
	}
	component, err := open(ctx, componentOpts)
	if component == nil {
		if revoke != nil {
			revoke()
		}
		if b.recover {
			err = errors.Join(errDSHDisabled, err)
		}
		return nil, dshFailure(errors.Join(err, errors.New("DSH did not open a session")))
	}
	// Legacy resumes already verify the recorded ACP identity in dsh.Resume;
	// imports have no probe receipt and must not be compared with an empty one.
	if b.model != nil && !b.model.Legacy && err == nil {
		if actual, ok := component.(interface{ RuntimeAgent() dsh.Artifact }); ok && actual.RuntimeAgent() != b.model.Runtime.Agent {
			err = errors.New("DSH ACP identity changed after model validation")
		}
	}
	// Deliver ownership even on an error, so Room's failed-open/discard path
	// closes any resource that was returned by the backend.
	return &dshSession{component: component, backend: b, opts: opts, revoke: revoke, inbox: inbox}, dshFailure(err)
}

// Materialize the effective member instructions for the live backend.
func (b dshBackend) componentOptions(opts SessionOptions, ref dshReference) (dsh.Options, error) {
	p := b.profile
	toolEnv := make(map[string]string, len(p.ToolEnv)+8)
	for key, value := range p.ToolEnv {
		toolEnv[key] = value
	}
	// CLIEnv includes inherited credentials and DSH settings. Pass only the
	// six Room identity bindings; dsh owns inheritance and reserved filtering.
	bindings := 0
	for _, entry := range opts.CLIEnv {
		key, value, _ := strings.Cut(entry, "=")
		if isRoomToolEnv(entry) && !privateRoomAccessEnv(key) {
			toolEnv[key] = value
		}
	}
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		if toolEnv[key] != "" {
			bindings++
		}
	}
	if bindings != 6 || toolEnv["TEAMX_ROOM_ID"] != opts.RoomID || toolEnv["TEAMX_ROOM_INSTANCE"] != opts.RoomInstance ||
		toolEnv["TEAMX_AGENT_ID"] != opts.AgentID || toolEnv["TEAMX_MEMBERSHIP_ID"] != opts.MembershipID {
		return dsh.Options{}, errors.Join(errDSHPaused, ErrIdentity)
	}
	toolEnv["TEAMX_CLI"] = opts.CLIPath
	if b.inputService != nil {
		toolEnv["TEAMX_INPUT_CONTRACT"] = inputstream.Contract
	}
	operator := humanOperator()
	if b.access != nil {
		operator = b.access.operator
	}
	guide := dshAgentGuide
	if b.inputService != nil {
		guide = unifiedAgentGuide()
	}
	instructions := strings.Join([]string{
		strings.TrimSpace(guide),
		fmt.Sprintf("## Session identity\n\nYou are member %q in Room %q (instance %q, membership %q).\nThe human operator is %q (mention %q).",
			opts.AgentID, opts.RoomID, opts.RoomInstance, opts.MembershipID, operator, "@"+operator),
		"## Member role\n\n" + strings.TrimSpace(p.Instructions),
	}, "\n\n")
	if b.inputService != nil {
		if b.inboxMCP != nil {
			instructions += "\nInput transport: HTTP MCP. Use discovered inbox_list/read; formal input CLI is disabled. Room history and business CLI remain available."
		} else {
			instructions += "\nInput transport: CLI. Use \"$TEAMX_CLI\" inbox list [--limit N | --cursor REF], or inbox read --stream REF [--limit N]. Execute one inbox read per shell tool call and wait for its result before continuing. Room history and business CLI remain available."
		}
	}
	if b.inboxMCP != nil && b.inputService == nil {
		instructions = strings.ReplaceAll(instructions, `"$TEAMX_CLI" inbox read`, "the discovered inbox_read MCP tool")
		instructions = strings.ReplaceAll(instructions, "Complete output is confirmed by the CLI", "Complete Room output is confirmed by the CLI; complete Inbox output is confirmed by the MCP service")
		instructions += "\n\nInbox entry mode: " + mcpInboxContract + ". Use the discovered inbox_read MCP tool with turn_number=current_turn. Omit cursor to retry the first page; pass next_cursor to continue until has_more=false. Consumption confirms complete server output, independently of business success. Missing images remain unverified. The consumption CLI is disabled for this turn. On an Inbox recovery wake, read first and check existing matters/action receipts before continuing; never repeat confirmed actions or resend unknown outcomes."
	}
	if ref.Directory != "" && ref.Directory != filepath.Base(dshStateDir(opts.StatePath)) {
		instructions += "\n\nSession context: explicitly rotated; private context has been reset."
	}

	component := dsh.Options{BinaryPath: p.BinaryPath, BinaryArgs: p.BinaryArgs, Artifact: p.Artifact, Workspace: p.Workspace,
		StateDir: filepath.Join(filepath.Dir(opts.StatePath), ref.Directory), Instructions: instructions, Model: p.Model.Clone(), ToolEnv: toolEnv, Limits: dsh.Limits{TextBytes: 64 << 10, TurnTimeout: b.turnTimeout}}
	component.InputBridge = b.inputService != nil
	if b.store.observe != nil {
		component.ObserveActivity = func() { b.store.changed("activity", opts.MemberIdentity) }
	}
	if b.inboxMCP != nil {
		component.MCPServers = []dsh.MCPHTTPServer{mcpHTTPConfiguration(toolEnv["TEAMX_ENDPOINT"], "")}
	}
	if b.model != nil {
		var err error
		component.Credential, err = b.credentials.get(b.model.CredentialRef)
		if err != nil {
			return dsh.Options{}, err
		}
		if b.model.Legacy {
			component.Credential.Ref = ""
		} // Original config fingerprint predates managed refs.
	}
	return component, nil
}

func dshFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, dsh.ErrUncertain) || errors.Is(err, dsh.ErrUnavailable) || errors.Is(err, dsh.ErrRecoveryUnsupported) {
		err = errors.Join(errDSHDisabled, err)
	}
	return errors.Join(errDSHPaused, err)
}

// Only a fully settled, expected cancellation may leave scheduling enabled.
// Joined cleanup/protocol/checkpoint errors must retain their normal handling.
func settledTurnCancellation(err error) bool {
	if !errors.Is(err, context.Canceled) {
		return false
	}
	var cancellationOnly func(error) bool
	cancellationOnly = func(err error) bool {
		switch err {
		case context.Canceled, dsh.ErrCancelled, errDSHPaused:
			return true
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				if !cancellationOnly(child) {
					return false
				}
			}
			return true
		case interface{ Unwrap() error }:
			return cancellationOnly(wrapped.Unwrap())
		}
		return false
	}
	return cancellationOnly(err)
}

// Common instructions are embedded into the Session configuration, never
// appended to individual turns. Changes participate in the recovery fingerprint.
//
//go:embed prompts/agent-guide.md
var dshAgentGuide string

func dshNotification(input UnreadChanged) string {
	// Identity and policy metadata are supplied in the policy JSON; read ranges
	// remain bound by the service. Fixed guidance belongs to Session Instructions.
	notification := fmt.Sprintf("Room unread=%d.\n", input.UnreadCount)
	if input.Inbox != nil {
		notification += "Inbox: a fixed batch is available.\n"
		if input.Inbox.Recovery {
			notification += "Inbox recovery: the previous offered range made no reading progress. Read it formally first; check existing matters and action receipts before business work. Do not repeat confirmed actions; verify unknown outcomes.\n"
		}
	}
	return notification
}

type dshSession struct {
	input     *inputSession
	revoke    func()
	inbox     *mcpInboxSession
	component dshComponent
	backend   dshBackend
	opts      SessionOptions
	// In-memory only: opening, resuming or rotating creates a new supply scope.
	policyToken      string
	recoverySupplied bool   // Only a successful RunPrompt clears first-supply work.
	timeZone         string // Last successfully supplied zone; independent of policy.
}

func dshTimeContext(settings settingsSnapshot, now time.Time, previous string) string {
	var text string
	if previous != settings.TimeZone {
		text = fmt.Sprintf("System time convention: The current system time zone is %s.\n", settings.TimeZone)
		if previous != "" {
			text = fmt.Sprintf("System time convention: The system time zone is now %s. This convention replaces the previous system time zone convention.\n", settings.TimeZone)
		}
		text += "In structured system timestamps, Z means UTC; interpret other explicit offsets as given. Convert times to the system time zone when describing them to users; do not copy UTC clock times as local times.\n" +
			"Interpret \"today/yesterday\" in the system time zone. Use full dates when the date is uncertain or a long turn lacks an up-to-date time reference.\n" +
			"Interpret times in third-party text and images using their explicit time zone labels; do not guess a missing time zone.\n"
	}
	return text + "Current time (turn-start reference): " + now.In(settings.location).Format(time.RFC3339) + "\n"
}

type dshDiagnostic struct {
	Result dsh.TurnResult `json:"result"`
	Error  string         `json:"error,omitempty"`
}

func (s *dshSession) Run(ctx context.Context, input UnreadChanged) (err error) {
	if s.backend.checkWorkspace != nil {
		if err := s.backend.checkWorkspace(); err != nil {
			return err
		}
	}
	defer func() {
		if err != nil {
			s.timeZone = "" // A failed turn is not evidence of retained context.
		}
	}()
	// Keep one settings snapshot even if a save occurs during preparation/run.
	settings, started := s.backend.settings.snapshot(), time.Now()
	if input.MemberIdentity != s.opts.MemberIdentity {
		return errors.Join(errDSHPaused, ErrIdentity)
	}
	policy, err := s.backend.store.readAgentPolicy(s.opts.MemberIdentity)
	if err != nil {
		return dshFailure(err)
	}
	var supplied any = policy.policyContext
	if s.policyToken != policy.PolicyToken {
		supplied = policy
	}
	body, err := json.Marshal(supplied)
	if err != nil {
		return dshFailure(err)
	}
	s.backend.store.activeDSH.Store(s.opts.MemberIdentity, s)
	s.backend.store.changed("activity", s.opts.MemberIdentity)
	defer func() {
		s.backend.store.activeDSH.CompareAndDelete(s.opts.MemberIdentity, s)
		s.backend.store.changed("activity", s.opts.MemberIdentity)
	}()
	notification := dshNotification(input)
	if s.backend.inputService != nil {
		svc := s.backend.inputService
		ref, _, e := s.backend.reference(s.opts)
		if e != nil {
			return dshFailure(e)
		}
		if s.inbox != nil {
			s.input, err = s.inbox.inputFor(ctx, policy.CurrentTurn, ref.Directory)
		} else {
			p, e := svc.inputStorage(s.opts.MemberIdentity)
			if e != nil {
				return dshFailure(e)
			}
			s.input, err = newInputSession(ctx, svc, p, ref.Directory, policy.CurrentTurn, s.component.SupportsImages(), func(ctx context.Context) error { return ctx.Err() })
		}
		if err != nil {
			return dshFailure(err)
		}
		c, e := s.input.catalog(ctx)
		if e != nil {
			return dshFailure(e)
		}
		if e := s.input.storage.run(ctx, "cache_catalog", func(context.Context) error { s.input.storage.keepInputCatalog(c); return nil }); e != nil {
			return dshFailure(e)
		}
		directory, exposure, e := c.Directory()
		if e != nil {
			return dshFailure(e)
		}
		directoryValue, e := s.input.chargeDiscovery(directory)
		if e != nil {
			return dshFailure(e)
		}
		if _, supplied := directoryValue.(inputstream.Directory); supplied {
			if e = s.input.storage.saveInputExposure(s.input.execution, exposure); e != nil {
				return dshFailure(e)
			}
		}
		b, e := json.Marshal(directoryValue)
		if e != nil {
			return dshFailure(e)
		}
		notification = "Input directory (JSON):\n" + string(b)
	}
	if svc := s.backend.inputService; svc != nil {
		work, e := svc.supplyCalendar(ctx, s.opts.MemberIdentity, policy.CurrentTurn)
		if e != nil {
			return dshFailure(e)
		}
		if len(work) > 0 {
			b, _ := json.Marshal(work)
			notification += "\nPending Calendar work (JSON):\n" + string(b) + "\nRead latest human intent, claim at most one, then calendar_finish the report. Never also send the report."
		}
	}
	prompt := []dsh.ContentBlock{{Type: "text", Text: "Member policy context (JSON):\n" + string(body) + "\n\n" + dshTimeContext(settings, started, s.timeZone) + "\n" + notification}}
	var materials *dshMaterials
	if s.backend.prepare != nil {
		materials, err = s.backend.prepare(ctx, input, policy.CurrentTurn, s.component.SupportsImages())
		if err != nil {
			return dshFailure(err)
		}
		prompt = materials.blocks(prompt[0].Text)
	}
	if !materialPromptFits(prompt) {
		return dshFailure(errors.New("DSH material prompt exceeds text/frame budget"))
	}
	if !s.recoverySupplied && s.backend.recoveryContext != nil {
		recovery, err := s.backend.recoveryContext(ctx, input, policy.CurrentTurn)
		if err != nil {
			return dshFailure(err)
		}
		prompt, err = recovery.appendTo(prompt)
		if err != nil {
			return dshFailure(err)
		}
	}
	if materials != nil {
		if err := materials.check(ctx); err != nil {
			return dshFailure(err)
		}
	}
	if s.inbox != nil {
		for i := range prompt {
			prompt[i].Text = strings.ReplaceAll(prompt[i].Text, "inbox read", "the discovered inbox_read MCP tool")
		}
	}
	if err := ctx.Err(); err != nil {
		return dshFailure(err)
	}
	if s.input != nil && s.backend.access != nil {
		s.input.executionContext = ctx
		token, revoke, e := s.backend.access.issueInputBinding(s.input)
		if e != nil {
			return dshFailure(e)
		}
		defer revoke()
		if private, ok := s.component.(interface{ AddRedactionSecret(string) }); ok {
			private.AddRedactionSecret(token)
		}
		ref, _, e := s.backend.reference(s.opts)
		if e != nil {
			return dshFailure(e)
		}
		path := filepath.Join(filepath.Dir(s.opts.StatePath), ref.Directory, "input-execution.json")
		if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
			return dshFailure(e)
		}
		if e = saveDSHDiagnostic(path, map[string]string{"token": token, "endpoint": s.backend.inputService.endpoint}); e != nil {
			return dshFailure(e)
		}
		defer os.Remove(path)
	}
	result, runErr := s.component.RunPrompt(ctx, prompt)
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
	private := dshDiagnostic{Result: result}
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
	return dshFailure(runErr)
}

func saveDSHDiagnostic(path string, value any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".dsh-turn-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(&boundedWriter{w: f, remaining: maxPrivateFileBytes}).Encode(value); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (s *dshSession) Close(ctx context.Context) error {
	if s.revoke != nil {
		s.revoke()
	}
	return s.component.Close(ctx)
}

func (s *dshSession) closeOwned() error {
	for {
		err := s.Close(context.Background())
		if !errors.Is(err, dsh.ErrCleanupPending) {
			return err
		}
		// Each component Close waits for its bounded cleanup budget. Retain
		// the actor and directory ownership while process observation retries.
	}
}

func profileNames(profiles map[string]dshProfile) []string {
	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
