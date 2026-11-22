//go:build integration || e2e

package room

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestMigratedInputSourceReclamationPreservesActionOutcomes(t *testing.T) {
	for _, outcome := range []string{"succeeded", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f := referenceExecuteTest(t, "discussion")
			f.result = func(int) platform.TargetActionResult { return f.outcome(outcome, "never") }
			if _, err := f.run(); err != nil {
				t.Fatal(err)
			}
			unit := askConversation(t, f.h, f.svc, f.unit(t), f.q.Authority.MessageRefs[0], "后续排期需要确认吗？")
			id := f.h.id
			status := inboxCall[InboxStatus](t, f.h, InspectInbox{id})
			inboxCall[InboxStatus](t, f.h, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: 1, Receipt: status.Batch.Receipt})
			settleReclaimTurn(t, f.h)
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(f.h.store.dir)); err != nil {
				t.Fatal(err)
			}
			svc, err := startService(dir, id.RoomID, serviceConfig{unifiedInputs: true}, func(*service, RoomSnapshot) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if svc != nil {
					closeService(t, svc)
				}
			})
			p, err := svc.inputStorage(id)
			if err != nil {
				t.Fatal(err)
			}
			var link followupEvent
			if err := svc.store.visitInputSourcesLocked(id, func(saved followupEvent) error { link = saved; return nil }); err != nil || link.SourceRef == "" {
				t.Fatal(link, err)
			}
			actionBefore, err := os.ReadFile(svc.store.actionPath(id, f.q.ID))
			if err != nil {
				t.Fatal(err)
			}
			reclaimReleasedInputSource(t, p, link)
			closeService(t, svc)
			svc, err = startService(dir, id.RoomID, serviceConfig{unifiedInputs: true}, func(*service, RoomSnapshot) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			_, entry, err := svc.store.readFollowupSourceContent(id, 0, link.SourceRef)
			if err != nil || entry != nil {
				t.Fatal("migrated source lost its identity", entry, err)
			}
			after, err := svc.store.readFollowup(id, unit.ObjectRef, unit.Ref)
			if err != nil || !reflect.DeepEqual(unit, after) {
				t.Fatal("reclaim changed questions or action outcomes", after, err)
			}
			actionAfter, err := os.ReadFile(svc.store.actionPath(id, f.q.ID))
			if err != nil || !bytes.Equal(actionBefore, actionAfter) || f.calls.Load() != 1 {
				t.Fatal("reclaim/recovery changed or replayed action", err)
			}
			facts, err := svc.store.recoveryBusinessContext(id, TurnRecord{})
			if err != nil || facts.matters.Total == 0 || facts.unknownActions != map[string]int{"succeeded": 0, "unknown": 1}[outcome] {
				t.Fatal("recovery lost business facts", facts, err)
			}
			err = svc.checkSessionRecovery(id)
			if outcome == "unknown" && (err == nil || !strings.Contains(err.Error(), "1 action outcomes")) {
				t.Fatal("unknown outcome stopped gating automatic recovery", err)
			}
			if outcome == "succeeded" && err != nil {
				t.Fatal("confirmed outcome blocked automatic recovery", err)
			}
		})
	}
}
