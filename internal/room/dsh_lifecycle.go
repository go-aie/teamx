package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// This adapter selects the only persisted backend currently accepted by TeamX.
// It interprets existing files without introducing a registry or changing schema.
func storedSessionBackend(store *Store) storedBackend {
	return newSessionBackend(store)
}

func sessionGenerationForOperation(operation string) string { return "session.json.dsh-" + operation }

func validSessionGeneration(g string) bool {
	const base = "session.json.dsh"
	return g == base || len(g) == len(base)+33 && g[:len(base)+1] == base+"-" && validRestartID(g[len(base)+1:])
}

func (b dshBackend) sessionGeneration(opts SessionOptions) (string, error) {
	ref, _, err := b.reference(opts)
	if err != nil {
		return "", err
	}
	if ref.Directory != "" {
		return ref.Directory, nil
	}
	return initialSessionGeneration(opts), nil
}
func (b dshBackend) checkRotationClosed(opts SessionOptions, from string, ownedSettled bool) error {
	if err := checkComputerSettled(opts, from); err != nil {
		return err
	}
	_, exists, err := b.reference(opts)
	if err != nil {
		return err
	}
	current, err := b.sessionGeneration(opts)
	if err != nil {
		return err
	}
	if current != from {
		return ErrConflict
	}
	if exists && !ownedSettled {
		if err := b.selectedFactory().CheckReset(filepath.Join(filepath.Dir(opts.StatePath), from)); err != nil {
			return fmt.Errorf("old execution has no verified settlement: %w", err)
		}
	}
	return nil
}
func (b dshBackend) switchSession(opts SessionOptions, from, to string) (Backend, error) {
	if !validSessionGeneration(from) || !validSessionGeneration(to) || from == to {
		return nil, ErrRequest
	}
	if b.model != nil && (b.model.Legacy || b.models != nil) {
		if b.models == nil {
			return nil, ErrInvalid
		}
		var err error
		b, err = b.latestModelSelection(opts)
		if err != nil {
			return nil, err
		}
	}
	ref, _, err := b.reference(opts)
	if err != nil {
		return nil, err
	}
	current, err := b.sessionGeneration(opts)
	if err != nil {
		return nil, err
	}
	if current == to {
		if !ref.Pending {
			return nil, fmt.Errorf("%w: target already started", ErrConflict)
		}
		b.recover = false
		return b, nil
	}
	if current != from {
		return nil, ErrConflict
	}
	if b.model != nil {
		snapshot := b.model.clone()
		snapshot.Generation = to
		b.model = &snapshot
		if err := b.saveModelSnapshot(opts, sessionReference{Directory: to}); err != nil {
			return nil, err
		}
	}
	ref = sessionReference{Version: formatVersion, MemberIdentity: opts.MemberIdentity, Directory: to, Pending: true, ManagedModel: b.model != nil}
	b.store.mu.Lock()
	path, err := b.store.dshPath(opts.MemberIdentity, "session.json", true)
	if err == nil {
		err = b.store.save(path, ref, maxPrivateFileBytes)
	}
	b.store.mu.Unlock()
	if b.model != nil {
		b.store.changed("models", opts.MemberIdentity)
	}
	if err != nil {
		return nil, err
	}
	b.recover = false
	return b, nil
}

func (b dshBackend) latestModelSelection(opts SessionOptions) (dshBackend, error) {
	profile := b.profile
	if b.model != nil {
		profile = b.model.Profile
	}
	var saved managedRoomConfig
	if err := readSnapshot(filepath.Join(b.store.dir, "room-config.json"), &saved, maxPrivateFileBytes); err != nil {
		return b, err
	}
	for _, member := range saved.Members {
		if saved.RoomIdentity == opts.RoomIdentity && member.AgentID == opts.AgentID && member.MembershipID == opts.MembershipID && member.ModelSelection != nil {
			target, err := b.models.target(opts.MemberIdentity, *member.ModelSelection, member.ModelSelectionVersion, profile)
			if err != nil {
				return b, err
			}
			b.model = &target
			b.profile.Model = target.Profile.Model.Clone()
			return b, nil
		}
	}
	return b, ErrIdentity
}

func (b dshBackend) recoverableFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, dsh.ErrCleanupPending) || errors.Is(err, dsh.ErrLocked) || errors.Is(err, dsh.ErrAuthentication) || errors.Is(err, dsh.ErrModelNotFound) || errors.Is(err, dsh.ErrRateLimited) {
		return false
	}
	var status *dsh.HTTPStatusError
	if errors.As(err, &status) {
		return status.StatusCode >= 500
	}
	// Incompatibility authorizes a recovery candidate, never replacement by
	// itself. The adapter reference, settlement and business receipts must all
	// pass the existing rotation gates, including after a failed lazy resume.
	return errors.Is(err, dsh.ErrRecoveryUnsupported) || errors.Is(err, dsh.ErrUnavailable) || errors.Is(err, dsh.ErrUncertain) || errors.Is(err, dsh.ErrModelUnavailable)
}

