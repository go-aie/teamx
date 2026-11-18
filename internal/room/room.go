//go:generate go run github.com/go-aie/actorx/cmd/actorx-gen

package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/inputstream"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/go-aie/actorx"
)

const (
	maxPageMessages = 100
	maxPageBytes    = 1 << 20
	readReceiptTTL  = 30 * time.Second
)

type pendingRead struct {
	id       string
	from     uint64
	through  uint64
	expires  time.Time
	snapshot uint64
	entries  []uint64
}

// Room owns committed business state. Disk writes see immutable candidates;
// only serial task completions install them and publish success replies.
//
//actorx:actor
type Room struct {
	observeCatalog  func() // membership shape changes; receipts and attachment do not affect directory
	observeMembers  func()
	observeMessages func() // optional signal after installation; no query or error result

	state      RoomSnapshot
	save       func(RoomSnapshot) error
	targets    map[string]actorx.Addr
	pages      map[string]pendingRead
	confirmed  map[string]ReadConfirmation
	confirming string // Pins this member's receipt while its cursor is committing.
	committing bool
	writeErr   error

	now func() time.Time

	owner      actorx.Context
	pending    map[string]UnreadChanged
	notifying  bool
	nextNotify time.Time
	nextSync   time.Time

	maintenanceInterval time.Duration
	notificationTimeout time.Duration
}

// NewRoom loads an already initialized store before Spawn. The caller owns the
// Store and must wait for Room exit before closing it. Assembly reattaches
// recovered members separately; loading never resets member cursors.
func NewRoom(store *Store) (*Room, error) {
	state, err := store.LoadRoom()
	if err != nil {
		return nil, err
	}
	confirmed := make(map[string]ReadConfirmation)
	for id, proof := range state.ReadProofs {
		confirmed[id] = proof.Confirmation
	}
	return &Room{
		state: state, save: store.SaveRoom, now: time.Now,
		targets: make(map[string]actorx.Addr), pages: make(map[string]pendingRead),
		confirmed: confirmed,
	}, nil
}

func (r *Room) Started(ctx actorx.Context, _ actorx.Started) error {
	if r.save == nil || r.now == nil || r.targets == nil || r.pages == nil || r.confirmed == nil {
		return fmt.Errorf("%w: construct Room with NewRoom", ErrRequest)
	}
	if err := r.state.validate(); err != nil {
		return err
	}
	r.owner = ctx.WithContext(context.WithoutCancel(ctx))
	r.pending = make(map[string]UnreadChanged)
	if r.maintenanceInterval == 0 {
		r.maintenanceInterval = time.Second
	}
	return r.startMaintenance()
}

func (r *Room) Add(ctx actorx.Context, msg AddMember, reply actorx.Reply[MemberInfo]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	if err := validateMemberHeader(formatVersion, msg.MemberIdentity); err != nil || msg.Target.IsZero() {
		return fmt.Errorf("%w: invalid member identity or target", ErrRequest)
	}
	for _, m := range r.state.Members {
		if m.AgentID == msg.AgentID || m.MembershipID == msg.MembershipID {
			if m.AgentID == msg.AgentID && m.MembershipID == msg.MembershipID &&
				r.targets[m.MembershipID] == msg.Target && m.Runner == msg.Runner {
				reply.Complete(r.memberInfo(m), nil)
				return nil
			}
			return ErrConflict
		}
	}
	if r.state.IsDirect {
		return fmt.Errorf("%w: direct conversation membership is fixed", ErrConflict)
	}
	if len(r.state.Members) >= maxMembers {
		return ErrCapacity
	}
	m := MemberState{AgentID: msg.AgentID, MembershipID: msg.MembershipID, ReadThrough: r.state.LatestSeq, Runner: msg.Runner}
	next := r.candidate()
	next.Members = append(next.Members, m)
	if err := next.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrRequest, err)
	}
	result := MemberInfo{MemberState: m, Attached: true}
	return commitRoom(r, ctx, next, reply, result, func() {
		r.targets[m.MembershipID] = msg.Target
		if r.observeCatalog != nil {
			r.observeCatalog()
		}
	})
}

