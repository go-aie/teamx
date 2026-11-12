package room

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// A single, bounded migration. Original bytes are private backups; the journal
// contains fixed targets and hashes, never credentials or a second model source.
type modelMigrationFile struct {
	Path   string `json:"path"`
	Before string `json:"before_sha256"`
	Target []byte `json:"target"`
}
type modelMigration struct {
	Version   int                  `json:"version"`
	Status    string               `json:"status"`
	Completed int                  `json:"completed"`
	Rooms     int                  `json:"rooms"`
	Templates int                  `json:"templates"`
	Catalog   modelCatalog         `json:"catalog"`
	Files     []modelMigrationFile `json:"files"`
}

func modelMigrationDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func runtimeMigrationDigest(r *teamRuntime) string {
	data, _ := json.Marshal(r)
	return modelMigrationDigest(data)
}

// Must run before any Room recovery or workers, while owning the data lock.
func migrateModelConfiguration(c startupConfig, checkpoint func(int) error) (startupConfig, error) {
	home := filepath.Dir(c.path)
	journal := filepath.Join(home, "model-migration.json")
	var migration modelMigration
	err := readSnapshot(journal, &migration, maxRoomsFileBytes)
	if err == nil && migration.Status == "completed" {
		return c, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if errors.Is(err, os.ErrNotExist) && (c.Models != nil || c.Runtime == nil || c.Runtime.Model.ModelID == "" || requireFreshModelHome(c) == nil) {
		return c, nil
	}
	if err := makeDurableDir(*c.Server.DataDir); err != nil {
		return c, err
	}
	lock, err := lockDirectory(*c.Server.DataDir)
	if err != nil {
		return c, err
	}
	defer unlockDirectory(lock)
	store := &Store{ops: defaultSnapshotOps()}
	if migration.Version == 0 {
		migration, err = planModelMigration(c)
		if err != nil {
			return c, err
		}
		if err = store.save(journal, migration, maxRoomsFileBytes); err != nil {
			return c, err
		}
	}
	if migration.Version != 1 || migration.Completed < 0 || migration.Completed > len(migration.Files) {
		return c, ErrInvalid
	}
	for i, file := range migration.Files {
		// Restrict targets to this deployment, even if the journal was edited.
		if !filepath.IsAbs(file.Path) || !(pathWithin(home, file.Path) || pathWithin(*c.Server.DataDir, file.Path) || pathWithin(*c.Server.TeamsDir, file.Path)) {
			return c, ErrIdentity
		}
		original, info, err := readSettingsFile(file.Path)
		if err != nil {
			return c, err
		}
		if !bytes.Equal(original, file.Target) {
			if modelMigrationDigest(original) != file.Before {
				return c, ErrConflict
			}
			doc := &configDocument{path: file.Path, source: original, file: info, private: true, ops: defaultSnapshotOps()}
			if err = doc.commitLocked(func([]byte) ([]byte, func(), error) { return file.Target, func() {}, nil }); err != nil {
				return c, err
			}
		} else {
			// A previous replace may have succeeded before its directory sync
			// failed. Re-establish durability before advancing the journal.
			doc := &configDocument{path: file.Path, source: original, file: info, private: true, ops: defaultSnapshotOps(), pending: &configCommit{source: file.Target, publish: func() {}}}
			if err = doc.reconcile(); err != nil {
				return c, err
			}
		}
		migration.Completed = i + 1
		if err = store.save(journal, migration, maxRoomsFileBytes); err != nil {
			return c, err
		}
		if checkpoint != nil {
			if err = checkpoint(i + 1); err != nil {
				return c, err
			}
		}
	}
	migration.Status = "completed"
	if err = store.save(journal, migration, maxRoomsFileBytes); err != nil {
		return c, err
	}
	return readStartupConfig(c.path, true, false)
}

func planModelMigration(c startupConfig) (modelMigration, error) {
	result := modelMigration{Version: 1, Status: "applying", Catalog: modelCatalog{SchemaVersion: 1, Version: 1, Default: "daily"}}
	credentials, err := newModelCredentials(filepath.Dir(c.path))
	if err != nil {
		return result, err
	}
	backup := filepath.Join(filepath.Dir(c.path), "model-migration-backup")
	if err = makeDurableDir(backup); err != nil {
		return result, err
	}
	if info, err := os.Lstat(backup); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return result, errors.New("model migration backup directory must be private (0700)")
	}
	keyRefs := map[string]string{}
	usedEnvs := map[string]bool{}
	originals := map[string][]byte{c.path: c.source}
	read := func(path string, target any) error {
		data, info, err := readSettingsFile(path)
		if err != nil {
			return err
		}
		if info == nil {
			return os.ErrNotExist
		}
		if err = json.Unmarshal(data, target); err != nil {
			return ErrInvalid
		}
		originals[path] = data
		return nil
	}
	importModel := func(model dsh.ModelConfig) (modelSelection, error) {
		if model.Protocol == "" {
			model.Protocol = "deepseek-messages"
		}
		key := legacyModelCredential(model.APIKeyEnv, c.Env)
		usedEnvs[model.APIKeyEnv] = true
		ref := keyRefs[key]
		if ref == "" {
			if key != "" {
				ref, err = credentials.put(key)
			} else {
				var id string
				id, err = newOpaqueID()
				ref = "cred-" + id
			}
			if err != nil {
				return modelSelection{}, err
			}
			keyRefs[key] = ref
		}
		entry := modelEntry{Revision: 1, CredentialRef: ref, Unverified: true, ModelParameters: parametersFromLegacyModel(model).clone()}
		for _, candidate := range result.Catalog.Entries {
			if key != "" && entry.sameExecution(candidate) {
				if candidate.ID == "daily" {
					return modelSelection{Mode: "default"}, nil
				}
				return modelSelection{Mode: "explicit", EntryID: candidate.ID}, nil
			}
		}
		entry.ID = "daily"
		entry.Name = "原默认模型"
		if len(result.Catalog.Entries) > 0 {
			entry.ID = fmt.Sprintf("imported-%d", len(result.Catalog.Entries))
			entry.Name = fmt.Sprintf("导入模型 %d", len(result.Catalog.Entries))
		}
		result.Catalog.Entries = append(result.Catalog.Entries, entry)
		if entry.ID == "daily" {
			return modelSelection{Mode: "default"}, nil
		}
		return modelSelection{Mode: "explicit", EntryID: entry.ID}, nil
	}
	add := func(path string, target []byte) error {
		original, _, err := readSettingsFile(path)
		if err != nil {
			return err
		}
		if expected, ok := originals[path]; ok && !bytes.Equal(expected, original) {
			return ErrConflict
		}
		if bytes.Equal(original, target) {
			return nil
		}
		// Backups are immutable evidence; repeated planning reuses only identical bytes.
		saved := filepath.Join(backup, modelMigrationDigest([]byte(path))+".json")
		var old struct {
			Path string `json:"path"`
			Data []byte `json:"data"`
		}
		if err := readSnapshot(saved, &old, maxRoomsFileBytes); err == nil {
			if old.Path != path || !bytes.Equal(old.Data, original) {
				return ErrConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else {
			old.Path, old.Data = path, original
			if err := (&Store{ops: defaultSnapshotOps()}).save(saved, old, maxRoomsFileBytes); err != nil {
				return err
			}
		}
		result.Files = append(result.Files, modelMigrationFile{path, modelMigrationDigest(original), target})
		return nil
	}
	marshalAdd := func(path string, v any) error {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return add(path, append(data, '\n'))
	}
	if _, err = importModel(c.Runtime.Model); err != nil {
		return result, err
	}
	convert := func(saved *managedRoomConfig, root string) error {
		if saved.Runtime == nil {
			return ErrInvalid
		}
		if saved.Runtime.ManagedModels {
			return nil
		}
		legacy := saved.Runtime.profile()
		if legacy.Model.Protocol == "" {
			legacy.Model.Protocol = "deepseek-messages"
		}
		selection, err := importModel(legacy.Model)
		if err != nil {
			return err
		}
		entry := result.Catalog.Entries[0]
		if selection.Mode == "explicit" {
			entry, _ = result.Catalog.resolve(selection)
		}
		for i, member := range saved.Members {
			id := MemberIdentity{saved.RoomIdentity, member.AgentID, member.MembershipID}
			path := filepath.Join(root, "agents", member.MembershipID, "session.json")
			ref, _, err := readDSHReference(path, id)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if ref.Directory != "" && !ref.Pending {
				p := legacy
				p.Instructions = member.Instructions
				p.Workspace = filepath.Join(root, filepath.FromSlash(member.Workspace))
				p.ToolEnv = cloneStringMap(member.ToolEnv)
				for k, v := range p.ToolEnv {
					p.ToolEnv[k] = resolveTeamResource(root, v)
				}
				frozen := modelSessionSnapshot{Legacy: true, Version: 1, MemberIdentity: id, Generation: ref.Directory, Selection: selection, SelectionVersion: 1, CatalogVersion: 1, EntryID: entry.ID, EntryRevision: 1, CredentialRef: entry.CredentialRef, ExecutionDigest: entry.executionDigest(), Profile: p}
				if err = frozen.validate(id); err != nil {
					return err
				}
				if err = marshalAdd(filepath.Join(root, "agents", member.MembershipID, ref.Directory+".model.json"), frozen); err != nil {
					return err
				}
			}
			s := selection
			saved.Members[i].ModelSelection = &s
			saved.Members[i].ModelSelectionVersion = 1
		}
		saved.ModelMigrationRuntimeDigest = runtimeMigrationDigest(saved.Runtime)
		runtime := *saved.Runtime
		runtime.ManagedModels = true
		runtime.Model = dsh.ModelConfig{}
		saved.Runtime = &runtime
		return nil
	}
	var index roomsIndex
	indexPath := filepath.Join(*c.Server.DataDir, "rooms.json")
	if err = read(indexPath, &index); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, op := range index.WorkspaceOperations {
		if !op.finished() {
			return result, ErrBusy
		}
	}
	for i, room := range index.Rooms {
		if !safeMembershipID(room.RoomID) {
			return result, ErrIdentity
		}
		root := filepath.Join(*c.Server.DataDir, "rooms", room.RoomID)
		path := filepath.Join(root, "room-config.json")
		var saved managedRoomConfig
		if err = read(path, &saved); err == nil {
			if saved.RoomIdentity != room.RoomIdentity {
				return result, ErrIdentity
			}
			if err = convert(&saved, root); err != nil {
				return result, err
			}
			if err = marshalAdd(path, saved); err != nil {
				return result, err
			}
			result.Rooms++
		} else if !errors.Is(err, os.ErrNotExist) || room.Status == "ready" {
			return result, err
		}
		if room.Initialization != nil {
			if err = convert(&index.Rooms[i].Initialization.Config, root); err != nil {
				return result, err
			}
		}
	}
	if index.Version != 0 {
		if err = marshalAdd(indexPath, index); err != nil {
			return result, err
		}
	}
	files, err := os.ReadDir(*c.Server.TeamsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, file := range files {
		if !file.IsDir() {
			continue
		}
		path := filepath.Join(*c.Server.TeamsDir, file.Name(), "team.json")
		var team teamDefinition
		if err = read(path, &team); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return result, err
		}
		if team.Runtime == nil {
			continue
		}
		selection, err := importModel(team.Runtime.Model)
		if err != nil {
			return result, err
		}
		for i := range team.Members {
			s := selection
			team.Members[i].ModelSelection = &s
		}
		team.Runtime.ManagedModels = true
		team.Runtime.Model = dsh.ModelConfig{}
		if err = marshalAdd(path, team); err != nil {
			return result, err
		}
		result.Templates++
	}
	source, err := removeConfigSubtrees(c.source, []string{"runtime", "model"})
	if err != nil {
		return result, err
	}
	for name := range usedEnvs {
		if name != "" {
			source, err = removeConfigSubtrees(source, []string{"env", name})
			if err != nil {
				return result, err
			}
		}
	}
	source, err = patchModelCatalog(source, result.Catalog)
	if err != nil {
		return result, err
	}
	// Catalog publication is last: no Room recovery sees a half-converted deployment.
	if err = add(c.path, source); err != nil {
		return result, err
	}
	if err = result.Catalog.validate(); err != nil {
		return result, err
	}
	return result, nil
}

func equalMigratedRuntime(current, historical managedRoomConfig) bool {
	return current.Runtime != nil && current.Runtime.ManagedModels && historical.Runtime != nil && !historical.Runtime.ManagedModels && current.ModelMigrationRuntimeDigest != "" && current.ModelMigrationRuntimeDigest == runtimeMigrationDigest(historical.Runtime) && reflect.DeepEqual(current.Runtime.DSH, historical.Runtime.DSH)
}
