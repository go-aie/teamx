package room

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/inputstream"
)

func roomStreamRef(id MemberIdentity) string {
	return inputstream.Ref("stream_", 1, id.RoomIdentity, id.MembershipID, "room")
}
func roomPartitionRef(id RoomIdentity) string { return inputstream.Ref("partition_", id, "room") }

func (s *inputSession) addRoomCatalog(c *inputstream.Catalog) error {
	if !s.unified {
		return nil
	}
	state, err := s.storage.store.LoadRoom()
	if err != nil {
		return err
	}
	id := s.storage.id
	var member *MemberState
	for i := range state.Members {
		if state.Members[i].MembershipID == id.MembershipID {
			member = &state.Members[i]
			break
		}
	}
	if member == nil {
		return ErrIdentity
	}
	records := []inputstream.StoredRecord{}
	human, event := false, false
	for _, m := range state.Messages {
		if m.Seq <= member.ReadThrough || !roomDeliverable(m, id) {
			continue
		}
		records = append(records, inputstream.StoredRecord{Location: inputstream.Location{Partition: roomPartitionRef(id.RoomIdentity), Seq: m.Seq}, ReceivedAt: m.CreatedAt})
		human = human || m.Author.Kind == "human" && m.Event == nil
		event = event || m.Event != nil
	}
	hints := []string{}
	if human {
		hints = append(hints, "有待交付的人类消息")
	}
	if event {
		hints = append(hints, "有待交付的交互事件")
	}
	summary := inputstream.StreamSummary{Ref: roomStreamRef(id), Kind: "room", Label: "当前 Room", Hint: strings.Join(hints, "；")}
	return c.AddRoom(inputstream.Snapshot{Partition: roomPartitionRef(id.RoomIdentity), Revision: state.Revision, Through: state.LatestSeq, Reclaimed: state.ClearedThrough}, summary, records)
}
func (s *inputSession) reserveRoom(ctx context.Context, q inputstream.ReadRequest, parent *inputstream.ReadAttempt) (inputstream.ReadAttempt, error) {
	id := s.storage.id
	limit := q.Limit
	var snapshot *uint64
	if parent == nil {
		state, err := s.inputState(ctx)
		if err != nil {
			return inputstream.ReadAttempt{}, err
		}
		if _, simple := state.Calls[q.ID]; simple {
			for i := len(state.Attempts) - 1; i >= 0; i-- {
				a := state.Attempts[i]
				if a.Kind == "room" && a.Phase == "settled" {
					if a.HasMore {
						through := a.Snapshot.Through
						snapshot = &through
					}
					break
				}
			}
		}
	}
	if parent != nil {
		limit = parent.Remaining
		snapshot = &parent.Snapshot.Through
	}
	if limit == 0 {
		limit = 20
	}
	page, err := s.service.room.Call[ReadPage](ctx, ReadMessages{RoomIdentity: id.RoomIdentity, Caller: agentCallerIdentity(id), Mode: ReadUnread, Peek: true, SnapshotSeq: snapshot, Limit: limit})
	if err != nil {
		return inputstream.ReadAttempt{}, err
	}
	return s.reserveRoomPage(ctx, q, page)
}