func (r *Room) Remove(ctx actorx.Context, msg RemoveMember, reply actorx.Reply[MemberInfo]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	i, err := r.member(msg.AgentID, msg.MembershipID)
	if err != nil {
		return err
	}
	if r.state.IsDirect {
		return fmt.Errorf("%w: delete the direct conversation instead of removing its agent", ErrConflict)
	}
	m := r.state.Members[i]
	result := r.memberInfo(m)
	result.Attached = false
	next := r.candidate()
	next.Members = slices.Delete(next.Members, i, i+1)
	delete(next.ReadProofs, m.MembershipID)
	return commitRoom(r, ctx, next, reply, result, func() {
		delete(r.targets, m.MembershipID)
		delete(r.pending, m.MembershipID)
		delete(r.pages, m.MembershipID)
		delete(r.confirmed, m.MembershipID)
		if r.observeCatalog != nil {
			r.observeCatalog()
		}
	})
}

func (r *Room) Attach(_ actorx.Context, msg AttachMember) (MemberInfo, error) {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return MemberInfo{}, err
	}
	i, err := r.member(msg.AgentID, msg.MembershipID)
	if err != nil {
		return MemberInfo{}, err
	}
	if msg.Target.IsZero() {
		return MemberInfo{}, ErrRequest
	}
	if old := r.targets[msg.MembershipID]; !old.IsZero() && old != msg.Target {
		return MemberInfo{}, ErrConflict
	}
	changed := r.targets[msg.MembershipID] != msg.Target
	r.targets[msg.MembershipID] = msg.Target
	if changed && r.observeMembers != nil {
		r.observeMembers()
	}
	r.pending[msg.MembershipID] = r.notification(r.state.Members[i])
	r.dispatchNotifications()
	return r.memberInfo(r.state.Members[i]), nil
}

func (r *Room) List(_ actorx.Context, msg ListMembers) (MemberList, error) {
	if err := r.identity(msg.RoomIdentity); err != nil {
		return MemberList{}, err
	}
	result := MemberList{
		RoomIdentity: r.state.RoomIdentity, IsDirect: r.state.IsDirect, LatestSeq: r.state.LatestSeq,
		Revision: r.state.Revision, Committing: r.committing,
		Members: make([]MemberInfo, 0, len(r.state.Members)),
	}
	if r.writeErr != nil {
		result.WriteError = r.writeErr.Error()
	}
	for _, m := range r.state.Members {
		result.Members = append(result.Members, r.memberInfo(m))
	}
	return result, nil
}