// Legacy snapshots retain only safe diagnostics, so recognize only known
// backend failures. An arbitrary saved error never authorizes replacement.
func recoverableDSHHistory(agent AgentSnapshot) bool {
	if agent.Turn == nil || agent.Turn.Status == "completed" || agent.Turn.Status == "interrupted" {
		return true
	}
	err := agent.Turn.Error
	if strings.Contains(err, "HTTP 4") {
		return false
	}
	for _, permanent := range []error{dsh.ErrAuthentication, dsh.ErrModelNotFound, dsh.ErrRateLimited} {
		if strings.Contains(err, permanent.Error()) {
			return false
		}
	}
	return strings.Contains(err, "cannot inspect owned DSH processes") || strings.Contains(err, dsh.ErrUnavailable.Error()) ||
		strings.Contains(err, dsh.ErrModelUnavailable.Error()) || strings.Contains(err, dsh.ErrRecoveryUnsupported.Error())
}

// Failed Open/Resume may return no owned Session. Only independently verified
// durable settlement permits entering rotation in that case. Never infer that
// no process exists merely because Open returned nil.
func (b dshBackend) settledOpenFailure(opts SessionOptions, err error) bool {
	if !b.recoverableFailure(err) {
		return false
	}
	ref, exists, checkErr := b.reference(opts)
	return checkErr == nil && exists && b.checkRotationClosed(opts, ref.Directory, false) == nil
}
func (s *service) memberBackend(runner RunnerConfig) dshBackend {
	backend := newSessionBackend(s.store)
	backend.executionResources = s.executionResources
	backend.execution = s.execution
	if s.humanRead != nil {
		backend.ownerUserID = s.humanRead.operator
	}
	backend.checkWorkspace = func() error {
		if s.checkWorkspace != nil {
			return s.checkWorkspace(runner.Profile)
		}
		return nil
	}
	backend.isDirect, backend.access, backend.settings = s.isDirect, s.access, s.settings
	backend.profile, backend.turnTimeout = s.dshProfiles[runner.Profile], s.dshTurnTimeout
	backend.environment = s.environment
	backend.prepare, backend.recoveryContext = s.prepareRoomMaterials, s.prepareDSHRecoveryContext

	if s.unifiedInputs {
		backend.prepare = nil
		backend.inputService = s
	}
	if s.inboxMode != "cli" && s.access != nil {
		backend.inboxMCP = s
	}
	if s.models != nil {
		backend.models = s.models
		backend.credentials = s.models.Credentials
		if target, ok := s.modelTargets[runner.Profile]; ok {
			backend.model = &target
		}
	}
	return backend
}

func (b dshBackend) switchPreparedSession(opts SessionOptions, from, to string, target *modelSessionSnapshot) (Backend, error) {
	if target != nil {
		snapshot := target.clone()
		b.model, b.profile = &snapshot, snapshot.Profile
	}
	return b.switchSession(opts, from, to)
}

func (b dshBackend) checkResume(opts SessionOptions, generation string) error {
	return errors.Join(checkComputerSettled(opts, generation), b.selectedFactory().CheckResume(filepath.Join(filepath.Dir(opts.StatePath), generation)))
}
func (b dshBackend) checkReset(opts SessionOptions, generation string) error {
	return errors.Join(checkComputerSettled(opts, generation), b.selectedFactory().CheckReset(filepath.Join(filepath.Dir(opts.StatePath), generation)))
}

// Preserve both errors.Is diagnostics and the component's independent cleanup fact.
func dshCloseResult(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, dsh.ErrCleanupPending) {
		return &pendingBackendClose{err}
	}
	if dsh.CleanupSettled(err) {
		return &settledBackendClose{err}
	}
	return err
}

var _ storedBackend = dshBackend{}
var _ modelChangingBackend = dshBackend{}
var _ backendRecoveryPolicy = dshBackend{}

func (s *service) executionBackend(runner RunnerConfig, resume bool) Backend {
	backend := s.memberBackend(runner)
	backend.recover = resume
	return backend
}

