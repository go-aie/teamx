package room

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

// Catalog validation and config publication are shared by first use and online management.
type modelService struct {
	observe               func()
	removeLegacyModel     bool
	launcherConfigMissing bool
	legacyKeyEnv          string // Only fresh prefilled homes; removed in the same catalog commit.
	document              *configDocument
	credentials           *modelCredentials
	launcher              dsh.Options
	current               modelCatalog // Protected by document.mu, shared with time-zone writes.
	probeSlot             chan struct{}
	probe                 func(context.Context, dsh.Options) (dsh.ProbeResult, error)
}

func newModelService(config startupConfig, settings *systemSettings, home string, protected ...[]string) (*modelService, error) {
	if config.Runtime != nil && config.Runtime.Model.ModelID != "" {
		return nil, errors.New("existing runtime models require migration; model writes are not enabled")
	}
	if settings == nil || settings.path != config.path {
		return nil, ErrIdentity
	}
	if config.Server.DataDir != nil && (config.Models == nil || len(config.Models.Entries) == 0) {
		err := requireFreshModelHome(config)
		if len(protected) > 0 {
			err = requireFreshModelHomeKeeping(config, protected[0])
		}
		if err != nil {
			return nil, err
		}
	}
	if config.Models != nil && len(config.Models.Entries) > 0 {
		if _, err := loadModelExecution(config); err != nil {
			return nil, err
		}
	}
	credentials, err := newModelCredentials(home)
	if err != nil {
		return nil, err
	}
	s := &modelService{document: settings.configDocument, credentials: credentials, current: modelCatalog{SchemaVersion: 1}, probeSlot: make(chan struct{}, 1), probe: dsh.Probe}
	s.launcher.Environment = startupEnvironment(config.Env)
	s.launcher.Execution, s.launcher.ExecutionBinding = config.execution, config.executionBinding
	if config.Models != nil {
		s.current = config.Models.clone()
	}
	if err := s.current.validate(); err != nil {
		return nil, err
	}
	if config.Runtime != nil {
		s.launcher.Artifact = config.Runtime.DSH.Artifact
		s.launcher.BinaryPath = config.Runtime.DSH.BinaryPath
		s.launcher.BinaryArgs = append([]string(nil), config.Runtime.DSH.BinaryArgs...)
	}
	return s, nil
}
func (s *modelService) snapshot() modelCatalog {
	s.document.mu.Lock()
	defer s.document.mu.Unlock()
	return s.current.clone()
}

type modelSave struct {
	beforeCommit    func(modelCatalog, modelCatalog) error
	afterCommit     func(modelCatalog)
	Entry           modelEntry
	APIKey          string `json:"-"`
	OperationID     string
	ExpectedVersion uint64
	MakeDefault     bool
}