func (r *Room) Post(ctx actorx.Context, msg PostMessage, reply actorx.Reply[PostReceipt]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	if _, err := r.caller(msg.Caller); err != nil {
		return err
	}
	if !validMentions(msg.Text, msg.Mentions) {
		return ErrRequest
	}
	if !validText(msg.ClientMessageID) || !validText(msg.Text) || len(msg.Text) > maxTextBytes || msg.ReplyTo > r.state.LatestSeq {
		return ErrRequest
	}
	if receipt, ok := r.state.FollowupNoticeReceipts[msg.ClientMessageID]; ok {
		if receipt.Author != msg.Caller.Author || receipt.MembershipID != msg.Caller.MembershipID || receipt.Digest != noticeDigest(msg.Text) || msg.ReplyTo != 0 {
			return ErrConflict
		}
		reply.Complete(PostReceipt{RoomIdentity: r.state.RoomIdentity, Seq: receipt.Seq, ClientMessageID: msg.ClientMessageID}, nil)
		return nil
	}
	if msg.FollowupNotice && (msg.Caller.Kind != "agent" || msg.ReplyTo != 0) {
		return ErrIdentity
	}
	if msg.FollowupNotice && len(r.state.FollowupNoticeReceipts) >= maxIntegrationReceipts {
		return ErrCapacity
	}
	bytes := 0
	for _, m := range r.state.Messages {
		bytes += len(m.Text)
		if m.Author == msg.Caller.Author && m.ClientMessageID == msg.ClientMessageID {
			if m.Text != msg.Text || m.ReplyTo != msg.ReplyTo || msg.FollowupNotice || !msg.inferMentions && !slices.Equal(m.Mentions, msg.Mentions) {
				return ErrConflict
			}
			reply.Complete(PostReceipt{RoomIdentity: r.state.RoomIdentity, Seq: m.Seq, ClientMessageID: m.ClientMessageID}, nil)
			return nil
		}
	}
	for _, mention := range msg.Mentions {
		if mention.Kind == "agent" {
			if _, err := r.member(mention.ID, mention.MembershipID); err != nil {
				return err
			}
		}
	}
	if msg.ReplyTo != 0 && msg.ReplyTo <= r.state.ClearedThrough {
		return ErrRequest
	}
	if r.state.LatestSeq == math.MaxUint64 || len(r.state.Messages) >= maxMessages || bytes+len(msg.Text) > maxHistoryBytes {
		return ErrCapacity
	}
	next := r.candidate()
	next.LatestSeq++
	next.Messages = append(next.Messages, RoomMessage{
		Seq: next.LatestSeq, ClientMessageID: msg.ClientMessageID, Author: msg.Caller.Author,
		Text: msg.Text, ReplyTo: msg.ReplyTo, CreatedAt: r.now().UTC(), AuthorMembershipID: msg.Caller.MembershipID, Mentions: slices.Clone(msg.Mentions),
	})
	if msg.FollowupNotice {
		if next.FollowupNoticeReceipts == nil {
			next.FollowupNoticeReceipts = map[string]followupNoticeReceipt{}
		}
		next.FollowupNoticeReceipts[msg.ClientMessageID] = followupNoticeReceipt{Seq: next.LatestSeq, Author: msg.Caller.Author, MembershipID: msg.Caller.MembershipID, Digest: noticeDigest(msg.Text)}
	}
	result := PostReceipt{RoomIdentity: next.RoomIdentity, Seq: next.LatestSeq, ClientMessageID: msg.ClientMessageID}
	return commitRoom(r, ctx, next, reply, result, nil)
}

