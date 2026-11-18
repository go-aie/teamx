package inputstream

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestReclaimBoundedCandidatesPinsAndIncrementalIndexRecovery(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	entries := []ImportEntry{}
	for i := 1; i <= 300; i++ {
		q := request(fmt.Sprint(i), "A", "node", "saved evidence")
		q.Seq, q.DeliveryDigest = uint64(i*2), fmt.Sprint(i)
		entries = append(entries, ImportEntry{AppendRequest: q, Consumed: true})
	}
	if err := r.ImportPartition("approval", 0, 600, entries); err != nil {
		t.Fatal(err)
	}
	identity := PrivateInbox(storedMember)
	if _, err := r.Capabilities(Inbox{identity.RoomID, identity.RoomInstance, "foreign"}, storedMember); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	for batch, want := range []int{128, 128, 44} {
		snap, candidates, err := r.ReclaimCandidates("approval", 128)
		if err != nil || len(candidates) != want {
			t.Fatal(batch, candidates, err)
		}
		facts := ReleaseSet{Revision: fmt.Sprint(batch), Records: map[uint64]ReleaseFacts{}}
		for _, l := range candidates {
			facts.Records[l.Seq] = ReleaseFacts{Revision: "verified", SourceConfirmed: true, BusinessReleased: true, ReceiptReleased: true}
		}
		facts, err = r.ConsumptionRelease("approval", facts)
		if err != nil || len(facts.Records) != want {
			t.Fatal("consumption expanded the candidate range", err)
		}
		now := storedTime.Add(30 * 24 * time.Hour)
		plan, err := r.PrepareReclaim("approval", facts, now)
		if err != nil || plan.Usage.Records != uint64(want) {
			t.Fatal(plan, err)
		}
		if batch == 0 {
			unpin, err := r.PinSnapshots(map[string]Snapshot{"approval": snap})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.CommitReclaim("approval", plan, facts); !errors.Is(err, ErrConflict) {
				t.Fatal("preview pin did not protect the prepared boundary", err)
			}
			unpin()
			unpin()
		}
		if _, err := r.CommitReclaim("approval", plan, facts); err != nil {
			t.Fatal(err)
		}
		if len(r.parts["approval"].state.Receipts) != 300-min((batch+1)*128, 300) {
			t.Fatal("released receipts accumulated")
		}
		usage, err := r.InspectUsage()
		if err != nil {
			t.Fatal(err)
		}
		// Compare the incrementally installed indexes and byte accounting against
		// the original full validator/loader after every published boundary.
		reopened := openTestRepo(t, dir)
		other, err := reopened.InspectUsage()
		if err != nil || usage != other {
			t.Fatal(usage, other, err)
		}
		if len(r.parts["approval"].records) > 0 {
			last := r.parts["approval"].records[600]
			if _, err := r.Read(snapshot(r.parts["approval"]), last.Location); err != nil {
				t.Fatal("retained suffix offset invalid", err)
			}
		}
	}
	if snap, _ := r.Snapshot("approval"); snap.Reclaimed != 600 || snap.Through != 600 {
		t.Fatal(snap)
	}
}

func TestReclaimVerifiedExpiryRetentionAndSkipSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	receipt := appendTest(t, r, "approval", request("old", "A", "node", "expired input"))
	l := Location{receipt.Partition, receipt.Seq}
	if _, err := r.ExcludeCandidates("approval", []Location{{"foreign", l.Seq}}, "expired_input"); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if skipped, err := r.ExcludeCandidates("approval", []Location{l}, "expired_input"); err != nil || len(skipped) != 1 {
		t.Fatal(skipped, err)
	}
	r = openTestRepo(t, dir)
	facts := ReleaseSet{Revision: "source-released", Retention: 7 * 24 * time.Hour, Records: map[uint64]ReleaseFacts{l.Seq: {Revision: "source-and-expiry", SourceConfirmed: true, BusinessReleased: true}}}
	facts, err := r.ConsumptionRelease("approval", facts)
	if err != nil || !facts.Records[l.Seq].Skipped || facts.Records[l.Seq].Consumed {
		t.Fatal(facts, err)
	}
	if plan, err := r.PrepareReclaim("approval", facts, storedTime.Add(4*24*time.Hour)); err != nil || plan.Usage.Records != 0 {
		t.Fatal("configured retention ignored", plan, err)
	}
	plan, err := r.PrepareReclaim("approval", facts, storedTime.Add(7*24*time.Hour))
	if err != nil || plan.Usage.Records != 1 {
		t.Fatal(plan, err)
	}
	if _, err := r.CommitReclaim("approval", plan, facts); err != nil {
		t.Fatal(err)
	}
}
