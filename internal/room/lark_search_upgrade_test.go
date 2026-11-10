//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Old codecs remain fixtures for explicit conversion only. No historical
// scheduler is available in the shipped Lark package.
type legacyLarkCodec struct{ lark.Source }

func (*legacyLarkCodec) NewRunner(notification.StepHost) notification.Runner {
	panic("legacy codec fixture cannot start a collector")
}

func seedLarkSchemaThree(t *testing.T, root, path string, state lark.State) lark.State {
	t.Helper()
	state.Search, state.Directory = nil, nil
	raw, _ := json.Marshal(state)
	if _, err := (&lark.Source{}).Decode(raw, state.RoomIdentity); err != nil {
		t.Fatal("legacy source fixture", err)
	}
	wire, _ := json.Marshal(notificationEnvelope{SourceID: lark.MessagesSourceID, Schema: 3, State: raw})
	if err := os.WriteFile(path, wire, 0600); err != nil {
		t.Fatal(err)
	}
	return state
}

// Match a mode-preserving whole-directory backup. os.CopyFS alone adds read
// permissions and therefore cannot produce a valid private platform registry.
func copyLarkRollbackStore(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Mkdir(to, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return ErrInvalid
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		return os.Chmod(filepath.Join(to, rel), info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Rollback restores the complete stopped store, not only the source's v3.bak.
// This includes the member Inbox and admission receipts saved before upgrade.
func TestLarkSearchUpgradeWholeStoreRollback(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	want := h.state()
	if len(want.Inbox) != 0 || len(want.Accepted) != 1 {
		t.Fatal("rollback fixture needs an acknowledged delivery")
	}
	sourcePath, err := h.m.notifications.StatePathLocked(want.Metadata().Key)
	if err != nil {
		t.Fatal(err)
	}
	root, index := h.m.store.dir, h.m.index
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	want = seedLarkSchemaThree(t, root, sourcePath, want)
	// The directory lock is mandatory even for backup. A running process must
	// prevent both the offline upgrader and a claimed consistent backup.
	lock, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{dir: root, lock: lock, ops: defaultSnapshotOps()}
	defer store.Close()
	if other, err := lockDirectory(root); err == nil {
		_ = unlockDirectory(other)
		t.Fatal("offline operation ignored the service lock")
	}
	backup := filepath.Join(t.TempDir(), "before-upgrade")
	copyLarkRollbackStore(t, root, backup)
	offline := &roomManager{store: store, index: index}
	offline.initPlatform()
	a := offline.notificationAdapters[lark.MessagesSourceID]
	a.Source = &lark.SearchSource{}
	offline.notificationAdapters[lark.MessagesSourceID] = a
	if _, err := offline.upgradeMemberNotificationsLocked(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope notificationEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Schema != lark.SearchSchema {
		t.Fatal("rehearsal did not upgrade")
	}
	if _, err := (&lark.Source{}).Decode(envelope.State, want.RoomIdentity); err == nil {
		t.Fatal("old codec accepted the upgraded source")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Retain the upgraded tree for reconciliation, and restore into an empty
	// directory. Never overlay the backup onto newer Room/Inbox evidence.
	retained := filepath.Join(t.TempDir(), "after-upgrade")
	copyLarkRollbackStore(t, root, retained)
	restoredRoot := filepath.Join(t.TempDir(), "restored")
	copyLarkRollbackStore(t, backup, restoredRoot)
	files := 0
	err = filepath.WalkDir(backup, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(backup, path)
		before, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		after, err := os.ReadFile(filepath.Join(restoredRoot, rel))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("whole-store restore changed %s", rel)
		}
		originalInfo, err := d.Info()
		if err != nil {
			return err
		}
		restoredInfo, err := os.Stat(filepath.Join(restoredRoot, rel))
		if err != nil || restoredInfo.Mode().Perm() != originalInfo.Mode().Perm() {
			t.Fatalf("whole-store restore changed permissions: %s", rel)
		}
		files++
		return nil
	})
	if err != nil || files < 5 {
		t.Fatal("whole-store restore verification", files, err)
	}
	// The old codec can decode the restored source; current startup correctly
	// refuses it until explicit upgrade, without rewriting the consistent backup.
	rel, _ := filepath.Rel(root, sourcePath)
	beforeRaw, err := os.ReadFile(filepath.Join(restoredRoot, rel))
	var oldEnvelope notificationEnvelope
	if err != nil || json.Unmarshal(beforeRaw, &oldEnvelope) != nil {
		t.Fatal(err)
	}
	decoded, err := (&lark.Source{}).Decode(oldEnvelope.State, want.RoomIdentity)
	if err != nil || !reflect.DeepEqual(decoded, want) {
		t.Fatal("old codec did not reopen the consistent backup", err)
	}
	if _, err := os.Stat(filepath.Join(retained, larkSearchUpgradeJournal)); err != nil {
		t.Fatal("post-upgrade evidence not retained")
	}
}

// Exercises the existing offline transaction against a fixture snapshot copy.
// Production uses schema 4; fixtures explicitly seed the predecessor format.
func TestLarkSearchUpgradeTransactionRecovery(t *testing.T) {
	for _, boundary := range []string{"backup", "source", "complete"} {
		t.Run(boundary, func(t *testing.T) {
			h := newLarkHarness(t)
			h.start()
			want := h.state()
			path, err := h.m.notifications.StatePathLocked(want.Metadata().Key)
			if err != nil {
				t.Fatal(err)
			}
			root, index := h.m.store.dir, h.m.index
			if err := h.m.close(); err != nil {
				t.Fatal(err)
			}
			want = seedLarkSchemaThree(t, root, path, want)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := lockDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			store := &Store{dir: root, lock: lock, ops: defaultSnapshotOps()}
			defer store.Close()
			offline := &roomManager{store: store, index: index}
			offline.initPlatform()
			adapter := offline.notificationAdapters[lark.MessagesSourceID]
			adapter.Source = &lark.SearchSource{}
			offline.notificationAdapters[lark.MessagesSourceID] = adapter
			rename := store.ops.rename
			store.ops.rename = func(from, to string) error {
				if err := rename(from, to); err != nil {
					return err
				}
				fail := boundary == "backup" && to == path+".v3.bak" || boundary == "source" && to == path
				if boundary == "complete" && to == filepath.Join(root, larkSearchUpgradeJournal) {
					var j platformUpgrade
					if readLarkUpgradeJournal(to, &j) == nil && j.Complete {
						fail = true
					}
				}
				if fail {
					return errors.New("injected post-rename failure")
				}
				return nil
			}
			if _, err := offline.upgradeMemberNotificationsLocked(); err == nil {
				t.Fatal("fault not injected")
			}
			backup, err := os.ReadFile(path + ".v3.bak")
			if err != nil || !equalPlatformJSON(backup, before) {
				t.Fatal("original backup missing", err)
			}
			if boundary != "complete" && checkMemberNotificationsUpgrade(root) == nil {
				t.Fatal("partial upgrade did not fence startup")
			}
			// Close the latched store and resume under a fresh offline lock, as after
			// a crash. Revalidation accepts only the exact preimage or candidate.
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			lock, err = lockDirectory(root)
			if err != nil {
				t.Fatal(err)
			}
			store = &Store{dir: root, lock: lock, ops: defaultSnapshotOps()}
			defer store.Close()
			offline.store = store
			if _, err := offline.upgradeMemberNotificationsLocked(); err != nil {
				t.Fatal("resume", err)
			}
			if checkMemberNotificationsUpgrade(root) != nil {
				t.Fatal("incomplete resumed journal")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var env notificationEnvelope
			if decodeNotificationJSON(after, &env) != nil || env.Schema != lark.SearchSchema {
				t.Fatal("wrong schema")
			}
			decoded, err := (&lark.SearchSource{}).Decode(env.State, want.RoomIdentity)
			if err != nil {
				t.Fatal(err)
			}
			got := decoded.(lark.State)
			if got.SubscriptionID != want.SubscriptionID || got.SubscriptionVersion != want.SubscriptionVersion || got.Baseline != want.Baseline || !reflect.DeepEqual(got.Inbox, want.Inbox) || !reflect.DeepEqual(got.Observed, want.Observed) || !reflect.DeepEqual(got.Accepted, want.Accepted) {
				t.Fatal("upgrade changed evidence")
			}
			if _, err := offline.upgradeMemberNotificationsLocked(); err != nil {
				t.Fatal("repeat", err)
			}
			repeated, _ := os.ReadFile(path)
			if !equalPlatformJSON(after, repeated) {
				t.Fatal("repeat changed source")
			}
			var original notificationEnvelope
			_ = json.Unmarshal(before, &original)
			if original.Schema != 3 {
				t.Fatal("test did not start from production format")
			}
			if info, err := os.Stat(path + ".v3.bak"); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("backup permissions")
			}
		})
	}
}