// A bounded immutable candidate is validated outside the config mutex. The
// commit rechecks the expected version and original file before publication.
func (s *modelService) save(ctx context.Context, input modelSave) (modelCatalog, error) {
	select {
	case s.probeSlot <- struct{}{}:
		defer func() { <-s.probeSlot }()
	default:
		return s.snapshot(), ErrBusy
	}
	current := s.snapshot()
	if current.Version != input.ExpectedVersion {
		return current, ErrConflict
	}
	entry := input.Entry.clone()
	entry.Unverified = false
	if entry.Protocol == "" {
		entry.Protocol = "openai-completions"
	}
	var old *modelEntry
	for i := range current.Entries {
		if current.Entries[i].ID == entry.ID {
			value := current.Entries[i].clone()
			old = &value
			break
		}
	}
	if old != nil {
		entry.CredentialRef = old.CredentialRef
		entry.PendingDeletion = old.PendingDeletion
		if old.PendingDeletion {
			return current, ErrBusy
		}
	} else {
		entry.CredentialRef = ""
		entry.PendingDeletion = false
	}
	newRef := ""
	if input.APIKey != "" {
		same := false
		// Identical key bytes can share one immutable binding across entries.
		// This preserves execution equality when only the selected entry changes.
		for _, candidate := range current.Entries {
			existing, err := s.credentials.get(candidate.CredentialRef)
			if err == nil && existing.Value == input.APIKey {
				entry.CredentialRef = candidate.CredentialRef
				same = true
				break
			}
		}
		if !same {
			var err error
			newRef, err = s.credentials.put(input.APIKey)
			if err != nil {
				return current, err
			}
			entry.CredentialRef = newRef
		}
	}
	retain := false
	defer func() {
		if newRef != "" && !retain {
			_ = os.Remove(filepath.Join(s.credentials.dir, newRef))
			_ = os.Remove(filepath.Join(filepath.Dir(s.credentials.dir), "model-validations", entry.executionDigest()+".json"))
		}
	}()
	if entry.CredentialRef == "" {
		return current, invalidField("api_key", "enter an API key")
	}
	entry.Revision = 1
	if old != nil {
		entry.Revision = old.Revision
		if !entry.sameExecution(*old) {
			if entry.Revision == ^uint64(0) {
				return current, ErrCapacity
			}
			entry.Revision++
		}
	}
	next := current.clone()
	found := false
	for i := range next.Entries {
		if next.Entries[i].ID == entry.ID {
			next.Entries[i] = entry
			found = true
		}
	}
	if !found {
		next.Entries = append(next.Entries, entry)
	}
	if input.MakeDefault || next.Default == "" {
		next.Default = entry.ID
	}
	if reflect.DeepEqual(next, current) && input.beforeCommit == nil {
		var receipt modelValidation
		path := filepath.Join(filepath.Dir(s.credentials.dir), "model-validations", entry.executionDigest()+".json")
		if readSnapshot(path, &receipt, maxPrivateFileBytes) == nil && modelValidationMatches(entry, receipt) {
			return current, nil
		}
	}
	if next.Version == ^uint64(0) {
		return current, ErrCapacity
	}
	next.Version++
	if err := next.validate(); err != nil {
		return current, err
	}
	executionChanged := old == nil || !entry.sameExecution(*old)
	if !executionChanged {
		var receipt modelValidation
		path := filepath.Join(filepath.Dir(s.credentials.dir), "model-validations", entry.executionDigest()+".json")
		executionChanged = readSnapshot(path, &receipt, maxPrivateFileBytes) != nil || !modelValidationMatches(entry, receipt)
	}
	defaultExecutionChanged := false
	if next.Default != current.Default {
		before, err := current.resolve(modelSelection{Mode: "default"})
		after, afterErr := next.resolve(modelSelection{Mode: "default"})
		defaultExecutionChanged = err != nil || afterErr != nil || !before.sameExecution(after)
	}
	if executionChanged || defaultExecutionChanged {
		key, err := s.credentials.get(entry.CredentialRef)
		if err != nil {
			return current, err
		}
		opts := s.launcher
		opts.Model = entry.materialize()
		opts.Credential = key
		started := time.Now()
		receipt, err := s.probeWhenAvailable(ctx, opts)
		if err == nil && (!receipt.Text || !receipt.ToolRoundTrip || !receipt.Cleanup || receipt.Identity.Version == "" || receipt.Identity.Agent == (dsh.Artifact{})) {
			err = errors.New("model validation did not complete text, tool and cleanup checks")
		}
		stage, code, level := "complete", "ok", slog.LevelInfo
		if err != nil {
			stage, code, level = "validation", "failed", slog.LevelWarn
			var probe *dsh.ProbeError
			if errors.As(err, &probe) {
				stage, code = probe.Stage, probe.Code
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = "timeout"
			} else if errors.Is(err, context.Canceled) {
				code = "cancelled"
			}
		}
		// Only authored categories and correlation IDs enter the service log.
		// Upstream errors may contain credentials, prompts or private paths.
		slog.Log(ctx, level, "model validation finished", "space_id", opts.ExecutionBinding.SpaceID, "operation_id", input.OperationID,
			"stage", stage, "code", code, "duration_ms", time.Since(started).Milliseconds())
		if err != nil {
			return current, err
		}
		// A private receipt is for diagnosis; it is never authority to skip a probe
		// in a later operation. Its identity includes the exact credential version.
		record := modelValidation{EntryID: entry.ID, Revision: entry.Revision, ExecutionDigest: entry.executionDigest(), CredentialRef: entry.CredentialRef, Result: receipt}
		if err := saveModelValidation(s.credentials.dir, record); err != nil {
			return current, err
		}
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	if input.beforeCommit != nil {
		if err := input.beforeCommit(current, next); err != nil {
			return current, err
		}
	}
	s.document.mu.Lock()
	defer s.document.mu.Unlock()
	err := s.document.commitLocked(func(source []byte) ([]byte, func(), error) {
		if s.current.Version != input.ExpectedVersion {
			return nil, nil, ErrConflict
		}
		var disk startupConfig
		if err := toml.Unmarshal(source, &disk); err != nil {
			return nil, nil, ErrConflict
		}
		var diskVersion uint64
		if disk.Models != nil {
			diskVersion = disk.Models.Version
		}
		if diskVersion != s.current.Version {
			return nil, nil, ErrConflict
		}
		if s.removeLegacyModel || s.legacyKeyEnv != "" {
			var err error
			paths := [][]string{{"runtime", "model"}}
			if s.legacyKeyEnv != "" {
				paths = append(paths, []string{"env", s.legacyKeyEnv})
			}
			source, err = removeConfigSubtrees(source, paths...)
			if err != nil {
				return nil, nil, err
			}
		}
		if s.launcherConfigMissing {
			runtime := teamRuntime{}
			runtime.DSH.BinaryPath, runtime.DSH.BinaryArgs, runtime.DSH.Artifact = s.launcher.BinaryPath, s.launcher.BinaryArgs, s.launcher.Artifact
			launcher, err := toml.Marshal(map[string]any{"runtime": map[string]any{"dsh": runtime.DSH}})
			if err != nil {
				return nil, nil, err
			}
			source, err = removeConfigSubtrees(source, []string{"runtime"})
			if err != nil {
				return nil, nil, err
			}
			source = append(append(source, '\n'), launcher...)
		}
		patched, err := patchModelCatalog(source, next)
		return patched, func() {
			s.current = next.clone()
			if s.observe != nil {
				s.observe()
			}
			if input.afterCommit != nil {
				input.afterCommit(next)
			}
		}, err
	})
	retain = s.document.pending != nil || s.current.Version == next.Version && reflect.DeepEqual(s.current, next)
	return s.current.clone(), err
}

// DSH's probe slot is shared across Spaces. An admission rejection has not
// started a request, so queue briefly instead of failing another user's setup
// or model update. Never retry a probe that entered execution or failed cleanup.
func (s *modelService) probeWhenAvailable(ctx context.Context, opts dsh.Options) (dsh.ProbeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return dsh.ProbeResult{}, err
		}
		result, err := s.probe(ctx, opts)
		var rejected *dsh.ProbeError
		if !errors.As(err, &rejected) || rejected.Stage != "admission" || rejected.Code != "busy" {
			return result, err
		}
		select {
		case <-ctx.Done():
			return dsh.ProbeResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

type modelValidation struct {
	EntryID         string          `json:"entry_id"`
	Revision        uint64          `json:"revision"`
	ExecutionDigest string          `json:"execution_digest"`
	CredentialRef   string          `json:"credential_ref"`
	Result          dsh.ProbeResult `json:"result"`
}

func modelValidationMatches(entry modelEntry, receipt modelValidation) bool {
	return receipt.ExecutionDigest == entry.executionDigest() && receipt.CredentialRef == entry.CredentialRef && receipt.Result.Text && receipt.Result.ToolRoundTrip && receipt.Result.Cleanup && receipt.Result.Identity.Version != "" && receipt.Result.Identity.Agent != (dsh.Artifact{})
}

func saveModelValidation(dir string, record modelValidation) error {
	// Separate receipts from immutable key files; no key values are serialized.
	path := filepath.Join(filepath.Dir(dir), "model-validations")
	if err := makeDurableDir(path); err != nil {
		return err
	}
	return writeModelValidation(path, record)
}

// Kept deliberately explicit for the migration implementation: comparison
// covers wire parameters and resolved key bytes, including three-state compat.
func sameLegacyModel(a, b dsh.ModelConfig, keyA, keyB string) bool {
	if keyA == "" || keyB == "" {
		return false
	}
	if a.Protocol == "" {
		a.Protocol = "deepseek-messages"
	}
	if b.Protocol == "" {
		b.Protocol = "deepseek-messages"
	}
	return reflect.DeepEqual(parametersFromLegacyModel(a).materialize(), parametersFromLegacyModel(b).materialize()) && keyA == keyB
}

func writeModelValidation(dir string, record modelValidation) error {
	storage := &snapshotWriter{ops: defaultSnapshotOps()}
	return storage.save(filepath.Join(dir, record.ExecutionDigest+".json"), record, maxPrivateFileBytes)
}
