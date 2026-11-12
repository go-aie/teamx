package room

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

func legacyMigrationHome(t *testing.T) (startupConfig, roomSummary, MemberIdentity) {
	t.Helper()
	home := t.TempDir()
	runtime := testTeamDefinition(t).Runtime
	runtime.Model.Protocol = "openai-completions"
	runtime.Model.BaseURL = "https://example.invalid/v1"
	runtime.Model.APIKeyEnv = "TEAMX_LEGACY_MODEL_KEY"
	value := false
	runtime.Model.Compat.SupportsDeveloperRole = &value
	m := directTestManager(t, filepath.Join(home, "data"), runtime)
	room := createDirectConversation(t, m, directSelection("legacy"))
	id := modelMember(t, m, room)
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	source, err := toml.Marshal(map[string]any{"server": map[string]any{"data_dir": "data", "teams_dir": "teams"}, "runtime": runtime, "env": map[string]string{runtime.Model.APIKeyEnv: "configured-legacy-key", "UNRELATED": "keep"}})
	if err != nil {
		t.Fatal(err)
	}
	path := writeStartupConfig(t, home, string(source))
	config, err := loadStartupConfig(path, true)
	if err != nil {
		t.Fatal(err)
	}
	return config, room, id
}

func TestModelMigrationInterruptedFixedTargetsAndLegacySnapshot(t *testing.T) {
	c, room, id := legacyMigrationHome(t)
	root := filepath.Join(*c.Server.DataDir, "rooms", room.RoomID)
	path := filepath.Join(root, "agents", id.MembershipID, "session.json")
	storage := &Store{ops: defaultSnapshotOps()}
	original := dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}
	if err := storage.save(path, original, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(filepath.Dir(path), original.Directory)
	if err := os.Mkdir(history, 0700); err != nil {
		t.Fatal(err)
	}
	fingerprint := []byte(`{"configSHA256":"original-fingerprint","apiKeyEnv":"TEAMX_LEGACY_MODEL_KEY"}`)
	if err := os.WriteFile(filepath.Join(history, "backend.json"), fingerprint, 0600); err != nil {
		t.Fatal(err)
	}
	// An override differs only in compat; all three states survive deduplication.
	for i, role := range []*bool{nil, new(true)} {
		runtime := *c.Runtime
		runtime.Model = runtime.Model.Clone()
		runtime.Model.Compat.SupportsDeveloperRole = role
		team := testTeamDefinition(t)
		team.ID = string(rune('a' + i))
		team.Runtime = &runtime
		dir := filepath.Join(*c.Server.TeamsDir, team.ID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := storage.save(filepath.Join(dir, "team.json"), team, maxPrivateFileBytes); err != nil {
			t.Fatal(err)
		}
	}
	interrupted := errors.New("interrupted migration")
	_, err := migrateModelConfiguration(c, func(int) error { return interrupted })
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	var pending modelMigration
	journal := filepath.Join(filepath.Dir(c.path), "model-migration.json")
	if err := readSnapshot(journal, &pending, maxRoomsFileBytes); err != nil {
		t.Fatal(err)
	}
	if pending.Completed != 1 || pending.Status != "applying" {
		t.Fatal("missing progress")
	}
	// Restart with changed environment must keep the already-reserved credential.
	t.Setenv("TEAMX_LEGACY_MODEL_KEY", "later-key-must-not-replace-target")
	migrated, err := migrateModelConfiguration(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Runtime.Model.ModelID != "" || len(migrated.Models.Entries) != 3 || migrated.Models.Default != "daily" {
		t.Fatal("unexpected catalog", migrated.Models)
	}
	keys, err := newModelCredentials(filepath.Dir(c.path))
	if err != nil {
		t.Fatal(err)
	}
	for i, entry := range migrated.Models.Entries {
		if !entry.Unverified {
			t.Fatal("import fabricated validation")
		}
		key, err := keys.get(entry.CredentialRef)
		if err != nil || key.Value != "configured-legacy-key" {
			t.Fatal("target changed", err)
		}
		want := []*bool{new(false), nil, new(true)}[i]
		if !reflect.DeepEqual(entry.Compat.SupportsDeveloperRole, want) {
			t.Fatal("compat state lost")
		}
	}
	disk, _ := os.ReadFile(c.path)
	if bytes.Contains(disk, []byte("configured-legacy-key")) || bytes.Contains(disk, []byte("api_key_env")) || !bytes.Contains(disk, []byte("UNRELATED")) {
		t.Fatal("legacy source retained or unrelated config lost")
	}
	after, _ := os.ReadFile(filepath.Join(history, "backend.json"))
	if !bytes.Equal(after, fingerprint) {
		t.Fatal("fingerprint rewritten")
	}
	var frozen modelSessionSnapshot
	if err := readSnapshot(filepath.Join(filepath.Dir(path), original.Directory+".model.json"), &frozen, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := frozen.validate(id); err != nil || !frozen.Legacy || frozen.Profile.Model.APIKeyEnv != "TEAMX_LEGACY_MODEL_KEY" {
		t.Fatal("legacy snapshot changed", err)
	}
	backend := dshBackend{model: &frozen, credentials: keys, profile: frozen.Profile, store: &Store{dir: root}}
	options := adapterOptions(backend.store, id)
	actual, err := backend.componentOptions(options, original)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Credential.Ref != "" || actual.Credential.Value != "configured-legacy-key" || actual.Model.APIKeyEnv != "TEAMX_LEGACY_MODEL_KEY" {
		t.Fatal("legacy fingerprint or credential injection changed")
	}
	execution, err := loadModelExecution(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if len(execution.Validations) != 0 {
		t.Fatal("migration fabricated receipt")
	}
	migrated.Runtime.ManagedModels = true
	restarted := directTestManager(t, *migrated.Server.DataDir, migrated.Runtime)
	restarted.config.models = execution
	restarted.settings = newSystemSettings(migrated)
	activation, err := restarted.activationConfig(room.RoomIdentity)
	if err != nil {
		t.Fatal("migrated activation blocked", err)
	}
	for _, target := range activation.modelTargets {
		if !target.Legacy || target.Generation != original.Directory {
			t.Fatal("activation ignored original generation")
		}
	}
	if len(activation.modelTargets) != 1 {
		t.Fatal("original Session not assembled")
	}
	restarted.modelControl, err = newModelController(context.Background(), restarted, migrated, func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		t.Error("binding-only change reprobed original execution")
		return dsh.ProbeResult{}, ErrInvalid
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := modelChange(t, restarted.modelControl, modelChangeInput{Kind: "selection", MemberIdentity: id, Selection: modelSelection{Mode: "explicit", EntryID: "daily"}, ExpectedSelectionVersion: 1})
	requireModelComplete(t, binding)
	if len(binding.Members) != 1 || binding.Members[0].Rotate || !binding.Members[0].Target.Legacy {
		t.Fatal("binding-only import change reset original Session")
	}
	if err := restarted.close(); err != nil {
		t.Fatal(err)
	}
	// Fresh execution remains blocked, but model management can repair an import.
	settings := newSystemSettings(migrated)
	service, err := newModelService(migrated, settings, filepath.Dir(c.path))
	if err != nil {
		t.Fatal(err)
	}
	service.probe = successfulModelProbe
	service.launcher.BinaryPath = os.Args[0]
	saved, err := service.save(context.Background(), modelSave{Entry: migrated.Models.Entries[0], ExpectedVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Entries[0].Unverified {
		t.Fatal("verified import not repaired")
	}
	if _, err := migrateModelConfiguration(migrated, nil); err != nil {
		t.Fatal("completed migration repeated", err)
	}
	backups, err := os.ReadDir(filepath.Join(filepath.Dir(c.path), "model-migration-backup"))
	if err != nil || len(backups) == 0 {
		t.Fatal("backup missing", err)
	}
	for _, file := range backups {
		info, _ := file.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatal("backup exposed")
		}
	}
}

func TestModelMigrationMissingKeyAndSourceConflict(t *testing.T) {
	c, _, _ := legacyMigrationHome(t)
	t.Setenv("TEAMX_LEGACY_MODEL_KEY", "") // Explicit empty export wins over [env].
	_, err := migrateModelConfiguration(c, func(int) error { return errors.New("stop") })
	if err == nil {
		t.Fatal("expected interruption")
	}
	original, _ := os.ReadFile(c.path)
	if err := os.WriteFile(c.path, append(original, []byte("\n# external edit\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateModelConfiguration(c, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("migration overwrote external edit", err)
	}
	if err := os.WriteFile(c.path, original, 0600); err != nil {
		t.Fatal(err)
	}
	migrated, err := migrateModelConfiguration(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := newModelCredentials(filepath.Dir(c.path))
	for _, entry := range migrated.Models.Entries {
		if _, err := keys.get(entry.CredentialRef); err == nil {
			t.Fatal("empty export fell back to configured key")
		}
	}
	if _, err := loadModelExecution(migrated); err != nil {
		t.Fatal("missing key disabled management", err)
	}
	files, _ := os.ReadDir(filepath.Join(filepath.Dir(c.path), "model-credentials"))
	if len(files) != 0 {
		t.Fatal("missing key written")
	}
	disk, _ := os.ReadFile(c.path)
	if strings.Contains(string(disk), "configured-legacy-key") {
		t.Fatal("legacy key retained")
	}
}

func TestModelMigrationPendingGenerationRequiresVerifiedTarget(t *testing.T) {
	c, room, id := legacyMigrationHome(t)
	root := filepath.Join(*c.Server.DataDir, "rooms", room.RoomID)
	path := filepath.Join(root, "agents", id.MembershipID, "session.json")
	ref := dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh-" + strings.Repeat("a", 32), Pending: true}
	if err := (&Store{ops: defaultSnapshotOps()}).save(path, ref, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	loaded, err := migrateModelConfiguration(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ref.Directory+".model.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unstarted legacy generation received an execution snapshot", err)
	}
	execution, err := loadModelExecution(loaded)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Runtime.ManagedModels = true
	m := directTestManager(t, *loaded.Server.DataDir, loaded.Runtime)
	m.config.models = execution
	m.settings = newSystemSettings(loaded)
	control, err := newModelController(context.Background(), m, loaded, successfulModelProbe)
	if err != nil {
		t.Fatal(err)
	}
	m.modelControl = control
	control.inspect = func(ctx context.Context, o dsh.Options) (dsh.RuntimeIdentity, error) {
		r, _ := successfulModelProbe(ctx, o)
		r.Identity.Agent = dsh.Artifact{}
		return r.Identity, nil
	}
	activation, err := m.activationConfig(room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if len(activation.modelTargets) != 0 {
		t.Fatal("unverified pending import allowed execution")
	}
	for _, p := range activation.dshProfiles {
		if p.checkCredentials() == nil {
			t.Fatal("unverified member not blocked")
		}
	}
	op := modelChange(t, control, modelChangeInput{Kind: "save", Entry: loaded.Models.Entries[0]})
	requireModelComplete(t, op)
	if len(op.Members) != 1 || !op.Members[0].Rotate {
		t.Fatal("pending generation not installed from validation")
	}
	var newRef dshReference
	if err := readSnapshot(path, &newRef, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	var target modelSessionSnapshot
	if err := readSnapshot(filepath.Join(filepath.Dir(path), newRef.Directory+".model.json"), &target, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if target.Legacy || target.Profile.Model.APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatal("pending generation retained legacy transport")
	}
}

func TestModelMigrationWorkspaceAuthorizationRemainsExact(t *testing.T) {
	legacy := managedRoomConfig{Runtime: testTeamDefinition(t).Runtime, Workspace: &workspaceConfig{Root: "workspaces"}, WorkspaceVersion: 2}
	current := legacy
	runtime := *legacy.Runtime
	runtime.ManagedModels = true
	runtime.Model = dsh.ModelConfig{}
	current.Runtime = &runtime
	current.ModelMigrationRuntimeDigest = runtimeMigrationDigest(legacy.Runtime)
	if !equalWorkspaceRoomConfig(current, legacy) {
		t.Fatal("migration invalidated historical workspace authorization")
	}
	changed := legacy
	changed.Workspace = &workspaceConfig{Root: "other"}
	if equalWorkspaceRoomConfig(current, changed) {
		t.Fatal("workspace invariant disabled")
	}
	changed = legacy
	oldRuntime := *legacy.Runtime
	oldRuntime.Model = oldRuntime.Model.Clone()
	oldRuntime.Model.ModelID = "other"
	changed.Runtime = &oldRuntime
	if equalWorkspaceRoomConfig(current, changed) {
		t.Fatal("different historical runtime adopted")
	}
}