func (r *Room) Clear(ctx actorx.Context, msg ClearMessages, reply actorx.Reply[ClearReceipt]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	if _, err := r.caller(msg.Caller); err != nil || msg.Caller.Kind != "human" {
		return ErrIdentity
	}
	if msg.ThroughSeq > r.state.LatestSeq {
		return ErrRequest
	}
	result := ClearReceipt{RoomIdentity: r.state.RoomIdentity, ClearedThrough: max(msg.ThroughSeq, r.state.ClearedThrough)}
	if msg.ThroughSeq <= r.state.ClearedThrough {
		reply.Complete(result, nil)
		return nil
	}
	for _, p := range r.pages {
		if r.state.InputLog != nil && r.now().Before(p.expires) && p.from < msg.ThroughSeq {
			return ErrBusy
		}
	}
	if r.state.InputLog != nil {
		records := []inputstream.StoredRecord{}
		sizes := map[uint64]int{}
		facts := inputstream.ReleaseSet{Revision: inputstream.Ref("clear_", r.state.Revision, msg.ThroughSeq), Records: map[uint64]inputstream.ReleaseFacts{}}
		partition := roomPartitionRef(r.state.RoomIdentity)
		for _, m := range r.state.Messages {
			if m.Seq > msg.ThroughSeq {
				break
			}
			b, _ := json.Marshal(m)
			sizes[m.Seq] = len(b) + 1
			records = append(records, inputstream.StoredRecord{Location: inputstream.Location{Partition: partition, Seq: m.Seq}, CreatedAt: m.CreatedAt, ReceivedAt: m.CreatedAt})
			pendingEvent := m.Event != nil && slices.ContainsFunc(r.state.Members, func(member MemberState) bool {
				return member.ReadThrough < m.Seq && slices.Contains(m.Event.Targets, member.MembershipID)
			})
			facts.Records[m.Seq] = inputstream.ReleaseFacts{Revision: facts.Revision, SourceConfirmed: true, BusinessReleased: true, Skipped: true, InFlight: pendingEvent}
		}
		plan, err := inputstream.PlanReclaim(inputstream.Snapshot{Partition: partition, Revision: r.state.Revision, Through: r.state.LatestSeq, Reclaimed: r.state.ClearedThrough}, inputstream.RoomPolicy(), records, sizes, facts, r.now())
		if err != nil {
			return err
		}
		if plan.Through != msg.ThroughSeq {
			return ErrBusy
		}
	}
	next := r.candidate()
	// Clone the retained suffix so deleted text is no longer held by the slice.
	next.Messages = append([]RoomMessage{}, next.Messages[msg.ThroughSeq-next.ClearedThrough:]...)
	next.ClearedThrough = msg.ThroughSeq
	for i := range next.Members {
		next.Members[i].ReadThrough = max(next.Members[i].ReadThrough, msg.ThroughSeq)
	}
	return commitRoom(r, ctx, next, reply, result, func() {
		clear(r.pages)
		clear(r.confirmed)
	})
}