// The Room scan precedes the member commit. Recheck the native publication
// under the same Store lock used by SaveRoom's protection check: either clear
// commits first and no page is reserved, or the durable page blocks clear.
func (s *inputSession) reserveRoomPage(ctx context.Context, q inputstream.ReadRequest, page ReadPage) (inputstream.ReadAttempt, error) {
	id := s.storage.id
	entries := []inputstream.Location{}
	for _, m := range page.Messages {
		entries = append(entries, inputstream.Location{Partition: roomPartitionRef(id.RoomIdentity), Seq: m.Seq})
	}
	var a inputstream.ReadAttempt
	err := s.coordinate(ctx, func(r inputstream.ExecutionStorage) error {
		var native RoomSnapshot
		if err := readSnapshot(filepath.Join(s.storage.store.dir, "room.json"), &native, maxRoomFileBytes); err != nil {
			return err
		}
		if native.RoomIdentity != id.RoomIdentity || page.RoomIdentity != id.RoomIdentity {
			return ErrIdentity
		}
		if page.FromExclusive < native.ClearedThrough || page.SnapshotSeq > native.LatestSeq {
			return ErrReadExpired
		}
		member := slices.IndexFunc(native.Members, func(m MemberState) bool { return m.AgentID == id.AgentID && m.MembershipID == id.MembershipID })
		if member < 0 {
			return ErrIdentity
		}
		if inputstream.ValidateCheckpoint(native.Members[member].ReadThrough, page.FromExclusive, native.ClearedThrough) != nil {
			return ErrReadExpired
		}
		var err error
		a, err = r.ReserveOwned(s.execution, q, roomStreamRef(id), inputstream.OwnedPage{Snapshot: inputstream.Snapshot{Partition: roomPartitionRef(id.RoomIdentity), Revision: page.Revision, Through: page.SnapshotSeq, Reclaimed: page.ClearedThrough}, From: page.FromExclusive, Through: page.ScannedThrough, Entries: entries, Remaining: page.Remaining, HasMore: page.ScannedThrough < page.SnapshotSeq})
		return err
	})
	return a, err
}
func (s *inputSession) roomEntries(a inputstream.ReadAttempt) ([]RoomMessage, error) {
	state, err := s.storage.store.LoadRoom()
	if err != nil {
		return nil, err
	}
	if a.From < state.ClearedThrough {
		return nil, ErrReadExpired
	}
	out := []RoomMessage{}
	for _, l := range a.Entries {
		if l.Partition != roomPartitionRef(state.RoomIdentity) || l.Seq <= state.ClearedThrough || l.Seq > state.LatestSeq {
			return nil, ErrIdentity
		}
		m := state.Messages[l.Seq-state.ClearedThrough-1]
		if !roomDeliverable(m, s.storage.id) {
			return nil, ErrIdentity
		}
		out = append(out, m)
	}
	return out, nil
}
func (s *inputSession) prepareRoomPage(ctx context.Context, a inputstream.ReadAttempt, render inputPageRenderer) error {
	entries, err := s.roomEntries(a)
	if err != nil {
		return err
	}
	page := inputPageContent{room: true, roomEntries: []agentRoomMessage{}}
	room, err := s.storage.store.LoadRoom()
	if err != nil {
		return err
	}
	for _, m := range room.Messages {
		if m.Seq > a.Through && m.Seq <= a.Snapshot.Through && roomDeliverable(m, s.storage.id) {
			page.remaining++
		}
	}
	entries = s.service.labelMessage(ReadPage{Messages: entries}).Messages
	notices := s.roomPageNotices(entries, a)
	sources := map[uint64]string{}
	for _, m := range entries {
		page.roomEntries = append(page.roomEntries, projectRoomMessage(m))
		sources[m.Seq] = fmt.Sprintf("Room #%d", m.Seq)
	}
	if len(entries) == 0 {
		page.stop = "stream_empty"
	}
	state, err := s.inputState(ctx)
	if err != nil {
		return err
	}
	return s.prepareInputMaterials(ctx, a, page, notices, len(entries), sources, state, render)
}

// ConfirmInputRead is internal trusted delivery data, never a tool argument.
// It revalidates the exact scan after restart or receipt expiry, then commits
// the sole Room cursor and same-page proof in the native Actor snapshot.
type ConfirmInputRead struct {
	MemberIdentity
	Attempt inputstream.ReadAttempt
}

