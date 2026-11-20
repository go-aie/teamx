package room

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
)

func (u *inputUpgrade) inventory(room RoomSnapshot) (map[string]inputUpgradeFile, error) {
	files := map[string]inputUpgradeFile{}
	add := func(name string) error {
		path := u.live(name)
		want, err := hashInputUpgradeFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil {
			files[name] = want
		}
		return err
	}
	walk := func(name string) error {
		path := u.live(name)
		return filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) && file == path {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".snapshot-") {
				return nil
			}
			return add(name + filepath.ToSlash(strings.TrimPrefix(file, path)))
		})
	}
	for _, name := range []string{upgradeRoomFile, "room/human-read.json"} {
		if err := add(name); err != nil {
			return nil, err
		}
	}
	if err := walk("room/lark-conversations"); err != nil {
		return nil, err
	}
	for _, m := range room.Members {
		root := "room/agents/" + m.MembershipID
		for _, name := range []string{"capacity.json", "input-state.json", "input-results", "followups/input-sources"} {
			if _, err := os.Lstat(u.live(root + "/" + name)); !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("%w: legacy member already has input stream state %s (%v)", ErrInvalid, name, err)
			}
		}
		for _, name := range []string{"agent.json", "collaboration.json", "followups", "actions", "task-events"} {
			if err := walk(root + "/" + name); err != nil {
				return nil, err
			}
		}
		if err := walk("room/inboxes/" + m.MembershipID); err != nil {
			return nil, err
		}
	}
	if err := u.inventoryNotices(room, add); err != nil {
		return nil, err
	}
	if _, ok := files[upgradeRoomFile]; !ok {
		return nil, fmt.Errorf("%w: legacy room.json disappeared", ErrInvalid)
	}
	return files, nil
}

type legacyInputEvent struct {
	member   MemberIdentity
	event    InboxTaskEvent
	inboxSeq uint64
	received time.Time
	consumed bool
}

type upgradedEvent struct {
	message RoomMessage
	inbox   legacyInputEvent
}

func (u *inputUpgrade) convert() error {
	old := &Store{dir: filepath.Join(u.backup(), "room"), roomID: u.store.roomID, lock: u.store.lock}
	state, err := old.loadRoom()
	if err != nil {
		return err
	}
	if err := old.checkReferenceRoom(state); err != nil {
		return err
	}
	var humanRead humanReadFile
	err = readSnapshot(filepath.Join(old.dir, "human-read.json"), &humanRead, maxPrivateFileBytes)
	if err == nil {
		err = validateHumanReadFile(humanRead, state, humanRead.OperatorID)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("legacy human-read.json: %w", err)
	}
	original := state
	state.Messages = slices.Clone(original.Messages)
	state.ReadProofs = nil
	next := &Store{dir: filepath.Join(u.stage(), "room"), roomID: u.store.roomID, lock: u.store.lock, snapshotWriter: snapshotWriter{ops: defaultSnapshotOps()}}
	// These files are copied only to validate the staged graph. Unchanged
	// business snapshots remain byte-identical when the new format is published.
	for name, want := range u.progress.Source {
		if (strings.Contains(name, "/followups/") && !strings.Contains(name, "/followups/events/")) ||
			strings.Contains(name, "/actions/") {
			if err := copyInputUpgradeFile(filepath.Join(u.backup(), name), filepath.Join(u.stage(), name), want); err != nil {
				return err
			}
		}
	}
	notices, err := u.readLegacyNotices()
	if err != nil {
		return err
	}
	queues := make([][]legacyInputEvent, 0, len(state.Members))
	for _, m := range state.Members {
		id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
		events, err := u.convertMember(old, next, id, original.LatestSeq)
		if err != nil {
			return fmt.Errorf("member %s legacy input: %w", id.MembershipID, err)
		}
		queues = append(queues, events)
	}
	events, err := appendUpgradedEvents(&state, queues, notices)
	if err != nil {
		return err
	}
	if err := u.convertNotices(notices, events); err != nil {
		return err
	}
	state.InputLog = &roomLogRoot{Version: 1, Segments: []roomLogSegment{}}
	if err := next.SaveRoom(state); err != nil {
		return err
	}
	reopened, err := next.LoadRoom()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(reopened.Members, original.Members) || !slices.EqualFunc(reopened.Messages[:len(original.Messages)], original.Messages, sameUpgradedMessage) {
		return fmt.Errorf("%w: converted Room history or member read checkpoints differ", ErrInvalid)
	}
	target := map[string]inputUpgradeFile{}
	err = filepath.WalkDir(u.stage(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		name := filepath.ToSlash(strings.TrimPrefix(path, u.stage()+string(filepath.Separator)))
		info, err := hashInputUpgradeFile(path)
		if err == nil && info != u.progress.Source[name] {
			target[name] = info
		}
		return err
	})
	if err != nil {
		return err
	}
	for name := range u.progress.Source {
		if (strings.HasPrefix(name, "room/inboxes/") || strings.Contains(name, "/followups/events/") ||
			strings.Contains(name, "/task-events/")) && target[name].Digest == "" {
			u.progress.Remove = append(u.progress.Remove, name)
		}
	}
	slices.Sort(u.progress.Remove)
	u.progress.Target = target
	return nil
}

