package room

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/calendar"
)

func (a *CalendarActor) deliver(owner actorx.Context) {
	if a.frozen != nil || a.closing || a.h.retired.Load() || a.h.service.closing.Load() {
		return
	}
	// A bounded set of owned delivery workers leaves the mutation slot free.
	for _, run := range a.state.Runs {
		if len(a.inFlight) >= 4 {
			return
		}
		r := *run
		kind := ""
		if r.Phase == "publishing" && r.Publication.Status != "published" {
			kind = "report"
		} else if !a.closing && !a.h.retired.Load() && !a.h.service.closing.Load() && !a.h.service.restarting.Load() {
			if r.CancelRequested && r.Status == "running" {
				kind = "cancel"
			} else if r.Status == "queued" && r.Reason != "work_calendar_uncovered" && !r.EventDelivered {
				kind = "event"
			}
		}
		if kind == "" {
			continue
		}
		key := r.ID + "/" + kind
		if a.inFlight[key] {
			continue
		}
		if r.NextCheckAt.After(time.Now()) {
			continue
		}
		a.inFlight[key] = true
		h := a.h
		_, err := actorx.StartTask(owner.WithContext(context.WithoutCancel(owner)), func(ctx context.Context) (PostReceipt, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			switch kind {
			case "event":
				return h.service.room.Call[PostReceipt](ctx, AppendRoomEvent{RoomIdentity: h.service.info.RoomIdentity, Event: RoomEvent{ID: calendar.ID("calendar_due_", r.ID), Kind: "calendar_due", SourceSeq: r.Task.Source.MessageSeq, OperationID: r.ID, ResultVersion: 1, ResultRef: "calendar/run/" + r.ID, Targets: []string{r.Task.MembershipID}, Actor: Author{Kind: "system", ID: "calendar"}}, Summary: fmt.Sprintf("定时任务到期：%s。run_id=%s；请先阅读 teamx calendar --help，核对最新人类意图后用 calendar claim 领取，再办理并以 calendar finish 发布报告。", r.Task.Definition.Title, r.ID), CreatedAt: r.DueAt})
			case "report":
				return h.service.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: h.service.info.RoomIdentity, Caller: Caller{Author: Author{Kind: "agent", ID: r.Task.AgentID}, MembershipID: r.Task.MembershipID}, ClientMessageID: r.Publication.ClientMessageID, Text: r.Report, DurablePublication: true})
			case "cancel":
				id := MemberIdentity{h.service.info.RoomIdentity, r.Task.AgentID, r.Task.MembershipID}
				binding := turnExecution(r.Binding)
				_, err := h.service.target(id.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "cancel_bound", expectedBinding: &binding})
				return PostReceipt{}, err
			}
			return PostReceipt{}, ErrRequest
		}, func(receipt PostReceipt, err error) bool {
			delete(a.inFlight, key)
			// This callback re-enters the same ordered commit queue. A late receipt
			// records a delivery fact without restoring claim eligibility.
			a.waiters++
			a.queue = append(a.queue, calendarOperation{internal: true, run: func(_ context.Context, state *calendar.State) (any, error) {
				current := state.Runs[r.ID]
				if current == nil {
					return nil, ErrIdentity
				}
				current.NextCheckAt = time.Now().UTC().Add(5 * time.Second)
				if err != nil {
					reason := "delivery_pending_verification"
					if errors.Is(err, ErrCapacity) {
						reason = "capacity"
					}
					if errors.Is(err, ErrIdentity) {
						reason = "executor_unavailable"
					}
					current.Reason = reason
					if task := state.Tasks[current.Task.ID]; task != nil {
						if task.BlockedReason == "" || task.BlockedReason == "delivery_pending_verification" || task.BlockedReason == "capacity" || task.BlockedReason == "executor_unavailable" {
							task.BlockedReason = reason
						}
					}
				}
				if err == nil {
					current.Reason = ""
					if task := state.Tasks[current.Task.ID]; task != nil {
						if task.BlockedReason == "delivery_pending_verification" || task.BlockedReason == "capacity" || task.BlockedReason == "executor_unavailable" {
							task.BlockedReason = ""
						}
					}
					switch kind {
					case "event":
						current.EventDelivered = true
						current.EventSeq = receipt.Seq
						if current.Status == "queued" && len(current.Supplies) == 0 {
							current.NextCheckAt = time.Time{}
						}
					case "report":
						current.Publication.Status = "published"
						current.Publication.Seq = receipt.Seq
						current.Phase = ""
						current.Status = current.Outcome
						current.Settled = true
						if t := state.Tasks[current.Task.ID]; t != nil && t.State == "active" && t.DefinitionVersion == current.Task.DefinitionVersion && (t.Definition.Schedule.Frequency == "once" || t.Evaluated) {
							pending := false
							for _, other := range state.Runs {
								if other.Task.ID == t.ID && !other.Settled {
									pending = true
								}
							}
							if !pending {
								t.State = "completed"
							}
						}
					case "cancel":
						current.NextCheckAt = time.Now().UTC().Add(5 * time.Second)
					}
				}
				if current.CancelRequested && kind == "event" {
					current.NextCheckAt = time.Time{}
				}
				current.Version++
				return nil, nil
			}})
			a.dispatch(owner)
			a.finishQuiesce()
			return true
		}, nil)
		if err != nil {
			delete(a.inFlight, key)
		}
	}
}
