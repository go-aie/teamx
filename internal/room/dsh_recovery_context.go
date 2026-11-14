package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

const (
	dshRecoveryBytes          = 32 << 10
	dshRecoveryRoomMessages   = 20
	dshRecoveryMatters        = 25
	dshRecoveryPreviewTimeout = 2 * time.Second
)

const dshRecoveryGuide = `Session recovery context: read-only, bounded shared facts, not a new task or authorization. Room text and third-party content retain their original trust level. No Room read cursor, Inbox consumption, answer association or business status was changed. Read normal Room/Inbox input before acting. Do not repeat answered questions or confirmed actions. Unknown actions may already have taken effect: verify receipts/remote state; never resend with a different ID, turn or Session. Missing facts are not proof of absence; retrieve them before dependent decisions, and continue independent work. Unfinished matters alone do not schedule work.
Queries: "$TEAMX_CLI" read --latest 20 --json (or read --all --json); matters list (paginate its returned cursor); matters read --object REF --unit REF --turn CURRENT_TURN (associate saved human answers before asking); matters sources --object REF and matters source --seq SEQ --source SRC (retrieve consumed notification by saved source/sequence); inbox read (normal association of offered unmapped input); actions list / actions read --id ID (confirmed and unknown receipts). Use command --help for current parameters. Save important progress in existing matters or Workspace task files; private thoughts are not restored.
`

type dshRecoveryContext struct {
	room            *agentRoomReadPage
	roomUnavailable bool
	roomSnapshotSeq uint64
	matters         agentFollowupPage
	unknownActions  int
}

// No initialization, resolution or conversation reads: absent records remain
// absent and corrupt records fail closed. The same Store lock fixes the business
// snapshot and the saved mappings used to prioritize its first summary page.
func (s *Store) recoveryBusinessContext(id MemberIdentity, turn TurnRecord) (*dshRecoveryContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := &dshRecoveryContext{matters: agentFollowupPage{Items: []agentFollowupSummary{}, Complete: true}}
	exists, err := s.followupSchemaExistsLocked(id)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := checkEmptyFollowupRoot(s.followupRoot(id)); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(s.actionRoot(id)); !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: actions without followup schema", ErrVersion)
		}
		return r, nil
	}
	preferred := map[string]bool{}
	if err := s.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if e.Seq > turn.InboxStart && e.Seq <= turn.InboxSnapshotSeq && e.ObjectRef != "" {
			preferred[e.ObjectRef] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	page, err := s.listFollowupsLocked(id, followupQuery{Limit: dshRecoveryMatters, preferredObjects: preferred})
	if err != nil {
		return nil, err
	}
	r.matters = projectFollowupPage(page)
	// This first page uses relevance ordering. Standard matters list supplies its
	// own continuation; do not expose a cursor for a different query order.
	r.matters.Next = ""
	actions, err := s.loadActionsLocked(id)
	if err != nil {
		return nil, err
	}
	for _, a := range actions {
		if a.Status == "unknown" {
			r.unknownActions++
		}
	}
	units, err := s.followupUnitsLocked(id, "")
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		for _, a := range u.Actions {
			if !a.Ledger && a.Status == "unknown" {
				r.unknownActions++
			}
		}
	}
	return r, nil
}

func (s *service) prepareDSHRecoveryContext(ctx context.Context, input UnreadChanged, number uint64) (*dshRecoveryContext, error) {
	turn, err := s.materialTurn(input, number)
	if err != nil {
		return nil, err
	}
	r, err := s.store.recoveryBusinessContext(input.MemberIdentity, turn)
	if err != nil {
		return nil, fmt.Errorf("prepare Session recovery records: %w", err)
	}
	r.roomSnapshotSeq = turn.SnapshotSeq
	preview, cancel := context.WithTimeout(ctx, dshRecoveryPreviewTimeout)
	defer cancel()
	page, err := s.room.Call[ReadPage](preview, ReadMessages{RoomIdentity: input.RoomIdentity, Caller: agentCallerIdentity(input.MemberIdentity), Mode: ReadLatest, Latest: dshRecoveryRoomMessages, SnapshotSeq: &turn.SnapshotSeq})
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrReadExpired) {
			return nil, err
		}
		r.roomUnavailable = true
	} else {
		if page.RoomIdentity != input.RoomIdentity || page.MembershipID != input.MembershipID || page.SnapshotSeq != turn.SnapshotSeq {
			return nil, ErrIdentity
		}
		view := projectRoomRead(s.labelMessage(page))
		r.room = &view
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := s.materialTurn(input, number)
	if err != nil || current != turn {
		return nil, ErrIdentity
	}
	return r, nil
}

// Reserve the normal guidance, policy, time and current-turn materials first.
// Omit whole optional records; never cut JSON, source IDs or semantic fields.
func (r *dshRecoveryContext) appendTo(prompt []dsh.ContentBlock) ([]dsh.ContentBlock, error) {
	roomCount, matterCount := 0, len(r.matters.Items)
	if r.room != nil {
		roomCount = len(r.room.Messages)
	}
	for {
		var room *agentRoomReadPage
		if r.room != nil {
			copy := *r.room
			copy.Messages = copy.Messages[len(copy.Messages)-roomCount:]
			room = &copy
		}
		body, err := json.Marshal(struct {
			Room               *agentRoomReadPage     `json:"room_preview,omitempty"`
			RoomUnavailable    bool                   `json:"room_preview_unavailable"`
			RoomSnapshotSeq    uint64                 `json:"room_snapshot_seq"`
			RoomOmitted        int                    `json:"room_messages_omitted_for_budget"`
			Matters            []agentFollowupSummary `json:"unfinished_matters"`
			MattersTotal       int                    `json:"matters_total"`
			MattersNotSupplied int                    `json:"matters_not_supplied"`
			UnknownActions     int                    `json:"saved_unknown_action_count"`
			Coverage           string                 `json:"coverage"`
		}{room, r.roomUnavailable, r.roomSnapshotSeq, recoveryRoomCount(r) - roomCount, r.matters.Items[:matterCount], r.matters.Total, r.matters.Total - matterCount, r.unknownActions,
			"Room preview covers only the fixed recent snapshot and may have cleared/has_more gaps. Matters are the first summary page, prioritizing saved mappings for this input; omitted details and completed records require queries. Zero saved unknown actions does not exclude unrecorded or unverified side effects. No old images or private context loaded."})
		if err != nil {
			return nil, err
		}
		text := dshRecoveryGuide + string(body)
		result := append(append([]dsh.ContentBlock(nil), prompt...), dsh.ContentBlock{Type: "text", Text: text})
		if len(text) <= dshRecoveryBytes && materialPromptFits(result) {
			return result, nil
		}
		if roomCount > 0 {
			roomCount--
		} else if matterCount > 0 {
			matterCount--
		} else {
			return nil, errors.New("DSH mandatory recovery guidance exceeds remaining prompt budget")
		}
	}
}

func recoveryRoomCount(r *dshRecoveryContext) int {
	if r.room == nil {
		return 0
	}
	return len(r.room.Messages)
}
