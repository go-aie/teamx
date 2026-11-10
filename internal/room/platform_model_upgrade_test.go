package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlatformIdentityConversionIsLosslessAndRejectsConflicts(t *testing.T) {
	old := json.RawMessage(`{"binding":{"connection_id":"same","connection_generation":"gen","connection_version":9007199254740993},"cursor":"original","input":{"connection_id":"business-text"},"body":"connection_id"}`)
	next, changed, err := convertPlatformJSON(old)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(next, &fields) != nil {
		t.Fatal("invalid candidate")
	}
	if string(fields["binding"]) != `{"platform_generation":"gen","platform_id":"same","platform_version":9007199254740993}` || string(fields["input"]) != `{"connection_id":"business-text"}` || string(fields["cursor"]) != `"original"` {
		t.Fatal(string(next))
	}
	again, changed, err := convertPlatformJSON(next)
	if err != nil || changed || string(again) != string(next) {
		t.Fatal("non-idempotent", err)
	}
	for _, s := range []string{`{"connection_id":"a","platform_id":"b"}`, `{"binding":{"connection_version":1,"platform_version":1}}`} {
		if _, _, err := convertPlatformJSON(json.RawMessage(s)); !errors.Is(err, ErrConflict) {
			t.Fatal("accepted ambiguous identity", err)
		}
	}
}
func TestPlatformUpgradeJournalRecoveryAndConflict(t *testing.T) {
	root := t.TempDir()
	lock, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{dir: root, lock: lock, ops: defaultSnapshotOps()}
	defer s.Close()
	j := platformUpgrade{Version: 1, Files: []platformFileChange{{Path: "one.json", Before: json.RawMessage(`{"connection_id":"a"}`), After: json.RawMessage(`{"platform_id":"a"}`)}, {Path: "two.json", Before: json.RawMessage(`{"connection_id":"b"}`), After: json.RawMessage(`{"platform_id":"b"}`)}}}
	for _, c := range j.Files {
		if err := s.save(filepath.Join(root, c.Path), c.Before, 1024); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.save(filepath.Join(root, platformUpgradeJournal), j, platformUpgradeLimit); err != nil {
		t.Fatal(err)
	}
	if err := checkPlatformUpgrade(root); !errors.Is(err, ErrVersion) {
		t.Fatal("mixed startup allowed", err)
	}
	// First file committed before interruption; retry accepts either exact image.
	if err := s.save(filepath.Join(root, "one.json"), j.Files[0].After, 1024); err != nil {
		t.Fatal(err)
	}
	if err := s.save(filepath.Join(root, "two.json"), json.RawMessage(`{"platform_id":"foreign"}`), 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := resumePlatformUpgrade(s); !errors.Is(err, ErrConflict) {
		t.Fatal("external mutation accepted", err)
	}
	if err := s.save(filepath.Join(root, "two.json"), j.Files[1].Before, 1024); err != nil {
		t.Fatal(err)
	}
	if resumed, err := resumePlatformUpgrade(s); err != nil || !resumed {
		t.Fatal(resumed, err)
	}
	if err := checkPlatformUpgrade(root); err != nil {
		t.Fatal(err)
	}
	for _, c := range j.Files {
		b, err := os.ReadFile(filepath.Join(root, c.Path))
		if err != nil || !equalPlatformJSON(b, c.After) {
			t.Fatal(string(b), err)
		}
	}
	if resumed, err := resumePlatformUpgrade(s); err != nil || resumed {
		t.Fatal("complete journal replayed", resumed, err)
	}
}

func TestPlatformFormatConversionPreservesActionReceiptsAndAuthority(t *testing.T) {
	f := referenceExecuteTest(t, "object")
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	before, err := f.h.store.loadActionsLocked(f.h.id)
	if err != nil || len(before) != 1 {
		t.Fatal(err)
	}
	root := f.h.store.actionRoot(f.h.id)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		old := bytes.ReplaceAll(b, []byte(`"platform_id"`), []byte(`"connection_id"`))
		old = bytes.ReplaceAll(old, []byte(`"platform_version"`), []byte(`"connection_version"`))
		next, _, err := convertPlatformJSON(bytes.TrimSpace(old))
		if err != nil {
			return err
		}
		var original, converted any
		if json.Unmarshal(b, &original) != nil || json.Unmarshal(next, &converted) != nil {
			return ErrInvalid
		}
		if !reflect.DeepEqual(original, converted) {
			t.Fatalf("receipt or authority changed: %s", path)
		}
		return os.WriteFile(path, next, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.h.store.loadActionsLocked(f.h.id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("action evidence changed", err)
	}
	reloadConversation(t, f.h)
	f.svc = referenceTestService(f.h)
	result, err := f.run()
	if err != nil || result.Status != "succeeded" || f.calls.Load() != 1 {
		t.Fatal("receipt replay", result, err, f.calls.Load())
	}
}

func TestPlatformConversionCoversBindingsWorkspaceAndPendingOperations(t *testing.T) {
	// Each payload exercises a distinct persisted consumer, including frozen
	// before/after configurations of an unfinished Workspace operation.
	for _, data := range []string{
		`{"bindings":[{"platform_id":"p","platform_generation":"g","credential_version":9,"binding_version":4,"membership_id":"m"}],"revision":11}`,
		`{"workspace":{"binding":{"platform_id":"p","platform_generation":"g","project_id":71}},"workspace_version":5}`,
		`{"workspace_operations":[{"status":"running","phase":"committing","target":{"binding":{"platform_id":"p"}},"previous_config":{"workspace":{"binding":{"platform_id":"old","platform_generation":"old-g"}}},"prepared_config":{"workspace":{"binding":{"platform_id":"p","platform_generation":"g"}}},"sessions_updated":["m"],"target_version":8}]}`,
		`{"access":{"platform_id":"p","platform_version":7,"generation":"g","credential_version":9},"last_attempt":{"access":{"platform_id":"p","platform_version":6}},"revision":4}`,
	} {
		original := []byte(data)
		legacy := bytes.ReplaceAll(original, []byte(`"platform_id"`), []byte(`"connection_id"`))
		legacy = bytes.ReplaceAll(legacy, []byte(`"platform_generation"`), []byte(`"connection_generation"`))
		legacy = bytes.ReplaceAll(legacy, []byte(`"platform_version"`), []byte(`"connection_version"`))
		converted, changed, err := convertPlatformJSON(legacy)
		if err != nil || !changed {
			t.Fatal(err)
		}
		var a, b any
		if json.Unmarshal(original, &a) != nil || json.Unmarshal(converted, &b) != nil || !reflect.DeepEqual(a, b) {
			t.Fatalf("changed evidence: %s", converted)
		}
	}
}
