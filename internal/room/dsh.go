package room

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/inputstream"
)

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

// Legacy audit fixtures also expose the text convenience method. The public
// factory contract uses RunPrompt, including for text-only execution.
type dshComponent interface {
	agent.Component
	Run(context.Context, string) (dsh.TurnResult, error)
}

type dshBackend struct {
	factory        agent.Factory
	checkWorkspace func() error
	isDirect       bool // Immutable type of the owning TeamX conversation.
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
	// A narrow seam for offline adapter tests; production uses the selected factory.
	open func(context.Context, dsh.Options) (dshComponent, error)
}

func (b dshBackend) mcpService() *service {
	if b.inputService != nil {
		return b.inputService
	}
	return b.inboxMCP
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

func (b dshBackend) reference(opts SessionOptions) (sessionReference, bool, error) {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	path, err := b.store.dshPath(opts.MemberIdentity, "session.json", false)
	if err != nil {
		return sessionReference{}, false, err
	}
	if path != opts.StatePath {
		return sessionReference{}, false, ErrIdentity
	}
	return b.selectedFactory().Reference(sessionCheckpoints{path: path}, opts.MemberIdentity, path)
}

// Read the adapter-owned reference without loading Room messages. Callers own
// the Room directory lock and validate the member identity before choosing path.
func readSessionReference(path string, identity MemberIdentity) (sessionReference, bool, error) {
	return defaultBackendFactory.Reference(sessionCheckpoints{path: path}, identity, path)
}

func (b dshBackend) OpenSession(ctx context.Context, opts SessionOptions) (Session, error) {
	s := &dshSession{backend: b, opts: opts}
	host := &sessionHost{dshSession: s}
	runtime, err := b.selectedFactory().OpenSession(ctx, host, b.recover)
	if runtime == nil {
		if !host.prepared {
			return nil, err
		}
		if b.recover {
			err = errors.Join(errBackendDisabled, err)
		}
		return nil, dshFailure(errors.Join(err, errors.New("DSH did not open a session")))
	}

	s.runtime = runtime
	return s, dshFailure(err)
}

// Materialize the effective member instructions for the live backend.
func (b dshBackend) componentOptions(opts SessionOptions, ref sessionReference) (dsh.Options, error) {
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
		return dsh.Options{}, errors.Join(errBackendPaused, ErrIdentity)
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
	conversation := "group (shared with Room members)"
	if b.isDirect {
		conversation = "direct (one-to-one with the human operator)"
	}
	instructions := strings.Join([]string{
		strings.TrimSpace(guide),
		strings.TrimSpace(dshBackendGuide),
		fmt.Sprintf("## Session identity\n\nYou are member %q in Room %q (instance %q, membership %q).\nCurrent conversation: %s.\nThe human operator is %q; their mention syntax is %q.",
			opts.AgentID, opts.RoomID, opts.RoomInstance, opts.MembershipID, conversation, operator, "@"+operator),
		"## Member role\n\n" + strings.TrimSpace(p.Instructions),
	}, "\n\n")
	if b.inputService != nil {
		if b.inboxMCP != nil {
			instructions += "\nInput transport: HTTP MCP. Use discovered inbox_list/read; formal input CLI is disabled. Use discovered history_list/history_read MCP for retained context. Business CLI remains available."
		} else {
			instructions += "\nInput transport: CLI. Use \"$TEAMX_CLI\" inbox list [--limit N | --cursor REF], or inbox read --stream REF [--limit N]. Execute one inbox read per shell tool call and wait for its result before continuing. Use discovered history_list/history_read MCP for retained context. Business CLI remains available."
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
	if b.mcpService() != nil {
		component.MCPServers = append([]dsh.MCPHTTPServer{mcpHTTPConfiguration(toolEnv["TEAMX_ENDPOINT"], "")}, component.MCPServers...)
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
	capability, err := loadComputerCapability(opts, ref.Directory)
	if err != nil {
		return component, err
	}
	component.CapabilityDigest = capability.digest()
	if capability.Enabled {
		component.Instructions += computerAgentGuide
	}
	return component, nil
}

func dshFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, dsh.ErrUncertain) || errors.Is(err, dsh.ErrUnavailable) || errors.Is(err, dsh.ErrRecoveryUnsupported) {
		err = errors.Join(errBackendDisabled, err)
	}
	return errors.Join(errBackendPaused, err)
}

// Only a fully settled, expected cancellation may leave scheduling enabled.
// Joined cleanup/protocol/checkpoint errors must retain their normal handling.
func (b dshBackend) settledCancellation(err error) bool {
	return cancellationOnly(err, func(err error) bool {
		return err == context.Canceled || err == dsh.ErrCancelled || err == errBackendPaused
	})
}

// Common instructions are embedded into the Session configuration, never
// appended to individual turns. Changes participate in the recovery fingerprint.
//
//go:embed prompts/agent-guide.md
var dshAgentGuide string

//go:embed prompts/dsh-guide.md
var dshBackendGuide string

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
	computerCapability computerCapability
	computerGeneration string
	computerTurn       uint64
	runtime            *agent.Session
	input              *inputSession
	revoke             func()
	inbox              *mcpInboxSession
	component          dshComponent
	backend            dshBackend
	opts               SessionOptions
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

type backendDiagnostic struct {
	Result dsh.TurnResult `json:"result"`
	Error  string         `json:"error,omitempty"`
}

func (s *dshSession) Run(ctx context.Context, input UnreadChanged) error {
	if s.runtime != nil {
		return s.runtime.Run(ctx, input)
	}
	// Isolated fixtures may supply their component directly. Both paths use the
	// same preparation, confirmation, diagnostic and release operations.
	turn, err := s.prepareTurn(ctx, input, turnCapabilities(s.component))
	if err != nil {
		return err
	}
	defer turn.Close()
	result, err := s.component.RunPrompt(ctx, turn.Prompt)
	return turn.Finish(result, err)
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
	var local error
	if s.runtime != nil {
		local = dshCloseResult(s.runtime.Close(ctx))
	} else {
		if s.revoke != nil {
			s.revoke()
		}
		local = dshCloseResult(s.component.Close(ctx))
	}
	remote := s.backend.computerMembers().settleScope(ctx, s.opts.MemberIdentity, computer.Scope{Session: s.computerGeneration}, false)
	if remote != nil {
		return &pendingBackendClose{errors.Join(local, remote)}
	}
	return local
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