func sameUpgradedMessage(a, b RoomMessage) bool {
	a.CreatedAt, b.CreatedAt = a.CreatedAt.UTC(), b.CreatedAt.UTC()
	return reflect.DeepEqual(a, b)
}

func (u *inputUpgrade) checkLegacySession(id MemberIdentity) error {
	path := filepath.Join(u.store.dir, "agents", id.MembershipID, "session.json")
	ref, exists, err := readDSHReference(path, id)
	if err != nil || !exists {
		return err
	}
	err = dsh.CheckReset(filepath.Join(filepath.Dir(path), ref.Directory))
	if err != nil && !errors.Is(err, dsh.ErrRecoveryUnsupported) && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("member %s previous Session: %w", id.MembershipID, err)
	}
	// A service can stop after writing the adapter reference but before
	// creating its private directory. Missing settlement still gates recovery.
	return nil
}

func (u *inputUpgrade) convertMember(old, next *Store, id MemberIdentity, roomLatest uint64) ([]legacyInputEvent, error) {
	inbox, entries, err := readLegacyInputInbox(old, id)
	if err != nil {
		return nil, err
	}
	if err := convertLegacyAgent(old, next, id, roomLatest, inbox); err != nil {
		return nil, err
	}
	events, err := legacyInputEvents(old, id, inbox, entries)
	if err != nil {
		return nil, err
	}
	if err := convertLegacyPlatform(old, next, id, inbox, entries); err != nil {
		return nil, err
	}
	return events, nil
}