func (r *Room) Read(_ actorx.Context, msg ReadMessages) (ReadPage, error) {
	if err := r.identity(msg.RoomIdentity); err != nil {
		return ReadPage{}, err
	}
	i, err := r.caller(msg.Caller)
	if err != nil {
		return ReadPage{}, err
	}
	snapshot := r.state.LatestSeq
	if msg.SnapshotSeq != nil {
		snapshot = *msg.SnapshotSeq
	}
	if snapshot > r.state.LatestSeq || msg.Limit < 0 || msg.Limit > maxMessages || (msg.Mode != ReadUnread && (msg.Peek || msg.Limit != 0)) {
		return ReadPage{}, ErrRequest
	}
	if snapshot < r.state.ClearedThrough {
		return ReadPage{}, ErrReadExpired
	}
	from, remaining := uint64(0), 0
	switch msg.Mode {
	case ReadAll:
		if msg.After != nil || msg.Latest != 0 {
			return ReadPage{}, ErrRequest
		}
	case ReadLatest:
		if msg.After != nil || msg.Latest <= 0 || msg.Latest > maxMessages {
			return ReadPage{}, ErrRequest
		}
		if uint64(msg.Latest) < snapshot {
			from = snapshot - uint64(msg.Latest)
		}
	case ReadRange:
		if msg.After == nil || msg.Latest != 0 {
			return ReadPage{}, ErrRequest
		}
		from = *msg.After
	case ReadUnread:
		if i < 0 || msg.Latest != 0 {
			return ReadPage{}, ErrRequest
		}
		from = r.state.Members[i].ReadThrough
		if msg.After != nil {
			if msg.Peek && msg.SnapshotSeq != nil && *msg.After >= from {
				from = *msg.After
			} else if *msg.After != from {
				return ReadPage{}, ErrReadExpired
			}
		}
		remaining = msg.Limit
		if remaining == 0 {
			remaining = 20
		}
		if !msg.Peek {
			if r.writeErr != nil {
				return ReadPage{}, r.writeErr
			}
			if r.confirming == msg.Caller.MembershipID {
				return ReadPage{}, ErrBusy
			}
			if p, exists := r.pages[msg.Caller.MembershipID]; exists && r.now().Before(p.expires) {
				return ReadPage{}, ErrBusy
			}
		}
	default:
		return ReadPage{}, ErrRequest
	}
	if from > snapshot {
		return ReadPage{}, ErrRequest
	}
	from = max(from, r.state.ClearedThrough)
	page := ReadPage{
		RoomIdentity: r.state.RoomIdentity, MembershipID: msg.Caller.MembershipID,
		Revision: r.state.Revision, SnapshotSeq: snapshot, FromExclusive: from,
		ScannedThrough: from, ClearedThrough: r.state.ClearedThrough, Messages: make([]RoomMessage, 0),
	}
	if i >= 0 {
		page.UnreadCount, _ = r.unread(r.state.Members[i])
	}
	bytes := 0
	for _, m := range r.state.Messages[from-r.state.ClearedThrough : snapshot-r.state.ClearedThrough] {
		self := !roomVisible(m, msg.Caller, msg.Mode != ReadUnread) || msg.Mode == ReadUnread && !roomDeliverable(m, MemberIdentity{RoomIdentity: r.state.RoomIdentity, AgentID: msg.Caller.ID, MembershipID: msg.Caller.MembershipID})
		if !self {
			if len(page.Messages) == maxPageMessages || bytes+len(m.Text) > maxPageBytes {
				break
			}
			page.Messages = append(page.Messages, m)
			bytes += len(m.Text)
			if msg.Mode == ReadUnread {
				remaining--
			}
		}
		page.ScannedThrough = m.Seq
		if msg.Mode == ReadUnread && remaining == 0 {
			break
		}
	}
	page.NextAfter = page.ScannedThrough
	page.Remaining = remaining
	page.HasMore = page.ScannedThrough < snapshot && (msg.Mode != ReadUnread || remaining > 0)
	if msg.Mode == ReadUnread && !msg.Peek && page.ScannedThrough > from {
		id, err := newOpaqueID()
		if err != nil {
			return ReadPage{}, err
		}
		page.ReadReceipt = id
		page.ExpiresAt = r.now().Add(readReceiptTTL)
		seqs := make([]uint64, 0, len(page.Messages))
		for _, message := range page.Messages {
			seqs = append(seqs, message.Seq)
		}
		r.pages[msg.Caller.MembershipID] = pendingRead{id: id, from: from, through: page.ScannedThrough, expires: page.ExpiresAt, snapshot: snapshot, entries: seqs}
		delete(r.confirmed, msg.Caller.MembershipID)
	}
	return page, nil
}

func (r *Room) Confirm(ctx actorx.Context, msg ConfirmRead, reply actorx.Reply[ReadConfirmation]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	i, err := r.member(msg.AgentID, msg.MembershipID)
	if err != nil {
		return err
	}
	if msg.ReadReceipt == "" {
		return ErrReadExpired
	}
	if previous, ok := r.confirmed[msg.MembershipID]; ok && previous.ReadReceipt == msg.ReadReceipt {
		reply.Complete(previous, nil)
		return nil
	}
	p, ok := r.pages[msg.MembershipID]
	if !ok || p.id != msg.ReadReceipt || !r.now().Before(p.expires) || p.from != r.state.Members[i].ReadThrough {
		return ErrReadExpired
	}
	next := r.candidate()
	next.Members[i].ReadThrough = p.through
	// Count against the current committed history, including arrivals after the
	// page snapshot. Those arrivals are never covered by this confirmation.
	m := next.Members[i]
	count, _ := r.unread(m)
	result := ReadConfirmation{MemberIdentity: msg.MemberIdentity, ReadReceipt: p.id, ReadThrough: p.through, UnreadCount: count, Revision: next.Revision + 1}
	proof := roomReadProof{Confirmation: result, From: p.from, Snapshot: p.snapshot, Entries: p.entries}
	proof.Digest = proof.digest()
	if next.ReadProofs == nil {
		next.ReadProofs = make(map[string]roomReadProof)
	}
	next.ReadProofs[msg.MembershipID] = proof
	err = commitRoom(r, ctx, next, reply, result, func() {
		delete(r.pages, msg.MembershipID)
		r.confirmed[msg.MembershipID] = result
	})
	if err == nil {
		r.confirming = msg.MembershipID
	}
	return err
}

