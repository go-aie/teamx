package room

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Supplied by static platform assembly, never an Agent-provided identity report.
type followupResolver func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error)

type referenceInboxPage struct {
	InboxPage
	Batch     InboxBatch              `json:"batch"`
	Sources   []followupSourceSummary `json:"sources"`
	Followups []followupSummary       `json:"followups"`
}

// AgentInbox owns the original batches and receipts. Business linkage is saved
// separately using stable object/discussion references before output and ack.
func (s *service) readReferenceInbox(ctx context.Context, id MemberIdentity, turn uint64, limit int, resolve followupResolver) (referenceInboxPage, error) {
	var view referenceInboxPage
	s.store.mu.Lock()
	err := s.store.checkFollowupsLocked(id, false)
	s.store.mu.Unlock()
	if err != nil {
		return view, err
	}
	s.inboxMu.Lock()
	addr, err := s.inboxLocked(ctx, id)
	s.inboxMu.Unlock()
	if err != nil {
		return view, err
	}
	page, err := addr.Call[InboxBatchPage](ctx, ReadInboxBatch{MemberIdentity: id, TurnNumber: turn, Limit: limit})
	if err != nil {
		return view, err
	}
	view = referenceInboxPage{InboxPage: page.InboxPage, Batch: page.Batch, Sources: []followupSourceSummary{}, Followups: []followupSummary{}}
	seen := map[string]bool{}
	// Cached source evidence prevents repeated remote resolution on a repeated
	// read or restart. A failed source is retried only through explicit re-resolve.
	for _, entry := range page.Entries {
		if err := ctx.Err(); err != nil {
			return view, err
		}
		link, err := s.resolveFollowupEvent(ctx, id, entry.Seq, turn, false, resolve)
		if err != nil {
			return view, err
		}
		view.Sources = append(view.Sources, sourceSummary(link))
		if link.ObjectRef == "" || seen[link.ObjectRef] {
			continue
		}
		seen[link.ObjectRef] = true
		u, err := s.store.readFollowup(id, link.ObjectRef, referenceDigest("unit_", link.ObjectRef))
		if err != nil {
			return view, err
		}
		view.Followups = append(view.Followups, unitSummary(u))
	}
	// Preserve Inbox's page limit, and bound the additional summaries explicitly.
	b, err := json.Marshal(view)
	if err != nil {
		return view, err
	}
	if len(b) > (1<<20)+followupPageBytes {
		return view, ErrCapacity
	}
	return view, nil
}

func (s *service) resolveFollowupEvent(ctx context.Context, id MemberIdentity, seq, turn uint64, retry bool, resolve followupResolver) (followupEvent, error) {
	s.store.mu.Lock()
	err := s.store.checkFollowupsLocked(id, false)
	var entry InboxEntry
	var old followupEvent
	if err == nil {
		_, err = s.store.runningTurnLocked(id, turn)
	}
	if err == nil {
		entry, err = s.store.inboxEntryLocked(id, seq)
	}
	if err == nil {
		old, err = s.store.loadFollowupEventLocked(entry)
		if errors.Is(err, os.ErrNotExist) {
			// A missing dependency of an existing event is corruption, not an
			// invitation to overwrite the event or silently rebuild progress.
			if _, statErr := os.Stat(s.store.eventPath(id, seq)); statErr == nil {
				err = ErrInvalid
			}
		}
	}
	s.store.mu.Unlock()
	if err == nil && !retry {
		return old, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return old, err
	}

	scope := &referenceResolutionScope{}
	ctx = context.WithValue(ctx, referenceResolutionScopeKey{}, scope)
	defer func() {
		if scope.finish != nil {
			scope.finish()
		}
	}()
	result := platform.TargetResolution{Status: platform.ResolutionUnsupported, ReasonCode: "unsupported", Reason: "reference resolution capability is unavailable", CheckedAt: time.Now().UTC()}
	var access platform.AccessContext
	if resolve != nil {
		result, access, err = resolve(ctx, id, entry.Item.Source)
		if err != nil {
			// Only known safe categories leave the orchestration boundary. Never log
			// or persist a raw transport error, credential, URL or remote body.
			code, reason := "internal_error", "内部解析失败；来源保留，可显式重新解析"
			switch {
			case errors.Is(err, context.Canceled):
				code, reason = "canceled", "解析已取消；来源保留"
			case errors.Is(err, context.DeadlineExceeded):
				code, reason = "timeout", "解析超过时限；来源保留"
			case errors.Is(err, ErrConflict):
				code, reason = "access_changed", "连接、凭据或成员绑定已变化／不可用；请核对当前绑定"
			case errors.Is(err, ErrRequest):
				code, reason = "binding_unavailable", "成员未启用对应平台的读取绑定或未选择有效连接"
			case errors.Is(err, ErrIdentity), errors.Is(err, ErrClosing), errors.Is(err, ErrBusy):
				code, reason = "access_unavailable", "成员或服务当前不可用"
			case errors.Is(err, platform.ErrCredential):
				code, reason = "credential_unavailable", "当前连接凭据不可用"
			}
			slog.WarnContext(ctx, "reference resolution failed", "source_ref", referenceDigest("src_", id, seq, entry.Item.DeliveryID), "step", "resolve", "code", code)
			result = platform.TargetResolution{Status: platform.ResolutionUnavailable, ReasonCode: code, Reason: reason, CheckedAt: time.Now().UTC()}
		}
	}
	if err := ctx.Err(); err != nil {
		return old, err
	}
	if validateFollowupResolution(entry, result, access) != nil {
		result = platform.TargetResolution{Status: platform.ResolutionMismatch, ReasonCode: "invalid_report", Reason: "resolution evidence did not match source or access; source retained", CheckedAt: time.Now().UTC()}
	}
	commit := func() (followupEvent, error) {
		return s.store.recordFollowupResolution(id, seq, turn, old.Revision, result, access)
	}
	if s.referenceCommit != nil && result.ObjectVerified {
		return s.referenceCommit(ctx, id, access, commit)
	}
	return commit()
}
