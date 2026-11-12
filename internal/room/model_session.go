package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// A generation's execution fact. It is never edited through model management.
// Values of credentials and execution tokens are deliberately absent.
type modelSessionSnapshot struct {
	Legacy  bool `json:"legacy,omitempty"`
	Version int  `json:"version"`
	MemberIdentity
	Generation       string              `json:"generation"`
	Selection        modelSelection      `json:"selection"`
	SelectionVersion uint64              `json:"selection_version"`
	CatalogVersion   uint64              `json:"catalog_version"`
	EntryID          string              `json:"entry_id"`
	EntryRevision    uint64              `json:"entry_revision"`
	CredentialRef    string              `json:"credential_ref"`
	ExecutionDigest  string              `json:"execution_digest"`
	Runtime          dsh.RuntimeIdentity `json:"runtime"`
	Profile          dshProfile          `json:"profile"`
}

func (s modelSessionSnapshot) clone() modelSessionSnapshot {
	// These private, bounded snapshots contain only JSON data.
	s.Profile.Model = s.Profile.Model.Clone()
	s.Profile.BinaryArgs = append([]string(nil), s.Profile.BinaryArgs...)
	s.Profile.ToolEnv = cloneStringMap(s.Profile.ToolEnv)
	s.Runtime.BinaryArgs = append([]string(nil), s.Runtime.BinaryArgs...)
	s.Runtime.ScriptSHA256 = append([]string(nil), s.Runtime.ScriptSHA256...)
	return s
}
func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range input {
		out[k] = v
	}
	return out
}
func (s modelSessionSnapshot) validate(id MemberIdentity) error {
	if s.Version != 1 || s.MemberIdentity != id || !validSessionGeneration(s.Generation) || s.SelectionVersion == 0 || s.CatalogVersion == 0 || s.EntryRevision == 0 || !modelEntryID.MatchString(s.EntryID) || !validCredentialRef(s.CredentialRef) || len(s.ExecutionDigest) != 64 || !s.Legacy && (s.Runtime.Version == "" || s.Runtime.Agent == (dsh.Artifact{})) {
		return ErrInvalid
	}
	if err := s.Selection.validate(); err != nil {
		return err
	}
	if validateMemberHeader(formatVersion, id) != nil || !filepath.IsAbs(s.Profile.Workspace) {
		return ErrIdentity
	}
	expected := modelEntry{CredentialRef: s.CredentialRef, ModelParameters: parametersFromLegacyModel(s.Profile.Model)}
	if expected.executionDigest() != s.ExecutionDigest {
		return ErrInvalid
	}
	if err := dsh.CheckToolEnv(s.Profile.ToolEnv, s.Profile.Model.APIKeyEnv); err != nil {
		return err
	}
	if !s.Legacy && s.Profile.Model.APIKeyEnv != "OPENAI_API_KEY" {
		return ErrInvalid
	}
	return dsh.CheckConfig(dsh.Options{BinaryPath: s.Profile.BinaryPath, BinaryArgs: s.Profile.BinaryArgs, Artifact: s.Profile.Artifact, Model: s.Profile.Model})
}