func (r *Room) identity(id RoomIdentity) error {
	if id != r.state.RoomIdentity {
		return ErrIdentity
	}
	return nil
}

func (r *Room) writable(id RoomIdentity) error {
	if err := r.identity(id); err != nil {
		return err
	}
	if r.writeErr != nil {
		return r.writeErr
	}
	if r.committing {
		return ErrBusy
	}
	return nil
}

func (r *Room) member(agentID, membershipID string) (int, error) {
	for i, m := range r.state.Members {
		if m.AgentID == agentID && m.MembershipID == membershipID {
			return i, nil
		}
	}
	return -1, ErrIdentity
}

func (r *Room) caller(c Caller) (int, error) {
	if !validText(c.ID) {
		return -1, ErrIdentity
	}
	if c.Kind == "human" && c.MembershipID == "" {
		return -1, nil
	}
	if c.Kind == "agent" {
		return r.member(c.ID, c.MembershipID)
	}
	return -1, ErrIdentity
}

func (r *Room) unread(m MemberState) (int, uint64) {
	count, newest := 0, uint64(0)
	for _, message := range r.state.Messages[m.ReadThrough-r.state.ClearedThrough:] {
		if roomDeliverable(message, MemberIdentity{RoomIdentity: r.state.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}) {
			count++
			newest = message.Seq
		}
	}
	return count, newest
}

func (r *Room) memberInfo(m MemberState) MemberInfo {
	count, newest := r.unread(m)
	return MemberInfo{MemberState: m, UnreadCount: count, NewestUnreadSeq: newest, Attached: !r.targets[m.MembershipID].IsZero()}
}

func (r *Room) candidate() RoomSnapshot {
	next := r.state
	next.Messages, next.Members = slices.Clone(next.Messages), slices.Clone(next.Members)
	next.IntegrationReceipts = maps.Clone(next.IntegrationReceipts)
	next.FollowupNoticeReceipts = maps.Clone(next.FollowupNoticeReceipts)
	next.ReadProofs = maps.Clone(next.ReadProofs)
	return next
}

func commitRoom[R any](r *Room, ctx actorx.Context, next RoomSnapshot, reply actorx.Reply[R], result R, install func()) error {
	if next.Revision == math.MaxUint64 {
		return ErrCapacity
	}
	next.Revision++
	save := r.save
	r.committing = true
	// Request cancellation cannot roll back a commit. Owner shutdown still
	// cancels the task, and Wait joins any file operation already in progress.
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)),
		func(ctx context.Context) (struct{}, error) {
			if err := ctx.Err(); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, save(next)
		},
		func(_ struct{}, err error) bool {
			r.committing = false
			r.confirming = ""
			if err != nil {
				if errors.Is(err, ErrCommitUnknown) {
					r.writeErr = err
				}
				if r.observeMembers != nil {
					r.observeMembers()
				}
				var zero R
				reply.Complete(zero, err)
				return true
			}
			messagesChanged := r.state.LatestSeq != next.LatestSeq || r.state.ClearedThrough != next.ClearedThrough
			r.state = next
			if install != nil {
				install()
			}
			// List exposes the installed revision and the end of committing even
			// when only a receipt changed. Message notices still require real data.
			if r.observeMembers != nil {
				r.observeMembers()
			}
			if messagesChanged && r.observeMessages != nil {
				r.observeMessages()
			}
			r.queueNotifications()
			r.dispatchNotifications()
			reply.Complete(result, nil)
			return true
		}, nil,
	)
	if err != nil {
		r.committing = false
	}
	return err
}
