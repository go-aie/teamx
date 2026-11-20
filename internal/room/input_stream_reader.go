package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/mark3labs/mcp-go/mcp"
)

type inputSourceUnavailable struct{ Reason string }

func (e *inputSourceUnavailable) Error() string { return e.Reason }

func (s *inputSession) entries(a inputstream.ReadAttempt) ([]InboxEntry, error) {
	return inputOwnerValue(context.Background(), s.storage, "read_entries", func(ctx context.Context) ([]InboxEntry, error) { return s.entriesOwned(a) })
}
func (s *inputSession) entriesOwned(a inputstream.ReadAttempt) ([]InboxEntry, error) {
	p := s.storage
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return nil, err
	}
	out := []InboxEntry{}
	for _, l := range a.Entries {
		loc := l
		e, err := p.store.businessInputLocked(p.id, 0, &loc)
		if err != nil {
			return nil, err
		}
		ad, err := p.store.inboxAdmissionLocked(e)
		if err != nil {
			return nil, err
		}
		if !ad.Verified || ad.Decision != notification.AdmissionAllow {
			return nil, &inputSourceUnavailable{"saved source admission unavailable"}
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *inputSession) notices(entries []InboxEntry) []materialNotice {
	out := inboxImageNotices(s.service.info, s.storage.id, entries)
	for i := range out {
		out[i].request.Seq = entries[i].Seq
		out[i].request.SourceRef = inputSourceRef(entries[i])
	}
	return out
}

func (s *inputSession) preparePlatformPage(ctx context.Context, a inputstream.ReadAttempt, render inputPageRenderer) error {
	entries, err := s.entries(a)
	if err != nil {
		return err
	}
	page := inputPageContent{remaining: a.Remaining, notifications: []inputStreamNotificationEntry{}}
	state, err := s.inputState(ctx)
	if err != nil {
		return err
	}
	if a.HasMore && a.Next == "" {
		page.stop = "read_budget_exhausted"
	}
	if len(entries) == 0 && page.stop == "" {
		page.stop = "stream_empty"
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		link, err := s.service.resolveInputSource(ctx, s.storage.id, entry.Seq, entry.Location, s.execution.Turn, false, s.service.referenceResolver)
		if err != nil {
			return err
		}
		view := projectStreamNotification(projectInboxEntry(entry, sourceSummary(link)))
		page.notifications = append(page.notifications, view)
		if link.ObjectRef != "" && !seen[link.ObjectRef] {
			u, err := s.storage.store.readFollowup(s.storage.id, link.ObjectRef, referenceDigest("unit_", link.ObjectRef))
			if inboxContextOnly(entry) && errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			seen[link.ObjectRef] = true
			page.followups = append(page.followups, projectFollowupSummary(unitSummary(u)))
		}
	}
	notices := s.notices(entries)
	sources := map[uint64]string{}
	for _, e := range entries {
		sources[e.Seq] = inputSourceRef(e)
	}
	return s.prepareInputMaterials(ctx, a, page, notices, len(entries), sources, state, render)
}

// The caller owns the member operation lease through the output handoff.
func (s *inputSession) readPage(ctx context.Context, q inputstream.ReadRequest, render inputPageRenderer) (*mcp.CallToolResult, func(context.Context) error, error) {
	var a inputstream.ReadAttempt
	var err error
	fail := func(err error) (*mcp.CallToolResult, func(context.Context) error, error) {
		var local *inputSourceUnavailable
		if errors.As(err, &local) && (a.Phase == "reserved" || a.Phase == "prepared") {
			if abortErr := s.coordinate(ctx, func(r inputstream.ExecutionStorage) error { return r.Abort(s.execution, a.ID, local.Reason) }); abortErr != nil {
				return nil, nil, abortErr
			}
		}
		return nil, nil, err
	}
	lookup, lookupErr := s.lookupInputRead(ctx, q)
	previous, parent := lookup.previous, lookup.parent
	if lookupErr != nil {
		return fail(lookupErr)
	}
	roomRead := s.unified && (q.Stream == roomStreamRef(s.storage.id) || previous != nil && previous.Kind == "room" || parent != nil && parent.Kind == "room")
	if previous != nil {
		a = *previous
	} else if roomRead {
		a, err = s.reserveRoom(ctx, q, parent)
	} else if _, _, _, sharedErr := s.sharedStream(q.Stream); sharedErr == nil {
		a, err = s.reserveShared(ctx, q)
	} else {
		err = s.coordinate(ctx, func(r inputstream.ExecutionStorage) error {
			var err error
			a, err = r.Reserve(s.execution, q)
			return err
		})
	}

	if err != nil {
		return fail(err)
	}
	if a.Phase == "failed_before_output" {
		return fail(fmt.Errorf("%w: %s", inputstream.ErrBlocked, a.Failure))
	}
	if a.Phase == "reserved" {
		prepare := s.preparePlatformPage
		if roomRead {
			prepare = s.prepareRoomPage
		}
		if err := prepare(ctx, a, render); err != nil {
			return fail(err)
		}
	}
	var notices []materialNotice
	if !roomRead {
		entries, e := s.entries(a)
		if e != nil {
			return fail(e)
		}
		notices = s.notices(entries)
	} else {
		entries, e := s.roomEntries(a)
		if e != nil {
			return fail(e)
		}
		notices = s.roomPageNotices(entries, a)
	}
	if err := s.checkMaterials(ctx, notices, nil); err != nil {
		return fail(err)
	}
	data, err := s.inputResult(ctx, a.ID)
	if err != nil {
		return fail(err)
	}
	var result inputPageResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fail(err)
	}
	out, err := result.mcpResult()
	if err != nil {
		return fail(err)
	}
	if a.Kind == "shared" {
		p := s.storage.store.sharedHandle.Load()
		if p == nil || a.Shared == nil {
			return fail(ErrIdentity)
		}
		if _, err := sharedOwnerValue(ctx, p, func() (struct{}, error) { return struct{}{}, p.repository.AuthorizeSharedHandoff(*a.Shared) }); err != nil {
			return fail(err)
		}
	}
	if err := s.coordinate(ctx, func(r inputstream.ExecutionStorage) error { return r.Handoff(s.execution, a.ID) }); err != nil {
		return fail(err)
	}
	confirmOwned := func(ctx context.Context) error {
		if err := s.checkMaterials(ctx, notices, nil); err != nil {
			return err
		}
		if roomRead {
			state, e := s.inputState(ctx)
			if e != nil {
				return e
			}
			for _, current := range state.Attempts {
				if current.ID == a.ID {
					a = current
				}
			}
			if a.Phase == "settled" {
				return nil
			}
			if _, err := s.service.room.Call[ReadConfirmation](ctx, ConfirmInputRead{MemberIdentity: s.storage.id, Attempt: a}); err != nil {
				return err
			}
			return s.coordinate(ctx, func(r inputstream.ExecutionStorage) error { return r.SettleOwned(s.execution, a.ID, a.Proof()) })
		}
		if a.Kind == "shared" {
			return s.confirmShared(ctx, a)
		}
		return s.consume(ctx, func(r inputstream.MemberConsumption) error { return r.Confirm(s.execution, a.ID) })
	}
	return out, func(ctx context.Context) error { return confirmOwned(ctx) }, nil
}