func readLegacyInputInbox(old *Store, id MemberIdentity) (*AgentInbox, map[uint64]InboxEntry, error) {
	dir := filepath.Join(old.dir, "inboxes", id.MembershipID)
	a := &AgentInbox{id: id, dir: dir, receipts: map[string]inboxIndex{}, taskReceipts: map[string]inboxIndex{}}
	entries := map[uint64]InboxEntry{}
	entryAt := func(seq uint64) (InboxEntry, error) {
		if entry, ok := entries[seq]; ok {
			return entry, nil
		}
		return InboxEntry{}, fmt.Errorf("%w: missing legacy Inbox entry #%d", ErrInvalid, seq)
	}
	files, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	header, err := old.inboxHeaderLocked(id)
	if errors.Is(err, os.ErrNotExist) && len(files) == 0 {
		if err := old.readInboxConsumption(a, entryAt); err != nil {
			return nil, nil, err
		}
		return a, entries, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if header.LatestSeq == math.MaxUint64 {
		return nil, nil, ErrCapacity
	}
	a.limits, a.latest, a.expiredThrough = header.Limits, header.LatestSeq, header.ExpiredThrough
	for _, file := range files {
		name := file.Name()
		if name == "inbox.json" || name == "consumption.json" {
			continue
		}
		var seq uint64
		if _, err := fmt.Sscanf(name, "%020d.json", &seq); err != nil || seq == 0 || name != inboxEntryName(seq) {
			return nil, nil, fmt.Errorf("%w: legacy Inbox entry filename %s", ErrInvalid, name)
		}
		if seq == math.MaxUint64 {
			return nil, nil, ErrCapacity
		}
		if seq <= header.ExpiredThrough {
			continue // A durable cleanup boundary already released this body.
		}
		var e InboxEntry
		if err := readSnapshot(filepath.Join(dir, name), &e, header.Limits.EntryBytes); err != nil {
			return nil, nil, err
		}
		if err := e.validate(id); err != nil {
			return nil, nil, err
		}
		meta := e.metadata()
		if e.Location != nil || e.Seq != seq || a.receipts[meta.deliveryID].seq != 0 {
			return nil, nil, ErrInvalid
		}
		size, err := inboxEntrySize(e)
		if err != nil {
			return nil, nil, err
		}
		a.bytes += size
		a.latest = max(a.latest, e.Seq)
		a.receipts[meta.deliveryID] = e.index()
		entries[seq] = e
	}
	if a.latest-a.expiredThrough != uint64(len(entries)) {
		return nil, nil, fmt.Errorf("%w: legacy Inbox retained sequence has a gap", ErrInvalid)
	}
	if len(entries) > header.Limits.Entries || a.bytes > header.Limits.Bytes {
		return nil, nil, ErrCapacity
	}
	if err := old.readInboxConsumption(a, entryAt); err != nil {
		return nil, nil, err
	}
	return a, entries, nil
}

func convertLegacyAgent(old, next *Store, id MemberIdentity, roomLatest uint64, inbox *AgentInbox) error {
	var agent AgentSnapshot
	path := filepath.Join("agents", id.MembershipID, "agent.json")
	err := readSnapshot(filepath.Join(old.dir, path), &agent, maxPrivateFileBytes)
	if err == nil {
		if err := agent.validate(); err != nil {
			return fmt.Errorf("%w: legacy agent.json: %w", ErrInvalid, err)
		}
		if agent.MemberIdentity != id || agent.InputStateRef != "" || agent.LastOfferedUnreadSeq > roomLatest ||
			agent.LastOfferedInboxSeq > inbox.latest || (agent.Turn != nil && agent.Turn.SnapshotSeq > roomLatest) {
			return ErrInvalid
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	scheduling, err := old.loadScheduling(id)
	if err == nil {
		if scheduling.Used < agent.TurnNumber || scheduling.InboxRecovery != nil && scheduling.InboxRecovery.SnapshotSeq > inbox.latest {
			return fmt.Errorf("%w: legacy collaboration checkpoint", ErrInvalid)
		}
		if scheduling.InboxRecovery != nil {
			// Retire the attempt bound to the old fixed Inbox range.
			// Explicit pause/enable, turn budgets and continuation history persist.
			scheduling.InboxRecovery = nil
			if err := next.save(filepath.Join(next.dir, "agents", id.MembershipID, "collaboration.json"), scheduling, maxPrivateFileBytes); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if agent.Turn == nil {
		return nil
	}
	agent.LastOfferedInboxSeq, agent.LastOfferedInboxThrough = 0, 0
	agent.Turn.InboxStart, agent.Turn.InboxSnapshotSeq = 0, 0
	agent.InputStateRef = "input-state.json"
	if agent.Turn.Status == "running" {
		agent.Turn.Status = "interrupted"
		agent.Turn.Error = "service stopped before turn settlement; tool effects may have occurred"
	}
	if err := agent.validate(); err != nil {
		return err
	}
	return next.save(filepath.Join(next.dir, path), agent, maxPrivateFileBytes)
}

func legacyInputEvents(old *Store, id MemberIdentity, a *AgentInbox, entries map[uint64]InboxEntry) ([]legacyInputEvent, error) {
	events := []legacyInputEvent{}
	for _, entry := range entries {
		if entry.TaskEvent == nil {
			continue
		}
		if err := old.loadTaskEventLocked(id, *entry.TaskEvent); err != nil {
			return nil, err
		}
		if delivery, err := old.taskDeliveryLocked(entry); err == nil {
			if delivery.Seq != entry.Seq {
				return nil, ErrInvalid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		events = append(events, legacyInputEvent{id, *entry.TaskEvent, entry.Seq, entry.ReceivedAt, entry.Seq <= a.consumption.Through})
	}
	for _, delivered := range a.taskReceipts {
		if delivered.seq > a.expiredThrough {
			continue
		}
		var link inboxTaskLink
		if err := readSnapshot(old.taskEventPath(id, "links", taskLinkKey(delivered.seq)), &link, maxPrivateFileBytes); err != nil {
			return nil, err
		}
		// A reclaimed body no longer has ReceivedAt. The surviving event
		// occurrence time gives a deterministic position among other queues.
		events = append(events, legacyInputEvent{id, link.Event, delivered.seq, link.Event.CreatedAt, true})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].inboxSeq < events[j].inboxSeq })
	for i := 1; i < len(events); i++ {
		if events[i-1].inboxSeq == events[i].inboxSeq {
			return nil, ErrConflict
		}
	}
	return events, nil
}

func appendUpgradedEvents(room *RoomSnapshot, queues [][]legacyInputEvent, notices map[string]legacyOperationNotice) (map[string]map[MemberIdentity]upgradedEvent, error) {
	baseSeq := room.LatestSeq
	room.EventReceipts = map[string]roomEventReceipt{}
	byMember := map[string]map[MemberIdentity]upgradedEvent{}
	for {
		index := -1
		for i, queue := range queues {
			if len(queue) == 0 {
				continue
			}
			if index == -1 || queue[0].received.Before(queues[index][0].received) ||
				queue[0].received.Equal(queues[index][0].received) && queue[0].member.MembershipID < queues[index][0].member.MembershipID {
				index = i
			}
		}
		if index == -1 {
			break
		}
		old := queues[index][0]
		queues[index] = queues[index][1:]
		notice := notices[old.event.ResultRef]
		event := RoomEvent{ID: old.event.EventID, Kind: old.event.EventType, SourceSeq: old.event.SourceSeq, OperationID: old.event.OperationID,
			ResultVersion: old.event.ResultVersion, ResultRef: old.event.ResultRef, Actor: Author{Kind: "system", ID: "system"}, Targets: []string{}}
		if notice.actor.Kind != "" {
			event.Actor = notice.actor
		}
		if !old.consumed && !notice.pageOnly {
			event.Targets = append(event.Targets, old.member.MembershipID)
		}
		key := roomEventKey(event)
		receipt, exists := room.EventReceipts[key]
		var message RoomMessage
		if exists {
			message = room.Messages[receipt.Seq-room.ClearedThrough-1]
			before, after := *message.Event, event
			before.Targets, after.Targets = nil, nil
			if !reflect.DeepEqual(before, after) || !message.CreatedAt.Equal(old.event.CreatedAt) {
				return nil, fmt.Errorf("%w: duplicate legacy event %s differs across members", ErrConflict, old.event.EventID)
			}
			if _, duplicate := byMember[key][old.member]; duplicate {
				return nil, ErrConflict
			}
			message.Event.Targets = append(message.Event.Targets, event.Targets...)
		} else {
			if old.event.SourceSeq > baseSeq {
				return nil, fmt.Errorf("%w: legacy event %s has an unknown Room source", ErrInvalid, old.event.EventID)
			}
			if room.Revision == math.MaxUint64 || len(room.Messages) >= maxMessages || len(room.EventReceipts) >= maxRoomReceipts {
				return nil, fmt.Errorf("%w: legacy event %s exceeds Room bounds", ErrCapacity, old.event.EventID)
			}
			room.LatestSeq++
			room.Revision++
			summary := notice.summary
			if summary == "" {
				summary = "操作结果已更新"
			}
			message = RoomMessage{Type: "event", Event: &event, Seq: room.LatestSeq, ClientMessageID: key, Author: event.Actor,
				Text: summary, CreatedAt: old.event.CreatedAt.UTC()}
			room.Messages = append(room.Messages, message)
			byMember[key] = map[MemberIdentity]upgradedEvent{}
		}
		slices.Sort(message.Event.Targets)
		room.EventReceipts[key] = roomEventReceipt{message.Seq, roomEventDigest(message)}
		byMember[key][old.member] = upgradedEvent{message, old}
	}
	return byMember, nil
}

func convertLegacyPlatform(old, next *Store, id MemberIdentity, a *AgentInbox, entries map[uint64]InboxEntry) error {
	codecs, err := inputStorageCodecs(defaultNotificationAdapters(nil), old.notificationCapabilities)
	if err != nil {
		return err
	}
	owner := inputstream.Member{RoomID: id.RoomID, RoomInstance: id.RoomInstance, MembershipID: id.MembershipID}
	repo, err := inputstream.OpenRepository(next.dir, owner, codecs, inputPersistence{next})
	if err != nil {
		return err
	}
	parts := map[string][]inputstream.ImportEntry{}
	expected := map[inputstream.Location]InboxEntry{}
	pending := []inputstream.Location{}
	specs := map[string]inputstream.PartitionSpec{}
	for _, codec := range codecs {
		d := codec.Descriptor()
		specs[d.Provider] = d.Spec(owner)
		// The expired prefix no longer identifies its providers. Carry its
		// boundary to each supported partition to avoid reusing any position.
		if a.expiredThrough != 0 {
			parts[d.Provider] = nil
		}
	}
	for _, seq := range slices.Sorted(maps.Keys(entries)) {
		entry := entries[seq]
		if entry.TaskEvent != nil {
			continue
		}
		provider := entry.Item.Source.Provider
		cap := old.notificationCapabilities(provider).Input
		spec, ok := specs[provider]
		if cap == nil || !ok {
			return fmt.Errorf("%w: legacy Inbox provider %s", ErrVersion, provider)
		}
		req, err := cap.EncodeStoredInput(entry.Item)
		if err != nil {
			return fmt.Errorf("legacy Inbox #%d codec: %w", seq, err)
		}
		meta := entry.metadata()
		if req.DeliveryDigest != hex.EncodeToString(meta.digest[:]) {
			return fmt.Errorf("%w: delivery digest differs for legacy Inbox #%d", ErrInvalid, seq)
		}
		req.Seq, req.ReceivedAt, req.SourceRef = seq, entry.ReceivedAt, inputSourceRef(entry)
		item := inputstream.ImportEntry{AppendRequest: req, Consumed: seq <= a.consumption.Through, Excluded: a.consumption.skipped(seq)}
		parts[provider] = append(parts[provider], item)
		loc := inputstream.Location{Partition: spec.Ref, Seq: seq}
		expected[loc] = entry
		if !item.Consumed && !item.Excluded {
			pending = append(pending, loc)
		}
	}
	for _, provider := range slices.Sorted(maps.Keys(parts)) {
		items := parts[provider]
		latest := a.expiredThrough
		if len(items) > 0 {
			latest = items[len(items)-1].Seq
		}
		if err := repo.ImportPartition(provider, a.expiredThrough, latest, items); err != nil {
			return fmt.Errorf("%s target partition: %w", provider, err)
		}
	}
	repo, err = inputstream.OpenRepository(next.dir, owner, codecs, inputPersistence{next})
	if err != nil {
		return err
	}
	inputstream.SortedLocations(pending)
	actual, err := repo.PendingLocations(nil)
	if err != nil || !slices.Equal(actual, pending) {
		return fmt.Errorf("%w: pending platform positions differ: got %v, expected %v (%v)", ErrInvalid, actual, pending, err)
	}
	for provider, snap := range repo.ExistingSnapshots() {
		locations, err := repo.Scan(snap)
		if err != nil {
			return err
		}
		for _, loc := range locations {
			entry, ok := expected[loc]
			rec, err := repo.Read(snap, loc)
			if err != nil || !ok || !rec.ReceivedAt.Equal(entry.ReceivedAt) || !rec.CreatedAt.Equal(entry.Item.CreatedAt) {
				return fmt.Errorf("%w: converted platform position %v (%v)", ErrInvalid, loc, err)
			}
			item, err := old.notificationCapabilities(provider).Input.DecodeStoredInput(rec)
			entry.Item.CreatedAt = entry.Item.CreatedAt.UTC()
			if err != nil || !reflect.DeepEqual(item, entry.Item) {
				return fmt.Errorf("%w: converted platform payload %v (%v)", ErrInvalid, loc, err)
			}
			ref, err := repo.DeliverySourceRef(loc, rec.Channel, rec.DeliveryID)
			if err != nil || ref != inputSourceRef(entry) {
				return fmt.Errorf("%w: converted source identity %v (%v)", ErrInvalid, loc, err)
			}
			delete(expected, loc)
		}
	}
	if len(expected) != 0 {
		return fmt.Errorf("%w: converted partition is missing retained entries", ErrInvalid)
	}
	next.platformInputs = map[string]*platformInputStorage{id.MembershipID: {store: next, id: id, repository: repo}}
	return convertLegacyLinks(old, next, id, specs, a.expiredThrough, entries)
}

func convertLegacyLinks(old, next *Store, id MemberIdentity, specs map[string]inputstream.PartitionSpec, expired uint64, entries map[uint64]InboxEntry) error {
	eventAt := func(seq uint64) (followupEvent, error) {
		if seq <= expired {
			return old.expiredFollowupEventLocked(id, seq)
		}
		entry, ok := entries[seq]
		if !ok {
			return followupEvent{}, fmt.Errorf("%w: legacy source link has no Inbox entry #%d", ErrInvalid, seq)
		}
		return old.loadFollowupEventLocked(entry)
	}
	return old.visitLegacyFollowupEvents(id, eventAt, func(link followupEvent) error {
		spec, ok := specs[link.Source.Provider]
		if !ok || link.Location != nil || !slices.Contains(spec.Channels, link.Source.Channel) {
			return ErrInvalid
		}
		loc := inputstream.Location{Partition: spec.Ref, Seq: link.Seq}
		link.Location = &loc
		p := next.platformInputs[id.MembershipID]
		if _, err := p.repository.DeliverySourceRef(loc, link.Source.Channel, link.DeliveryID); errors.Is(err, inputstream.ErrExpired) {
			link.ReclaimedSourceProof = reclaimedInputSourceProof(link)
		} else if err != nil {
			return err
		}
		path := filepath.Join(next.followupRoot(id), "input-sources", link.SourceRef+".json")
		if err := next.save(path, link, followupBytes); err != nil {
			return err
		}
		got, _, err := next.inputSourceContentLocked(id, link.SourceRef)
		if err != nil || got.ObjectRef != link.ObjectRef || got.TargetRef != link.TargetRef {
			return fmt.Errorf("%w: converted source link %s (%v)", ErrInvalid, link.SourceRef, err)
		}
		return nil
	})
}