func (b dshBackend) saveModelSnapshot(opts SessionOptions, ref dshReference) error {
	if b.model == nil {
		return nil
	}
	target := b.model.clone()
	target.Generation = ref.Directory
	if err := target.validate(opts.MemberIdentity); err != nil {
		return err
	}
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	path, err := b.store.dshPath(opts.MemberIdentity, ref.Directory+".model.json", true)
	if err != nil {
		return err
	}
	var existing modelSessionSnapshot
	if err := readSnapshot(path, &existing, maxPrivateFileBytes); err == nil {
		if !reflect.DeepEqual(existing, target) {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return b.store.save(path, target, maxPrivateFileBytes)
}

func (b dshBackend) restoreModelSnapshot(opts SessionOptions, ref dshReference) (dshBackend, error) {
	b.store.mu.Lock()
	defer b.store.mu.Unlock()
	path, err := b.store.dshPath(opts.MemberIdentity, ref.Directory+".model.json", false)
	if err != nil {
		return b, err
	}
	var frozen modelSessionSnapshot
	frozen, err = readModelSessionSnapshot(path, opts.MemberIdentity, ref.Directory)
	if err != nil {
		return b, err
	}
	b.model = &frozen
	b.profile = frozen.Profile
	return b, nil
}

func readModelSessionSnapshot(path string, id MemberIdentity, generation string) (modelSessionSnapshot, error) {
	var frozen modelSessionSnapshot
	if err := readSnapshot(path, &frozen, maxPrivateFileBytes); err != nil {
		return frozen, err
	}
	if err := frozen.validate(id); err != nil {
		return frozen, err
	}
	if frozen.Generation != generation {
		return frozen, ErrIdentity
	}
	return frozen, nil
}

func (b dshBackend) withModel(target modelSessionSnapshot) (dshBackend, error) {
	if err := target.validate(target.MemberIdentity); err != nil {
		return b, err
	}
	if _, err := b.credentials.get(target.CredentialRef); err != nil {
		return b, err
	}
	frozen := target.clone()
	b.model = &frozen
	b.profile = frozen.Profile
	return b, nil
}

func (b dshBackend) checkModelRuntime(ctx context.Context, opts dsh.Options) error {
	if b.model == nil || b.model.Legacy || b.open != nil {
		return nil
	}
	actual, err := dsh.InspectRuntime(ctx, opts)
	if err != nil {
		return err
	}
	expected := b.model.Runtime
	expected.Agent = dsh.Artifact{}
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("DSH installation changed after model validation")
	}
	return nil
}

// The loader reads the generation file before consulting any current catalog.
func (b dshBackend) switchModel(opts SessionOptions, from, to string, target modelSessionSnapshot) (Backend, error) {
	target.Generation = to
	next, err := b.withModel(target)
	if err != nil {
		return nil, err
	}
	if err := next.saveModelSnapshot(opts, dshReference{Directory: to}); err != nil {
		return nil, err
	}
	// This operation already owns a fixed target. Catalog resolution is for
	// later ordinary rotations, never for changing a prepared operation's target.
	execution := next.models
	next.models = nil
	backend, err := next.switchSession(opts, from, to)
	if err != nil {
		return backend, err
	}
	installed := backend.(dshBackend)
	installed.models = execution
	return installed, nil
}

func newModelSessionSnapshot(catalog modelCatalog, ref memberModelReference, generation string, profile dshProfile, validation modelValidation) (modelSessionSnapshot, error) {
	if ref.Version == 0 || validateMemberHeader(formatVersion, ref.MemberIdentity) != nil {
		return modelSessionSnapshot{}, ErrIdentity
	}
	entry, err := catalog.resolve(ref.Selection)
	if err != nil {
		return modelSessionSnapshot{}, err
	}
	if validation.EntryID != entry.ID || validation.Revision != entry.Revision || validation.CredentialRef != entry.CredentialRef || validation.ExecutionDigest != entry.executionDigest() || !validation.Result.Text || !validation.Result.ToolRoundTrip || !validation.Result.Cleanup {
		return modelSessionSnapshot{}, errors.New("model validation does not match the execution target")
	}
	if len(profile.ToolEnv) == 0 {
		profile.ToolEnv = nil
	}
	if len(profile.BinaryArgs) == 0 {
		profile.BinaryArgs = nil
	}
	profile.managedModel = false // execution snapshots must round-trip through JSON
	profile.Model = entry.materialize()
	snapshot := modelSessionSnapshot{Version: 1, MemberIdentity: ref.MemberIdentity, Generation: generation, Selection: ref.Selection, SelectionVersion: ref.Version, CatalogVersion: catalog.Version, EntryID: entry.ID, EntryRevision: entry.Revision, CredentialRef: entry.CredentialRef, ExecutionDigest: entry.executionDigest(), Runtime: validation.Result.Identity, Profile: profile}
	if err := snapshot.validate(ref.MemberIdentity); err != nil {
		return modelSessionSnapshot{}, err
	}
	return snapshot.clone(), nil
}
