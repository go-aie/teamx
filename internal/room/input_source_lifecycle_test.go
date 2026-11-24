package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func inputSourceFixture(t *testing.T) (*roomHarness, *platformInputStorage, *service, followupEvent) {
	t.Helper()
	h, p, svc, id := platformReaderFixture(t)
	item := storedGitLab(id, "source", "events", "7", "42", "saved source content")
	item.Source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "142"}
	receivePlatform(t, p, item)
	svc.referenceResolver = referenceTestResolver
	session := startPlatformReader(t, h, p, svc, 1)
	page := deliverPlatform(t, session, streamFor(t, session, item.Text), "source")
	link, err := h.store.readFollowupSource(id, 0, page.Entries[0].Source.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.repository.Seal(session.execution); err != nil {
		t.Fatal(err)
	}
	return h, p, svc, link
}

// Exercise the production maintenance branch that releases BOTH the body and
// its deduplication receipt. Source terminal evidence is a local fixture.
func reclaimReleasedInputSource(t *testing.T, p *platformInputStorage, link followupEvent) {
	t.Helper()
	provider, snapshot, err := p.repository.Locate(*link.Location)
	if err != nil {
		t.Fatal(err)
	}
	record, err := p.repository.Read(snapshot, *link.Location)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := p.store.businessInputLocked(p.id, 0, link.Location)
	if err != nil {
		t.Fatal(err)
	}
	proof := notification.SourceRelease{
		Candidate: notification.SourceReleaseCandidate{Owner: p.id.RoomIdentity, InboxID: p.id.MembershipID, Location: *link.Location, Item: entry.Item, Digest: record.Digest},
		Status:    "released", Revision: 1, Evidence: "fixture-source-terminal",
	}
	inventory := inputSourceInventory{
		Revision: "source-released", Candidates: map[string][]inputstream.Location{provider: {*link.Location}},
		Evidence: map[inputstream.Location]notification.SourceRelease{*link.Location: proof},
		Current:  func(notification.SourceRelease) bool { return true },
	}
	plan, err := p.maintain(provider, inventory, time.Now().Add(7*24*time.Hour))
	if err != nil || plan.Through != link.Seq || plan.Usage.Records != 1 {
		t.Fatal("source was not reclaimed", plan, err)
	}
	if _, err := p.repository.DeliverySourceRef(*link.Location, link.Source.Channel, link.DeliveryID); !errors.Is(err, inputstream.ErrExpired) {
		t.Fatal("test retained the delivery receipt", err)
	}
}

func TestInputSourceAssociationSurvivesReceiptReclamation(t *testing.T) {
	parallelFixtureTest(t)
	h, p, _, link := inputSourceFixture(t)
	id := link.MemberIdentity
	unit, err := h.store.readFollowup(id, link.ObjectRef, referenceDigest("unit_", link.ObjectRef))
	if err != nil {
		t.Fatal(err)
	}
	reclaimReleasedInputSource(t, p, link)
	h.reload(t, true)

	// Business facts are readable before any input repository is opened. Neither
	// recovery nor directory reads may touch receipts, bodies or consumption.
	before := recoveryFiles(t, h.store.dir)
	restored, err := h.store.readFollowupSource(id, 0, link.SourceRef)
	if err != nil || !reflect.DeepEqual(restored, link) {
		t.Fatal("association changed after reclaim/reopen", restored, err)
	}
	page, err := h.store.listInputSources(id, link.ObjectRef, "", 25)
	if err != nil || !page.Complete || len(page.Items) != 1 || page.Items[0].Ref != link.SourceRef {
		t.Fatal("source directory depends on original input", page, err)
	}
	if err := validatePlatformFollowups(h.store); err != nil {
		t.Fatal("saved business validation depends on original input", err)
	}
	facts, err := h.store.recoveryBusinessContext(id, TurnRecord{})
	if err != nil || facts.matters.Total != 1 {
		t.Fatal("Session recovery depends on original input", facts, err)
	}
	if !reflect.DeepEqual(before, recoveryFiles(t, h.store.dir)) {
		t.Fatal("business reads modified saved state")
	}

	p, err = h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	before = recoveryFiles(t, h.store.dir)
	restored, entry, err := h.store.readFollowupSourceContent(id, 0, link.SourceRef)
	if err != nil || entry != nil || !reflect.DeepEqual(restored, link) {
		t.Fatal("reclaimed content was not reported as expired", restored, entry, err)
	}
	facts, err = h.store.recoveryBusinessContext(id, TurnRecord{})
	if err != nil || facts.matters.Total != 1 {
		t.Fatal("Session recovery with last input page failed", facts, err)
	}
	after, err := h.store.readFollowup(id, link.ObjectRef, unit.Ref)
	if err != nil || !reflect.DeepEqual(unit, after) || !reflect.DeepEqual(before, recoveryFiles(t, h.store.dir)) {
		t.Fatal("content query changed business state", after, err)
	}
}

