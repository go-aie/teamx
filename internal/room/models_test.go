package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

func modelTestEntry(id string) modelEntry {
	return modelEntry{ID: id, Name: id, Revision: 1, CredentialRef: "cred-" + strings.Repeat("a", 32), ModelParameters: ModelParameters{Protocol: "openai-completions", BaseURL: "https://example.invalid/v1", ModelID: id}}
}
func modelTestCatalog() modelCatalog {
	return modelCatalog{SchemaVersion: 1, Version: 1, Default: "A", Entries: []modelEntry{modelTestEntry("A"), modelTestEntry("B")}}
}
func successfulModelProbe(_ context.Context, opts dsh.Options) (dsh.ProbeResult, error) {
	return dsh.ProbeResult{Identity: dsh.RuntimeIdentity{BinaryPath: opts.BinaryPath, Version: dsh.ProbeVersion, Agent: dsh.Artifact{Source: "fixture", Version: "1"}}, Text: true, ToolRoundTrip: true, Cleanup: true}, nil
}
func modelTestService(t *testing.T) (*modelService, *systemSettings, startupConfig) {
	t.Helper()
	home := t.TempDir()
	path := writeStartupConfig(t, home, "# preserve\n[server]\ntime_zone = 'Asia/Shanghai' # keep\n[env]\nUNRELATED = 'never change'\n")
	config, err := loadStartupConfig(path, true)
	if err != nil {
		t.Fatal(err)
	}
	settings := newSystemSettings(config)
	service, err := newModelService(config, settings, home)
	if err != nil {
		t.Fatal(err)
	}
	service.launcher = dsh.Options{BinaryPath: "/fixture-dsh"}
	service.probe = successfulModelProbe
	return service, settings, config
}

func TestModelCatalogReferenceSemantics(t *testing.T) {
	before := modelTestCatalog()
	room := RoomIdentity{RoomID: "same", RoomInstance: "instance"}
	a := MemberIdentity{room, "same-role", "member-A"}
	b := MemberIdentity{room, "same-role", "member-B"}
	refs := []memberModelReference{{a, modelSelection{Mode: "default"}, 1}, {b, modelSelection{Mode: "explicit", EntryID: "A"}, 1}}
	after := before.clone()
	after.Default = "B"
	impacts, err := modelImpacts(before, after, refs)
	if err != nil || !reflect.DeepEqual(impacts, []MemberIdentity{a}) {
		t.Fatal(impacts, err)
	}
	edited := after.clone()
	edited.Entries[0].ModelID = "A-new"
	edited.Entries[0].Revision++
	impacts, err = modelImpacts(after, edited, refs)
	if err != nil || !reflect.DeepEqual(impacts, []MemberIdentity{b}) {
		t.Fatal(impacts, err)
	}
	renamed := before.clone()
	renamed.Entries[0].Name = "renamed"
	impacts, err = modelImpacts(before, renamed, refs)
	if err != nil || len(impacts) != 0 {
		t.Fatal(impacts, err)
	}
	fixed, _ := before.resolve(modelSelection{Mode: "explicit", EntryID: "A"})
	def, _ := before.resolve(modelSelection{Mode: "default"})
	if !fixed.sameExecution(def) {
		t.Fatal("selection mode changes execution")
	}
	before.Entries[0].PendingDeletion = true
	if _, err := before.resolve(modelSelection{Mode: "default"}); err == nil {
		t.Fatal("deleting target resolved")
	}
}

func TestModelCatalogPatchAndCompatRoundTrip(t *testing.T) {
	for _, flag := range []*bool{nil, new(false), new(true)} {
		catalog := modelTestCatalog()

		catalog.Entries[0].Compat.SupportsDeveloperRole = flag
		for _, source := range []string{"", "# preserve\n[server]\ntime_zone='UTC' # keep\n[env]\nMULTILINE='''\n[models]\nnot = \"a header\"\n'''\n", "models = {schema_version=1,version=0,default='',entries=[]} # keep\nserver.time_zone='UTC'\n", "models.schema_version=1\nmodels.version=0\nmodels.default=''\nmodels.entries=[]\n[server]\ntime_zone='UTC'\n", "[ 'models' ] # keep\nschema_version=1\nversion=0\ndefault=''\nentries=[]\n[env]\nOTHER='untouched'\n"} {
			next, err := patchModelCatalog([]byte(source), catalog)
			if err != nil {
				t.Fatalf("source %q: %v", source, err)
			}
			var config startupConfig
			if err := toml.Unmarshal(next, &config); err != nil || !reflect.DeepEqual(config.Models, &catalog) {
				t.Fatalf("%s: %v %#v", next, err, config.Models)
			}
			if strings.Contains(source, "# keep") && !bytes.Contains(next, []byte("# keep")) {
				t.Fatal("comment removed")
			}
			var viaJSON modelCatalog
			body, _ := json.Marshal(catalog)
			if json.Unmarshal(body, &viaJSON) != nil || !reflect.DeepEqual(viaJSON, catalog) {
				t.Fatal("compat JSON changed")
			}
			again, err := patchModelCatalog(next, catalog)
			if err != nil {
				t.Fatal(err)
			}
			var check startupConfig
			if toml.Unmarshal(again, &check) != nil || !reflect.DeepEqual(check.Models, &catalog) {
				t.Fatal("repeat failed")
			}
		}
	}
	bad := modelTestCatalog()
	bad.Entries[0].Protocol = "deepseek-messages"
	bad.Entries[0].Compat.SupportsDeveloperRole = new(false)
	if bad.validate() == nil {
		t.Fatal("invalid protocol compat accepted")
	}
}