func (s *service) inspectBackendRecovery(opts SessionOptions, runner RunnerConfig, agent AgentSnapshot, rotationReady bool) (backendRecovery, error) {
	b := s.memberBackend(runner)
	ref, exists, err := b.reference(opts)
	result := backendRecovery{reference: ref}
	if err != nil {
		return result, err
	}
	if _, ok := s.dshProfiles[runner.Profile]; !ok {
		result.blocked = "DSH profile unavailable: " + runner.Profile
	} else if exists {
		// Local recovery eligibility and the external execution gate are
		// independent facts. A remote pending scope must not permanently turn
		// a resumable local Session into an unsupported backend. The member's
		// capability gate keeps it idle until remote reconciliation completes.
		path := filepath.Join(filepath.Dir(opts.StatePath), ref.Directory)
		localReset := func() bool { return b.selectedFactory().CheckReset(path) == nil }
		if err := b.selectedFactory().CheckResume(path); err != nil {
			result.blocked = "DSH recovery blocked: " + err.Error()
			if rotationReady && errors.Is(err, dsh.ErrRecoveryUnsupported) && recoverableDSHHistory(agent) {
				result.recovery = localReset()
			}
		} else {
			result.resume = true
			// For an already resumable Session the old failed-turn path does not
			// require a completed rotation; the member's rotation gate still applies.
			result.recovery = agent.Turn != nil && agent.Turn.Status == "failed" && recoverableDSHHistory(agent) && localReset()
			result.notice = "DSH original session will resume lazily using current tool identity"
			opts.CLIPath, opts.CLIEnv = s.cliPath, s.toolEnv(opts.MemberIdentity)
			if config, configErr := b.componentOptions(opts, ref); configErr == nil && config.BinaryPath != "" {
				if configErr = b.selectedFactory().CheckResumeConfig(launchOptions(config)); configErr != nil {
					result.blocked = "DSH configuration requires settled Session rotation: " + configErr.Error()
					result.resume = false
					result.recovery = rotationReady && recoverableDSHHistory(agent) && localReset()
				}
			}
		}
	} else if !ref.Pending && agent.Turn != nil {
		result.blocked = "DSH recovery blocked: backend reference missing for recorded turn"
	}
	return result, nil
}

func (b dshBackend) activity(id MemberIdentity, active bool) (backendActivity, error) {
	var result backendActivity
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	path, err := b.store.dshPath(id, "dsh-turn.json", false)
	if err != nil {
		return backendActivity{}, err
	}
	if live, ok := b.store.activeSessions.Load(id); ok {
		result.current = live.(activeSessionOutput).currentActivity()
		return result, nil
	}
	if active {
		// Opening a backend can take time before a component is available.
		// Do not mislabel the prior completed turn as the current activity.
		result.current = &backendDiagnostic{}
		return result, nil
	}
	var output backendDiagnostic
	if err := readSnapshot(path, &output, maxPrivateFileBytes); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			result.error = "私有输出暂不可读：" + err.Error()
		}
		return result, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		result.error = "私有输出时间不可读：" + err.Error()
		return result, nil
	}
	updated := info.ModTime().UTC()
	result.last, result.updated = &output, &updated
	return result, nil
}

func (s *dshSession) currentActivity() *backendDiagnostic {
	output := &backendDiagnostic{}
	if s.runtime != nil {
		if result, active := s.runtime.Activity(); active {
			output.Result = result
		}
		return output
	}
	if reader, ok := s.component.(interface{ Activity() (dsh.TurnResult, bool) }); ok {
		if result, active := reader.Activity(); active {
			output.Result = result
		}
	}
	return output
}

func checkSessionGenerationSettled(path string) error {
	return errors.Join(checkComputerFence(filepath.Join(filepath.Dir(path), "computer-execution.json"), filepath.Base(path)), defaultBackendFactory.CheckReset(path))
}
func isSessionGenerationEntry(name string) bool {
	return name == "session.json.dsh" || strings.HasPrefix(name, "session.json.dsh-")
}

// Historical snapshots store safe strings rather than live errors. Only the
// adapter recognizes its known provider messages; arbitrary text is not shown.
func backendModelNotice(text string) *ExecutionNotice {
	switch {
	case strings.Contains(text, dsh.ErrAuthentication.Error()):
		return &ExecutionNotice{Code: "model_authentication", Message: "模型连接验证失败，请在设置中检查模型凭据。"}
	case strings.Contains(text, dsh.ErrRateLimited.Error()):
		return &ExecutionNotice{Code: "model_rate_limited", Message: "模型服务请求受限，请稍后重试。"}
	case strings.Contains(text, dsh.ErrModelNotFound.Error()):
		return &ExecutionNotice{Code: "model_not_found", Message: "当前模型不可用，请在设置中检查或更换模型。"}
	}
	return nil
}

func initialSessionGeneration(opts SessionOptions) string {
	return filepath.Base(dshStateDir(opts.StatePath))
}
