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

	"github.com/go-aie/teamx/internal/agent/dsh"
)

var errDSHPaused = errors.New("DSH member paused; inspect backend failure before continuing")
var errDSHDisabled = errors.New("DSH backend disabled; replacement execution is forbidden")

// Profiles are supplied by the operator, never accepted from member HTTP
// requests. Single-room membership persists a profile name; managed creation
// freezes the team runtime and roles in its Room configuration.
type dshProfile struct {
	BinaryPath   string            `json:"binary_path"`
	BinaryArgs   []string          `json:"binary_args,omitempty"`
	Artifact     dsh.Artifact      `json:"artifact,omitzero"`
	Workspace    string            `json:"workspace"`
	Instructions string            `json:"instructions"`
	Model        dsh.ModelConfig   `json:"model"`
	ToolEnv      map[string]string `json:"tool_env,omitempty"`
}

func (p dshProfile) checkCredentials() error {
	if os.Getenv(p.Model.APIKeyEnv) == "" {
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
	store       *Store
	prepare     func(context.Context, UnreadChanged, uint64, bool) (*dshMaterials, error)
	profile     dshProfile
	turnTimeout time.Duration // Service policy; not part of the frozen Room runtime.
	recover     bool          // Only set by startup after checking normal shutdown evidence.
	// A narrow seam for offline adapter tests; production always uses dsh.Open.
	open func(context.Context, dsh.Options) (dshComponent, error)
}

// Reject missing credentials before admitting a turn or publishing a Session
// reference. A validation failure must not become ambiguous execution state.
func (b dshBackend) CheckRun() error {
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
	Directory string `json:"directory"`
	Pending   bool   `json:"pending,omitempty"` // Authorized fresh generation, not yet opened.
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
	if ref.Directory == "" {
		ref = dshReference{Version: formatVersion, MemberIdentity: opts.MemberIdentity, Directory: filepath.Base(dshStateDir(opts.StatePath))}
	}
	if !filepath.IsAbs(opts.CLIPath) {
		return nil, errors.Join(errDSHPaused, errors.New("absolute TeamX CLI path required"))
	}
	componentOpts, err := b.componentOptions(opts, ref)
	if err != nil {
		return nil, err
	}

	// Publish the adapter reference before new execution. This also guards a crash
	// before the component can publish opening.json or backend.json.
	path := opts.StatePath
	if !b.recover {
		b.store.mu.Lock()
		path, err = b.store.dshPath(opts.MemberIdentity, "session.json", true)
		if err == nil {
			ref.Pending = false // Consume fresh-generation authority before launch.
			err = b.store.save(path, ref, maxPrivateFileBytes)
		}
		b.store.mu.Unlock()
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
	component, err := open(ctx, componentOpts)
	if component == nil {
		if b.recover {
			err = errors.Join(errDSHDisabled, err)
		}
		return nil, dshFailure(errors.Join(err, errors.New("DSH did not open a session")))
	}
	// Deliver ownership even on an error, so Room's failed-open/discard path
	// closes any resource that was returned by the backend.
	return &dshSession{component: component, backend: b, opts: opts}, dshFailure(err)
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
		if isRoomToolEnv(entry) {
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
	operator := humanOperator()
	instructions := strings.Join([]string{
		strings.TrimSpace(dshAgentGuide),
		fmt.Sprintf("## Session identity\n\nYou are member %q in Room %q (instance %q, membership %q).\nThe human operator is %q (mention %q).",
			opts.AgentID, opts.RoomID, opts.RoomInstance, opts.MembershipID, operator, "@"+operator),
		"## Member role\n\n" + strings.TrimSpace(p.Instructions),
	}, "\n\n")
	if ref.Directory != "" && ref.Directory != filepath.Base(dshStateDir(opts.StatePath)) {
		instructions += "\n\nSession context: explicitly rotated; private context has been reset."
	}

	return dsh.Options{BinaryPath: p.BinaryPath, BinaryArgs: p.BinaryArgs, Artifact: p.Artifact, Workspace: p.Workspace,
		StateDir: filepath.Join(filepath.Dir(opts.StatePath), ref.Directory), Instructions: instructions, Model: p.Model.Clone(), ToolEnv: toolEnv, Limits: dsh.Limits{TextBytes: 64 << 10, TurnTimeout: b.turnTimeout}}, nil
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
	}
	return notification
}

type dshSession struct {
	component dshComponent
	backend   dshBackend
	opts      SessionOptions
	// In-memory only: opening, resuming or rotating creates a new supply scope.
	policyToken string
}

type dshDiagnostic struct {
	Result dsh.TurnResult `json:"result"`
	Error  string         `json:"error,omitempty"`
}

func (s *dshSession) Run(ctx context.Context, input UnreadChanged) error {
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
	defer s.backend.store.activeDSH.CompareAndDelete(s.opts.MemberIdentity, s)
	prompt := []dsh.ContentBlock{{Type: "text", Text: "Member policy context (JSON):\n" + string(body) + "\n\n" + dshNotification(input)}}
	if s.backend.prepare != nil {
		materials, err := s.backend.prepare(ctx, input, policy.CurrentTurn, s.component.SupportsImages())
		if err != nil {
			return dshFailure(err)
		}
		prompt = materials.blocks(prompt[0].Text)
		if !materialPromptFits(prompt) {
			return dshFailure(errors.New("DSH material prompt exceeds text/frame budget"))
		}
		if err := materials.check(ctx); err != nil {
			return dshFailure(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return dshFailure(err)
	}
	result, runErr := s.component.RunPrompt(ctx, prompt)
	if runErr == nil {
		s.policyToken = policy.PolicyToken
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

func (s *dshSession) Close(ctx context.Context) error { return s.component.Close(ctx) }

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