func TestModelServiceValidatedSaveRevisionAndRestart(t *testing.T) {
	service, settings, config := modelTestService(t)
	var probes atomic.Int32
	service.probe = func(ctx context.Context, o dsh.Options) (dsh.ProbeResult, error) {
		probes.Add(1)
		if o.Credential == nil || o.Credential.Value != "private-key" || o.Model.APIKeyEnv != "OPENAI_API_KEY" {
			t.Error("invalid materialization")
		}
		return successfulModelProbe(ctx, o)
	}
	entry := modelTestEntry("daily")
	entry.Compat.SupportsDeveloperRole = new(false)
	saved, err := service.save(context.Background(), modelSave{Entry: entry, APIKey: "private-key", ExpectedVersion: 0})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.Default != "daily" || saved.Entries[0].Revision != 1 || probes.Load() != 1 {
		t.Fatal(saved, probes.Load())
	}
	if _, err := settings.save("UTC", 1); err != nil {
		t.Fatal(err)
	}
	rename := saved.Entries[0]
	rename.Name = "Renamed"
	saved, err = service.save(context.Background(), modelSave{Entry: rename, ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if probes.Load() != 1 || saved.Entries[0].Revision != 1 || saved.Version != 2 {
		t.Fatal("rename probed/changed execution", saved)
	}
	_, err = service.save(context.Background(), modelSave{Entry: rename, ExpectedVersion: 1})
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	saved, err = service.save(context.Background(), modelSave{Entry: saved.Entries[0], ExpectedVersion: 2, APIKey: "private-key"})
	if err != nil || saved.Version != 2 || probes.Load() != 1 {
		t.Fatal("same key rotated", saved, err)
	}
	edited := saved.Entries[0]
	edited.Compat.SupportsDeveloperRole = new(true)
	saved, err = service.save(context.Background(), modelSave{Entry: edited, ExpectedVersion: 2})
	if err != nil || saved.Entries[0].Revision != 2 || probes.Load() != 2 {
		t.Fatal(saved, err)
	}
	disk, err := os.ReadFile(config.path)
	if err != nil || bytes.Contains(disk, []byte("private-key")) || !bytes.Contains(disk, []byte("# preserve")) || !bytes.Contains(disk, []byte("UNRELATED = 'never change'")) {
		t.Fatal("unsafe config", err)
	}
	loaded, err := loadStartupConfig(config.path, true)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newModelService(loaded, newSystemSettings(loaded), filepath.Dir(config.path))
	if err != nil || !reflect.DeepEqual(restarted.snapshot(), saved) {
		t.Fatal("restart", err)
	}
	key, err := restarted.credentials.get(saved.Entries[0].CredentialRef)
	if err != nil || key.Value != "private-key" {
		t.Fatal("restart credential", err)
	}
}

func TestModelServiceFailureConflictAndSharedWriter(t *testing.T) {
	service, settings, config := modelTestService(t)
	before, _ := os.ReadFile(config.path)
	service.probe = func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		return dsh.ProbeResult{}, &dsh.ProbeError{Stage: "text", Code: "authentication"}
	}
	if _, err := service.save(context.Background(), modelSave{Entry: modelTestEntry("A"), APIKey: "failed-key"}); err == nil {
		t.Fatal("failed probe saved")
	}
	after, _ := os.ReadFile(config.path)
	files, _ := os.ReadDir(service.credentials.dir)
	if !bytes.Equal(before, after) || service.snapshot().Version != 0 || len(files) != 0 {
		t.Fatal("failed candidate published or leaked")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	service.probe = func(ctx context.Context, o dsh.Options) (dsh.ProbeResult, error) {
		close(entered)
		<-release
		return successfulModelProbe(ctx, o)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var saveErr error
	go func() {
		defer wg.Done()
		_, saveErr = service.save(context.Background(), modelSave{Entry: modelTestEntry("A"), APIKey: "new-key"})
	}()
	<-entered
	if _, err := settings.save("UTC", 1); err != nil {
		t.Fatal("probe held config lock", err)
	}
	if _, err := service.save(context.Background(), modelSave{Entry: modelTestEntry("B"), APIKey: "other"}); !errors.Is(err, ErrBusy) {
		t.Fatal("probe overlap", err)
	}
	close(release)
	wg.Wait()
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	if settings.snapshot().TimeZone != "UTC" {
		t.Fatal("timezone lost")
	}
	if err := os.WriteFile(config.path, append(service.document.source, []byte("\n# external\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	entry := service.snapshot().Entries[0]
	entry.Name = "external conflict"
	if _, err := service.save(context.Background(), modelSave{Entry: entry, ExpectedVersion: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("external write overwritten", err)
	}
}

func TestModelCredentialsIsolationAndLegacyPrecedence(t *testing.T) {
	home := t.TempDir()
	store, err := newModelCredentials(home)
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.put("key-A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.put("key-B")
	if err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{a: "key-A", b: "key-B"} {
		key, err := store.get(ref)
		if err != nil || key.Value != want {
			t.Fatal(err)
		}
		info, _ := os.Stat(filepath.Join(store.dir, ref))
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
	}
	if _, err := store.get("../outside"); err == nil {
		t.Fatal("path traversal")
	}
	if err := os.Chmod(filepath.Join(store.dir, a), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.get(a); err == nil {
		t.Fatal("public key file accepted")
	}
	t.Setenv("LEGACY_MODEL_TEST_KEY", "")
	if value := legacyModelCredential("LEGACY_MODEL_TEST_KEY", map[string]string{"LEGACY_MODEL_TEST_KEY": "configured"}); value != "" {
		t.Fatal("explicit empty lost")
	}
	t.Setenv("LEGACY_MODEL_TEST_KEY", "exported")
	if value := legacyModelCredential("LEGACY_MODEL_TEST_KEY", map[string]string{"LEGACY_MODEL_TEST_KEY": "configured"}); value != "exported" {
		t.Fatal("process precedence lost")
	}
}

func TestModelConfigUncertainCommitReconcilesAcrossWriters(t *testing.T) {
	service, settings, config := modelTestService(t)
	syncDir := service.document.ops.syncDir
	service.document.ops.syncDir = func(string) error { return errors.New("injected durability failure") }
	current, err := service.save(context.Background(), modelSave{Entry: modelTestEntry("A"), APIKey: "retained-key"})
	if !errors.Is(err, ErrCommitUnknown) || current.Version != 0 || service.document.pending == nil {
		t.Fatal(current, err)
	}
	files, _ := os.ReadDir(service.credentials.dir)
	if len(files) != 1 {
		t.Fatal("uncertain credential discarded")
	}
	service.document.ops.syncDir = syncDir
	if _, err := settings.save("UTC", 1); err != nil {
		t.Fatal("shared reconciliation failed", err)
	}
	current = service.snapshot()
	if current.Version != 1 || service.document.pending != nil {
		t.Fatal("model snapshot not published", current)
	}
	loaded, err := loadStartupConfig(config.path, true)
	if err != nil || loaded.Models.Version != 1 || *loaded.Server.TimeZone != "UTC" {
		t.Fatal("reconciled values lost", err)
	}
}

func TestModelServiceRejectsExistingData(t *testing.T) {
	service, settings, config := modelTestService(t)
	if err := os.MkdirAll(*config.Server.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*config.Server.DataDir, "rooms.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newModelService(config, settings, filepath.Dir(service.credentials.dir)); err == nil {
		t.Fatal("existing-data write gate bypassed")
	}
}

func TestModelServiceRepairsMissingValidationWithoutChangingExecution(t *testing.T) {
	service, _, _ := modelTestService(t)
	catalog, err := service.save(context.Background(), modelSave{Entry: modelTestEntry("A"), APIKey: "retained-key"})
	if err != nil {
		t.Fatal(err)
	}
	entry := catalog.Entries[0]
	path := filepath.Join(filepath.Dir(service.credentials.dir), "model-validations", entry.executionDigest()+".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	var probes int
	service.probe = func(ctx context.Context, o dsh.Options) (dsh.ProbeResult, error) {
		probes++
		return successfulModelProbe(ctx, o)
	}
	next, err := service.save(context.Background(), modelSave{Entry: entry, ExpectedVersion: catalog.Version})
	if err != nil {
		t.Fatal(err)
	}
	var receipt modelValidation
	if probes != 1 || next.Entries[0].Revision != entry.Revision || readSnapshot(path, &receipt, maxPrivateFileBytes) != nil || !modelValidationMatches(entry, receipt) {
		t.Fatal("missing receipt was not repaired without a new execution revision")
	}
}

func TestModelSessionSnapshotBindsCompleteValidatedTarget(t *testing.T) {
	catalog := modelTestCatalog()
	entry := catalog.Entries[0]
	id := MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "member"}
	ref := memberModelReference{MemberIdentity: id, Selection: modelSelection{Mode: "default"}, Version: 1}
	result, _ := successfulModelProbe(context.Background(), dsh.Options{BinaryPath: "/fixture-dsh"})
	validation := modelValidation{EntryID: entry.ID, Revision: entry.Revision, CredentialRef: entry.CredentialRef, ExecutionDigest: entry.executionDigest(), Result: result}
	profile := dshProfile{BinaryPath: "/fixture-dsh", Workspace: t.TempDir(), Instructions: "role"}
	snapshot, err := newModelSessionSnapshot(catalog, ref, "session.json.dsh", profile, validation)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EntryID != "A" || snapshot.Selection.Mode != "default" || snapshot.Profile.Model.ModelID != "A" || snapshot.CredentialRef != entry.CredentialRef {
		t.Fatal(snapshot)
	}
	changed := catalog.clone()
	changed.Entries[0].Compat.SupportsDeveloperRole = new(false)
	if _, err := newModelSessionSnapshot(changed, ref, "session.json.dsh", profile, validation); err == nil {
		t.Fatal("mismatched execution accepted")
	}
	changed = catalog.clone()
	changed.Entries[0].CredentialRef = "cred-" + strings.Repeat("b", 32)
	if _, err := newModelSessionSnapshot(changed, ref, "session.json.dsh", profile, validation); err == nil {
		t.Fatal("mismatched credential accepted")
	}
}

func TestModelMigrationComparisonPreservesCompatAndCredentialPrecedence(t *testing.T) {
	base := modelTestEntry("A").materialize()
	other := base.Clone()
	other.APIKeyEnv = "CUSTOM_KEY"
	if !sameLegacyModel(base, other, "same", "same") {
		t.Fatal("variable spelling changed execution")
	}
	if sameLegacyModel(base, other, "first", "second") {
		t.Fatal("different credentials collapsed")
	}
	for _, flag := range []*bool{new(false), new(true)} {
		other = base.Clone()
		other.Compat.SupportsDeveloperRole = flag
		if sameLegacyModel(base, other, "same", "same") {
			t.Fatal("compat tri-state collapsed")
		}
		imported := parametersFromLegacyModel(other)
		if imported.Compat.SupportsDeveloperRole == nil || *imported.Compat.SupportsDeveloperRole != *flag {
			t.Fatal("compat migration mapping lost")
		}
	}
	empty := base.Clone()
	empty.InputModalities = []string{}
	if !modelTestEntry("A").sameExecution(modelEntry{CredentialRef: modelTestEntry("A").CredentialRef, ModelParameters: parametersFromLegacyModel(empty)}) {
		t.Fatal("empty modalities created execution change")
	}
}

func TestModelEntriesWithSameWireConfigAndKeyShareExecution(t *testing.T) {
	service, _, _ := modelTestService(t)
	first := modelTestEntry("A")
	current, err := service.save(context.Background(), modelSave{Entry: first, APIKey: "shared-key"})
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "B"
	second.Name = "Alias"
	next, err := service.save(context.Background(), modelSave{Entry: second, APIKey: "shared-key", ExpectedVersion: current.Version, MakeDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Entries[0].sameExecution(next.Entries[1]) {
		t.Fatal("identical wire config/key split execution")
	}
	files, _ := os.ReadDir(service.credentials.dir)
	if len(files) != 1 {
		t.Fatal("duplicated immutable credential")
	}
	probe := service.probe
	service.probe = func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		t.Fatal("identical default target was re-probed")
		return dsh.ProbeResult{}, nil
	}
	next, err = service.save(context.Background(), modelSave{Entry: next.Entries[0], ExpectedVersion: next.Version, MakeDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	service.probe = probe
	next, err = service.save(context.Background(), modelSave{Entry: next.Entries[1], APIKey: "changed-key", ExpectedVersion: next.Version})
	if err != nil {
		t.Fatal(err)
	}
	if next.Entries[0].sameExecution(next.Entries[1]) || next.Entries[1].Revision != 2 {
		t.Fatal("changed credential not revisioned")
	}
}
