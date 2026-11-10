//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/registry"
)

func TestPlatformModelUpgradePreservesP2StateAndRestarts(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	cfg := unifiedGitLabConfig(t, h)
	cfg.Todos.Enabled = true
	if _, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg); err != nil {
		t.Fatal(err)
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	// Build the actual P2 input format, including source snapshots and settings.
	before := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if !platformSnapshotPath(rel) && rel != "platform-connections.json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		before[rel] = b
		legacy := bytes.ReplaceAll(b, []byte(`"platform_id"`), []byte(`"connection_id"`))
		legacy = bytes.ReplaceAll(legacy, []byte(`"platform_generation"`), []byte(`"connection_generation"`))
		legacy = bytes.ReplaceAll(legacy, []byte(`"platform_version"`), []byte(`"connection_version"`))
		if rel == "platform-connections.json" {
			var f map[string]json.RawMessage
			if err = json.Unmarshal(legacy, &f); err != nil {
				return err
			}
			f["version"] = json.RawMessage("5")
			f["connections"] = f["platforms"]
			delete(f, "platforms")
			legacy, err = json.Marshal(f)
			if err != nil {
				return err
			}
		}
		return os.WriteFile(path, legacy, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runPlatformsUpgrade([]string{"upgrade", "--rooms-dir", root}, &out, &out); err != nil {
		t.Fatal(err)
	}
	for rel, want := range before {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		var x, y any
		if json.Unmarshal(b, &x) != nil || json.Unmarshal(want, &y) != nil {
			t.Fatal(rel)
		}
		// The registry schema alone advances; all business identities stay exact.
		if rel == "platform-connections.json" {
			x.(map[string]any)["version"] = float64(registry.ConfigurationVersion)
		}
		xb, _ := json.Marshal(x)
		yb, _ := json.Marshal(y)
		if !bytes.Equal(xb, yb) {
			t.Fatalf("changed business state %s", rel)
		}
	}
	journal, _ := os.ReadFile(filepath.Join(root, platformUpgradeJournal))
	if err := runPlatformsUpgrade([]string{"upgrade", "--rooms-dir", root}, &out, &out); err != nil {
		t.Fatal("repeat", err)
	}
	again, _ := os.ReadFile(filepath.Join(root, platformUpgradeJournal))
	if !bytes.Equal(journal, again) {
		t.Fatal("repeat replaced recovery evidence")
	}
	m := testManager(t, root, nil)
	if _, err := m.platforms.Load(); err != nil {
		t.Fatal(err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
}
