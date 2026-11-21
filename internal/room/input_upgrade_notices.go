package room

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

var upgradeNoticeDirectories = []string{"platform-connect-notifications", "platform-selection-notifications"}

func upgradeOperationDirectory(noticeDirectory string) string {
	if noticeDirectory == "platform-connect-notifications" {
		return "platform-connect"
	}
	return "platform-selections"
}

type legacyOperationNotice struct {
	path     string
	saved    platformOperationNotification
	actor    Author
	summary  string
	pageOnly bool
}

func (u *inputUpgrade) inventoryNotices(room RoomSnapshot, add func(string) error) error {
	if u.options.catalog == "" {
		return nil
	}
	active := map[string]string{}
	for _, member := range room.Members {
		active[member.MembershipID] = member.AgentID
	}
	for _, dir := range upgradeNoticeDirectories {
		files, err := os.ReadDir(filepath.Join(u.options.catalog, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".json") || strings.HasPrefix(file.Name(), ".snapshot-") {
				continue
			}
			path := "catalog/" + dir + "/" + file.Name()
			var notice platformOperationNotification
			if err := readSnapshot(u.live(path), &notice, maxPrivateFileBytes); err != nil {
				return err
			}
			if notice.RoomIdentity != room.RoomIdentity || active[notice.MembershipID] != notice.AgentID || notice.Event == nil {
				continue
			}
			if !safeMembershipID(notice.OperationID) || file.Name() != notice.OperationID+".json" || notice.Schema != 1 ||
				!notice.Event.valid() || notice.Event.OperationID != notice.OperationID || !validRoomOperationIntent(notice) {
				return fmt.Errorf("%w: legacy operation intent %s", ErrInvalid, path)
			}
			if err := add(path); err != nil {
				return err
			}
			if err := add("catalog/" + upgradeOperationDirectory(dir) + "/" + file.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (u *inputUpgrade) readLegacyNotices() (map[string]legacyOperationNotice, error) {
	notices := map[string]legacyOperationNotice{}
	for path := range u.progress.Source {
		parts := strings.Split(path, "/")
		if parts[0] != "catalog" || !slices.Contains(upgradeNoticeDirectories, parts[1]) {
			continue
		}
		notice := legacyOperationNotice{path: path}
		n := &notice.saved
		if err := readSnapshot(filepath.Join(u.backup(), path), n, maxPrivateFileBytes); err != nil {
			return nil, err
		}
		catalog := &roomManager{platformCatalog: u.store.catalog, store: &Store{catalog: u.store.catalog, dir: filepath.Join(u.backup(), "catalog"), lock: u.store.lock}}
		status := ""
		if parts[1] == "platform-connect-notifications" {
			op, err := catalog.readPlatformConnectLocked(n.OperationID)
			if err != nil {
				return nil, err
			}
			if op.MemberIdentity != n.MemberIdentity || op.ID != n.OperationID ||
				!connectTerminal(op.Status) || !reflect.DeepEqual(connectResultEvent(op), *n.Event) || n.HandledVersion > op.ResultVersion {
				return nil, fmt.Errorf("%w: legacy connection result %s", ErrInvalid, path)
			}
			notice.summary, status = "连接结果已更新", op.Status
		} else {
			op, err := catalog.readPlatformSelectionLocked(n.OperationID)
			if err != nil {
				return nil, err
			}
			if op.MemberIdentity != n.MemberIdentity || op.ID != n.OperationID ||
				!reflect.DeepEqual(selectionResultEvent(op), *n.Event) || n.HandledVersion > op.ResultVersion {
				return nil, fmt.Errorf("%w: legacy selection result %s", ErrInvalid, path)
			}
			notice.actor, notice.summary, status = op.ResultActor, "选择结果已更新", op.Status
		}
		// A later pause affects future notifications. A durable delivery stays
		// pending for its original member unless the result itself was cancelled.
		notice.pageOnly = status == "cancelled" || status == "expired"
		key := n.Event.ResultRef
		if _, exists := notices[key]; exists {
			return nil, ErrConflict
		}
		notices[key] = notice
	}
	return notices, nil
}

func (u *inputUpgrade) convertNotices(notices map[string]legacyOperationNotice, events map[string]map[MemberIdentity]upgradedEvent) error {
	for _, notice := range notices {
		n := notice.saved
		key := roomEventKey(RoomEvent{ID: n.Event.EventID, ResultVersion: n.Event.ResultVersion})
		result, exists := events[key][n.MemberIdentity]
		if !exists {
			if n.Receipt != nil {
				return fmt.Errorf("%w: legacy operation receipt has no matching delivery %s", ErrInvalid, notice.path)
			}
			continue // Intent saved before delivery. The normal owner may publish it.
		}
		if !reflect.DeepEqual(*n.Event, result.inbox.event) || n.RoomEvent != nil {
			return ErrInvalid
		}
		if n.Receipt != nil {
			e := InboxEntry{MemberIdentity: n.MemberIdentity, Type: InboxTaskEventType, TaskEvent: n.Event}
			if n.Receipt.MemberIdentity != n.MemberIdentity || n.Receipt.Seq != result.inbox.inboxSeq || n.Receipt.DeliveryID != e.metadata().deliveryID {
				return fmt.Errorf("%w: legacy operation delivery receipt %s", ErrInvalid, notice.path)
			}
		}
		event := *result.message.Event
		// A notice is member-scoped even if historical event dedup merged
		// several deliveries into the Room audience.
		event.Targets = []string{}
		if slices.Contains(result.message.Event.Targets, n.MembershipID) {
			event.Targets = append(event.Targets, n.MembershipID)
		}
		n.Receipt, n.ErrorCode, n.RoomEvent, n.RoomSummary = nil, "", &event, result.message.Text
		n.RoomReceipt = &PostReceipt{RoomIdentity: n.RoomIdentity, Seq: result.message.Seq, ClientMessageID: key}
		n.RoomConsumed = result.inbox.consumed
		if !validRoomOperationIntent(n) {
			return ErrInvalid
		}
		if err := u.store.save(filepath.Join(u.stage(), notice.path), n, maxPrivateFileBytes); err != nil {
			return err
		}
	}
	return nil
}