func (r *Room) ConfirmInput(ctx actorx.Context, msg ConfirmInputRead, reply actorx.Reply[ReadConfirmation]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	i, err := r.member(msg.AgentID, msg.MembershipID)
	if err != nil {
		return err
	}
	a := msg.Attempt
	proof := a.Proof()
	if a.Kind != "room" || a.Snapshot.Partition != roomPartitionRef(msg.RoomIdentity) || a.Stream != roomStreamRef(msg.MemberIdentity) || a.Phase != "awaiting_confirmation" {
		return ErrIdentity
	}
	if p, ok := r.state.ReadProofs[msg.MembershipID]; ok && p.Confirmation.ReadReceipt == proof {
		reply.Complete(p.Confirmation, nil)
		return nil
	}
	if err := inputstream.ValidateReadRange(a.Snapshot.Partition, a.From, a.Through, a.Snapshot.Through, a.Entries); err != nil {
		return errors.Join(ErrConflict, err)
	}
	if a.From < r.state.ClearedThrough || inputstream.ValidateCheckpoint(r.state.Members[i].ReadThrough, a.From, r.state.ClearedThrough) != nil || a.Through > r.state.LatestSeq || a.Through > a.Snapshot.Through {
		return ErrReadExpired
	}
	expected := []uint64{}
	for _, m := range r.state.Messages[a.From-r.state.ClearedThrough : a.Through-r.state.ClearedThrough] {
		if roomDeliverable(m, msg.MemberIdentity) {
			expected = append(expected, m.Seq)
		}
	}
	actual := []uint64{}
	for _, l := range a.Entries {
		if l.Partition != a.Snapshot.Partition {
			return ErrIdentity
		}
		actual = append(actual, l.Seq)
	}
	if !slices.Equal(expected, actual) {
		return ErrConflict
	}
	if a.Through == a.From {
		reply.Complete(ReadConfirmation{MemberIdentity: msg.MemberIdentity, ReadReceipt: proof, ReadThrough: a.From, Revision: r.state.Revision}, nil)
		return nil
	}
	next := r.candidate()
	next.Members[i].ReadThrough = a.Through
	count, _ := r.unread(next.Members[i])
	result := ReadConfirmation{MemberIdentity: msg.MemberIdentity, ReadReceipt: proof, ReadThrough: a.Through, UnreadCount: count, Revision: next.Revision + 1}
	p := roomReadProof{Confirmation: result, From: a.From, Snapshot: a.Snapshot.Through, Entries: actual}
	p.Digest = p.digest()
	if next.ReadProofs == nil {
		next.ReadProofs = map[string]roomReadProof{}
	}
	next.ReadProofs[msg.MembershipID] = p
	return commitRoom(r, ctx, next, reply, result, func() { delete(r.pages, msg.MembershipID); r.confirmed[msg.MembershipID] = result })
}

func roomInputNotices(info ServiceInfo, entries []RoomMessage) []materialNotice {
	out := []materialNotice{}
	for _, m := range entries {
		if len(m.Attachments) > 0 {
			found := notificationImageReferences{References: len(m.Attachments)}
			for i, a := range m.Attachments {
				found.Images = append(found.Images, notificationImage{Path: a.ID, Identity: a.ID, Supported: true, Positions: []int{i + 1}})
			}
			out = append(out, materialNotice{request: attachmentRequest{ServiceInfo: info, Seq: m.Seq, Local: true}, author: m.Author.ID, created: m.CreatedAt, text: m.Text, images: found, attachments: slices.Clone(m.Attachments)})
		}
		if m.Source == nil {
			continue
		}
		found := findNotificationImages(m.Text, *m.Source)
		if found.References == 0 {
			continue
		}
		out = append(out, materialNotice{request: attachmentRequest{ServiceInfo: info, Seq: m.Seq}, source: *m.Source, author: m.Author.ID, created: m.CreatedAt, text: m.Text, images: found})
	}
	return out
}

func (s *inputSession) roomPageNotices(entries []RoomMessage, a inputstream.ReadAttempt) []materialNotice {
	notices := roomInputNotices(s.service.info, entries)
	for i := range notices {
		if notices[i].request.Local {
			page := a
			page.Entries = slices.Clone(a.Entries)
			notices[i].request.Page = &roomAttachmentPage{session: s, attempt: page}
		}
	}
	return notices
}