func TestInputSourceBusinessReadsDoNotReadRetainedContent(t *testing.T) {
	parallelFixtureTest(t)
	h, _, _, link := inputSourceFixture(t)
	records := filepath.Join(h.store.dir, "inboxes", link.MembershipID, link.Source.Provider, "records")
	segments, err := filepath.Glob(filepath.Join(records, "*.jsonl"))
	if err != nil || len(segments) != 1 {
		t.Fatal(segments, err)
	}
	if err := os.Remove(segments[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.readFollowupSource(link.MemberIdentity, 0, link.SourceRef); err != nil {
		t.Fatal("association reader opened original content", err)
	}
	if _, err := h.store.recoveryBusinessContext(link.MemberIdentity, TurnRecord{}); err != nil {
		t.Fatal("business recovery opened original content", err)
	}
	if _, _, err := h.store.readFollowupSourceContent(link.MemberIdentity, 0, link.SourceRef); !errors.Is(err, inputstream.ErrCorrupt) {
		t.Fatal("missing retained content was treated as expiry", err)
	}
}

func TestInputSourceAssociationRejectsInvalidSavedFacts(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"member", "reference", "sequence", "partition", "other-member-partition", "other-provider-partition", "source", "target", "action"} {
		t.Run(mode, func(t *testing.T) {
			h, p, _, link := inputSourceFixture(t)
			reclaimReleasedInputSource(t, p, link)
			path := filepath.Join(h.store.followupRoot(link.MemberIdentity), "input-sources", link.SourceRef+".json")
			bad := link
			switch mode {
			case "member":
				bad.MembershipID = "another-member"
			case "reference":
				bad.SourceRef = referenceDigest("src_", "another-source")
			case "sequence":
				bad.Seq++
			case "partition":
				bad.Location = &inputstream.Location{Partition: "invalid", Seq: link.Seq}
			case "other-member-partition":
				owner := inputMember(link.MemberIdentity)
				owner.MembershipID = "another-member"
				bad.Location = &inputstream.Location{Partition: inputstream.PrivatePartitionRef(owner, link.Source.Provider), Seq: link.Seq}
			case "other-provider-partition":
				bad.Location = &inputstream.Location{Partition: inputstream.PrivatePartitionRef(inputMember(link.MemberIdentity), "lark"), Seq: link.Seq}
			case "source":
				bad.Source.SubscriptionID = "invalid"
			case "target":
				if err := os.Remove(h.store.targetPath(link.MemberIdentity, link.ObjectRef, link.TargetRef)); err != nil {
					t.Fatal(err)
				}
			case "action":
				path := filepath.Join(h.store.actionRoot(link.MemberIdentity), "records", "invalid.json")
				if err := h.store.save(path, storedAction{}, followupBytes); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "target" && mode != "action" {
				if err := h.store.save(path, bad, followupBytes); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.store.readFollowupSource(link.MemberIdentity, 0, link.SourceRef); err == nil {
				t.Fatal("invalid association accepted")
			}
			if _, err := h.store.recoveryBusinessContext(link.MemberIdentity, TurnRecord{}); err == nil {
				t.Fatal("business corruption hidden by expiry")
			}
		})
	}
}

func TestFollowupSourceRetiredProofIsDecodeOnly(t *testing.T) {
	parallelFixtureTest(t)
	h, _, _, link := inputSourceFixture(t)
	data, err := json.Marshal(link)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["reclaimed_source_proof"] = json.RawMessage(`"historical-conversion-digest"`)
	path := filepath.Join(h.store.followupRoot(link.MemberIdentity), "input-sources", link.SourceRef+".json")
	if err := h.store.save(path, fields, followupBytes); err != nil {
		t.Fatal(err)
	}
	decoded, err := readFollowupEventSnapshot(path)
	if err != nil || !reflect.DeepEqual(link, decoded) {
		t.Fatal("old snapshot changed association semantics", decoded, err)
	}
	current, err := json.Marshal(decoded)
	if err != nil || bytes.Contains(current, []byte("reclaimed_source_proof")) {
		t.Fatal("retired proof leaked into new writes", err)
	}
	fields["unexpected"] = json.RawMessage(`true`)
	if err := h.store.save(path, fields, followupBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := readFollowupEventSnapshot(path); err == nil {
		t.Fatal("decoding accepted unrelated unknown fields")
	}
}
