//go:build integration || e2e

package room

import (
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

// Offline composition reads the same formats and adapters as startup, without
// starting provider workers or a Room/Agent runtime.
func offlineSearchManager(t *testing.T, root string) *roomManager {
	t.Helper()
	lock, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{dir: root, lock: lock, ops: defaultSnapshotOps()}
	t.Cleanup(func() { _ = store.Close() })
	m := &roomManager{store: store}
	m.initPlatform()
	a := m.notificationAdapters[lark.MessagesSourceID]
	a.Source = &lark.SearchSource{}
	m.notificationAdapters[lark.MessagesSourceID] = a
	m.initPlatformNotifications()
	if err := readSnapshot(m.indexPath(), &m.index, maxRoomsFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := m.validateIndex(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.readPlatforms(); err != nil {
		t.Fatal(err)
	}
	return m
}

func searchSnapshotDigests(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrInvalid
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// The input is an immutable, consistent backup. Only the new output copy can
// be upgraded; there is deliberately no live-store migration in this test.
func TestLarkSearchUpgradeSnapshotCopy(t *testing.T) {
	input := os.Getenv("TEAMX_LARK_SEARCH_SNAPSHOT_INPUT")
	if input == "" {
		t.Skip("set TEAMX_LARK_SEARCH_SNAPSHOT_INPUT to a stopped-store backup")
	}
	output := os.Getenv("TEAMX_LARK_SEARCH_SNAPSHOT_OUTPUT")
	if !filepath.IsAbs(input) || !filepath.IsAbs(output) || input == output || filepath.Dir(output) == input {
		t.Fatal("snapshot input and new output must be separate absolute paths")
	}
	lock, err := lockDirectory(input)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockDirectory(lock)
	before := searchSnapshotDigests(t, input)
	copyLarkRollbackStore(t, input, output)
	m := offlineSearchManager(t, output)
	count, err := m.upgradeMemberNotificationsLocked()
	if err != nil {
		t.Fatal("snapshot-copy upgrade", err)
	}
	if err := m.loadPlatformNotificationsLocked(); err != nil {
		t.Fatal("upgraded snapshot-copy load", err)
	}
	subscriptions, pending := 0, 0
	for _, room := range m.index.Rooms {
		for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
			s, ok := state.(lark.State)
			if !ok {
				continue
			}
			subscriptions++
			pending += len(s.Inbox)
			path, err := m.notifications.StatePathLocked(s.Metadata().Key)
			if err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(output, path)
			raw, err := os.ReadFile(filepath.Join(input, rel))
			if err != nil {
				t.Fatal(err)
			}
			var old notificationEnvelope
			if json.Unmarshal(raw, &old) != nil {
				t.Fatal("original envelope")
			}
			decoded, err := (&lark.Source{}).Decode(old.State, room.RoomIdentity)
			if err != nil {
				t.Fatal("expected schema-3 source in backup", err)
			}
			previous := decoded.(lark.State)
			if s.SubscriptionID != previous.SubscriptionID || s.SubscriptionVersion != previous.SubscriptionVersion || s.Binding != previous.Binding || s.Config.Target != previous.Config.Target || s.Baseline != previous.Baseline || !reflect.DeepEqual(s.Inbox, previous.Inbox) || !reflect.DeepEqual(s.Observed, previous.Observed) || !reflect.DeepEqual(s.Accepted, previous.Accepted) {
				t.Fatal("upgrade changed identity, baseline or delivery evidence")
			}
		}
	}
	if subscriptions == 0 {
		t.Fatal("backup has no Lark subscriptions")
	}
	after := searchSnapshotDigests(t, output)
	if _, err := m.upgradeMemberNotificationsLocked(); err != nil || !reflect.DeepEqual(after, searchSnapshotDigests(t, output)) {
		t.Fatal("repeated upgrade changed candidate", err)
	}
	if err := m.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := offlineSearchManager(t, output)
	if err := reopened.loadPlatformNotificationsLocked(); err != nil {
		t.Fatal("reopen latest upgraded copy", err)
	}
	if err := reopened.store.Close(); err != nil {
		t.Fatal(err)
	}
	// Exercise the real startup constructor as well, with the production
	// adapter. Construction alone never starts provider workers or model turns.
	startup, err := newRoomManager(output, 7)
	if err != nil {
		t.Fatal("production startup on upgraded formal copy", err)
	}
	if startup.notifications.Source(lark.MessagesSourceID).Schema() != lark.SearchSchema {
		t.Fatal("production startup selected the old collector")
	}
	if err := startup.close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, searchSnapshotDigests(t, input)) {
		t.Fatal("immutable backup was changed")
	}
	t.Logf("snapshot rehearsal: files=%d rooms=%d upgraded=%d lark_subscriptions=%d pending=%d repeated_upgrade_stable=true reopen=true provider_calls=0", len(before), len(m.index.Rooms), count, subscriptions, pending)
}

func TestLarkSearchLatestStateRecoversLostDeliveryAcknowledgement(t *testing.T) {
	h := newLarkSearchHarness(t)
	id := notificationMemberIdentity(h.room.RoomIdentity, h.members[0])
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Target = inboxTarget(id)
	cfg.Private.Scope, cfg.Private.ChatIDs = "specified", []string{"oc_chat"}
	cfg.Groups.Enabled = false
	searchConfigHTTP(t, h, nil, cfg)
	h.clock.add(3 * time.Second)
	s := finishLarkSearch(t, h, 101)
	items := s.PendingItems()
	if len(items) != 1 {
		t.Fatal("expected one new search delivery")
	}
	// Deliver to the real member Inbox, then deliberately leave source pending:
	// exactly the durable boundary of a crash before source acknowledgement.
	h.svc.inboxMu.Lock()
	addr, err := h.svc.inboxLocked(t.Context(), id)
	h.svc.inboxMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	want, err := addr.Call[InboxReceipt](t.Context(), ReceiveInbox{MemberIdentity: id, Item: items[0]})
	if err != nil {
		t.Fatal(err)
	}
	root, roomRoot := h.m.store.dir, h.svc.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	latest := filepath.Join(t.TempDir(), "latest")
	copyLarkRollbackStore(t, root, latest)
	m := offlineSearchManager(t, latest)
	if err := m.loadPlatformNotificationsLocked(); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.currentLarkLocked(s.RoomIdentity)
	if err != nil || loaded == nil || !reflect.DeepEqual(loaded.Inbox, s.Inbox) || !reflect.DeepEqual(loaded.Accepted, s.Accepted) || loaded.Baseline != s.Baseline {
		t.Fatal("latest source evidence lost", err)
	}
	// Reopen the corresponding latest member Inbox and consumption state.
	rel, _ := filepath.Rel(root, roomRoot)
	store, err := OpenStore(filepath.Join(latest, rel), id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a := &AgentInbox{id: id, store: store, receipts: map[string]inboxIndex{}}
	if err := store.loadInbox(a, InboxLimits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.loadInboxConsumption(a); err != nil {
		t.Fatal(err)
	}
	item := loaded.PendingItems()[0]
	receipt, ok := a.receipts[item.DeliveryID]
	if !ok || receipt.seq != want.Seq || receipt.digest != inboxDigest(item) || a.latest != 1 {
		t.Fatal("latest Inbox cannot deduplicate source retry")
	}
	// Only acknowledge after proving the original durable receipt. The same
	// Acknowledge transition is used by the live delivery manager on its reply.
	if err := m.notifications.SaveLocked(loaded.Acknowledge(item.Key)); err != nil {
		t.Fatal(err)
	}
	resumed, err := m.currentLarkLocked(s.RoomIdentity)
	if err != nil || len(resumed.PendingItems()) != 0 || !reflect.DeepEqual(resumed.Accepted, s.Accepted) || resumed.Config.Target != s.Config.Target {
		t.Fatal("receipt-based recovery changed routing/evidence", err)
	}
}
