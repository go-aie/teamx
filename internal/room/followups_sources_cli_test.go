package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	gitlabplatform "github.com/go-aie/teamx/internal/app/builtinplatforms/gitlab"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
)

// Select only the source under test, through the production constructors. No
// legacy fixture wrapper may supply the other built-ins to Store or CLI.
func sourceCLIFixture(t *testing.T, unified bool) (*roomHarness, *service, *platforms.Catalog) {
	t.Helper()
	selected, err := platforms.NewCatalog(gitlabplatform.Definition)
	if err != nil {
		t.Fatal(err)
	}
	state := historyRoom(0, "")
	if unified {
		state.InputLog = &roomLogRoot{Version: 1}
	}
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
	store, err := openStoreWithCatalog(t.TempDir(), state.RoomID, nil, selected)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	r, err := NewRoom(store)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := actorx.NewRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rt.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	addr, err := rt.Spawn(r)
	if err != nil {
		t.Fatal(err)
	}
	h := &roomHarness{store: store, rt: rt, addr: addr, id: state.RoomIdentity, ctx: t.Context()}
	svc := &service{store: store, room: addr, unifiedInputs: unified, inputAdapters: notificationAdaptersFor(nil, selected), info: ServiceInfo{RoomIdentity: state.RoomIdentity, ServiceEpoch: "source-cli"}, requests: make(chan struct{}, 32), referenceResolver: referenceTestResolver}
	return h, svc, selected
}

func sourceCLI(t *testing.T, svc *service, selected *platforms.Catalog) func(MemberIdentity, []string, any) error {
	t.Helper()
	server := httptest.NewServer(svc)
	t.Cleanup(server.Close)
	return func(id MemberIdentity, args []string, out any) error {
		env := cliToolEnv(server.URL, svc, MemberState{AgentID: id.AgentID, MembershipID: id.MembershipID})
		var stdout bytes.Buffer
		if err := runCLIWithCatalog(t.Context(), args, nil, &stdout, io.Discard, env, selected); err != nil {
			return err
		}
		if out != nil {
			return json.Unmarshal(stdout.Bytes(), out)
		}
		return nil
	}
}

func TestMattersCLIInputSourcesAndDiscussion(t *testing.T) {
	h, svc, selected := sourceCLIFixture(t, true)
	cli := sourceCLI(t, svc, selected)
	aID, bID := h.memberID("A", "generation-A"), h.memberID("B", "generation-B")
	begin := func(id MemberIdentity, turn uint64) *inputSession {
		t.Helper()
		if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: turn, Turn: &TurnRecord{Number: turn, Status: "running"}}); err != nil {
			t.Fatal(err)
		}
		p, err := h.store.openPlatformInputStorage(id, svc.inputAdapters)
		if err != nil {
			t.Fatal(err)
		}
		return startPlatformReader(t, h, p, svc, turn)
	}
	a := begin(aID, 1)
	// More than the default source page and one turn's platform delivery quota.
	const count = 27
	for i := range count {
		item := storedGitLab(aID, fmt.Sprintf("source-%02d", i), "events", "7", "42", "discussion evidence")
		item.Source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "142"}
		item.Source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "verified-thread"}
		receivePlatform(t, a.storage, item)
	}
	receivePlatform(t, a.storage, storedGitLab(aID, "pending", "events", "7", "56", "leave unread"))
	a = begin(aID, 2) // freeze bounds after receiving the records
	listed, err := a.listInputStreams(h.ctx, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	directory := listed.(inputstream.Directory)
	var stream string
	for _, item := range directory.Streams {
		if strings.Contains(item.Label, "42") {
			stream = item.Ref
		}
	}
	if stream == "" {
		t.Fatal("missing selected source stream", directory)
	}
	first := readInputStream(t, a, "first", stream, 10000, true)
	if len(first.Entries) != 25 || first.Remaining != 2 {
		t.Fatal(first)
	}
	a = begin(aID, 3)
	last := readInputStream(t, a, "last", stream, 10000, true)
	if len(last.Entries) != 2 || last.Remaining != 0 {
		t.Fatal(last)
	}
	before, err := a.inputState(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var firstPage inputSourcePage
	if err := cli(aID, []string{"matters", "sources"}, &firstPage); err != nil || len(firstPage.Items) != 25 || firstPage.Next == "" || firstPage.Complete {
		t.Fatal(firstPage, err)
	}
	source := firstPage.Items[0]
	if source.Ref == "" || source.TargetRef == "" || source.ObjectRef == "" {
		t.Fatal(source)
	}
	seen := map[string]bool{}
	for _, item := range firstPage.Items {
		seen[item.Ref] = true
	}
	var secondPage inputSourcePage
	if err := cli(aID, []string{"matters", "sources", "--cursor", firstPage.Next}, &secondPage); err != nil || len(secondPage.Items) != 2 || !secondPage.Complete || secondPage.Next != "" {
		t.Fatal(secondPage, err)
	}
	for _, item := range secondPage.Items {
		if seen[item.Ref] {
			t.Fatal("repeated source", item)
		}
		seen[item.Ref] = true
	}
	if len(seen) != count {
		t.Fatal("incomplete source traversal", len(seen))
	}
	// A filtered cursor must retain the exact filter, including on continuation.
	var filtered, tail inputSourcePage
	if err := cli(aID, []string{"matters", "sources", "--object", source.ObjectRef, "--limit", "1"}, &filtered); err != nil || filtered.Next == "" {
		t.Fatal(filtered, err)
	}
	if err := cli(aID, []string{"matters", "sources", "--object", source.ObjectRef, "--cursor", filtered.Next, "--limit", "1"}, &tail); err != nil || len(tail.Items) != 1 || tail.Items[0].Ref == filtered.Items[0].Ref {
		t.Fatal(tail, err)
	}
	var content struct {
		Source inputStreamSource `json:"source"`
		Entry  struct {
			Item struct {
				Text string `json:"text"`
			} `json:"item"`
		} `json:"entry"`
	}
	if err := cli(aID, []string{"matters", "source", "--source", source.Ref}, &content); err != nil || content.Source.Ref != source.Ref || content.Entry.Item.Text != "discussion evidence" {
		t.Fatal(content, err)
	}
	if err := cli(aID, []string{"matters", "resolve", "--source", source.Ref, "--turn", "3"}, &content); err != nil || content.Source.TargetRef != source.TargetRef {
		t.Fatal(content, err)
	}
	type receipt struct {
		UnitRef      string `json:"unit_ref"`
		ObjectRef    string `json:"object_ref"`
		Version      uint64 `json:"unit_version"`
		Reused       bool   `json:"reused"`
		RequiresRead bool   `json:"requires_read"`
	}
	args := []string{"matters", "ensure", "--source", source.Ref, "--target", source.TargetRef, "--turn", "3"}
	var created, reused receipt
	if err := cli(aID, args, &created); err != nil || created.Reused || !created.RequiresRead || created.UnitRef == "" || created.ObjectRef != source.ObjectRef {
		t.Fatal(created, err)
	}
	if err := cli(aID, args, &reused); err != nil || !reused.Reused || !reused.RequiresRead || reused.UnitRef != created.UnitRef || reused.Version != created.Version {
		t.Fatal(reused, err)
	}
	var discussion agentFollowupRead
	if err := cli(aID, []string{"matters", "read", "--object", created.ObjectRef, "--unit", created.UnitRef, "--turn", "3"}, &discussion); err != nil || discussion.Target.Ref != source.TargetRef || discussion.Target.Target.Discussion.Value != "verified-thread" || discussion.Target.Ref == source.ObjectRef {
		t.Fatal("discussion did not restore its exact target", discussion, err)
	}
	// The second member has a valid running execution but owns none of A's refs.
	begin(bID, 1)
	for _, check := range []struct {
		name string
		id   MemberIdentity
		args []string
	}{
		{"invalid cursor", aID, []string{"sources", "--cursor", "invalid"}},
		{"changed cursor filter", aID, []string{"sources", "--object", source.ObjectRef, "--cursor", firstPage.Next}},
		{"missing cursor filter", aID, []string{"sources", "--cursor", filtered.Next}},
		{"foreign cursor", bID, []string{"sources", "--cursor", firstPage.Next}},
		{"foreign source", bID, []string{"source", "--source", source.Ref}},
		{"foreign resolution", bID, []string{"resolve", "--source", source.Ref, "--turn", "1"}},
		{"foreign discussion", bID, []string{"ensure", "--source", source.Ref, "--target", source.TargetRef, "--turn", "1"}},
		{"invalid source", aID, []string{"ensure", "--source", "invalid", "--target", source.TargetRef, "--turn", "3"}},
		{"unmatched target", aID, []string{"ensure", "--source", source.Ref, "--target", source.ObjectRef, "--turn", "3"}},
		{"stale turn", aID, []string{"ensure", "--source", source.Ref, "--target", source.TargetRef, "--turn", "2"}},
		{"legacy offset on unified input", aID, []string{"sources", "--after", "1"}},
	} {
		t.Run(check.name, func(t *testing.T) {
			if err := cli(check.id, append([]string{"matters"}, check.args...), nil); err == nil {
				t.Fatal("invalid reference accepted", check.args)
			}
		})
	}
	after, err := a.inputState(h.ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("source operations changed input consumption", err)
	}
	if h.store.catalog != selected || h.store.notificationCapabilities("lark").Input != nil {
		t.Fatal("selected source catalog replaced")
	}
}

func TestMattersCLILegacySourceContracts(t *testing.T) {
	h, svc, selected := sourceCLIFixture(t, false)
	id := h.memberID("A", "generation-A")
	addr, err := SpawnAgentInbox(h.rt, h.store, id, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
	inbox := &inboxHarness{roomHarness: h, id: id, addr: addr}
	svc.inboxes = map[string]actorx.Addr{id.MembershipID: addr}
	for i := range 2 {
		key := fmt.Sprint(i)
		source := referenceTestSource(key)
		source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "legacy-thread"}
		receiveReference(t, inbox, key, source)
	}
	inboxRunning(t, inbox, 1, 2)
	cli := sourceCLI(t, svc, selected)
	if err := cli(id, []string{"inbox", "read"}, nil); err != nil {
		t.Fatal(err)
	}
	var first, next followupSourcePage
	if err := cli(id, []string{"matters", "sources", "--after", "0", "--limit", "1"}, &first); err != nil || len(first.Items) != 1 || first.NextAfter == 0 || first.Complete {
		t.Fatal(first, err)
	}
	if err := cli(id, []string{"matters", "sources", "--after", fmt.Sprint(first.NextAfter), "--limit", "1"}, &next); err != nil || len(next.Items) != 1 || next.Items[0].Seq == first.Items[0].Seq || !next.Complete {
		t.Fatal(next, err)
	}
	source := next.Items[0]
	seq := fmt.Sprint(source.Seq)
	if err := cli(id, []string{"matters", "source", "--seq", seq, "--source", source.SourceRef}, nil); err != nil {
		t.Fatal(err)
	}
	if err := cli(id, []string{"matters", "resolve", "--seq", seq, "--source", source.SourceRef, "--turn", "1"}, nil); err != nil {
		t.Fatal(err)
	}
	var unit struct {
		Ref string `json:"unit_ref"`
	}
	if err := cli(id, []string{"matters", "ensure", "--seq", seq, "--target", source.TargetRef, "--turn", "1"}, &unit); err != nil || unit.Ref == "" {
		t.Fatal(unit, err)
	}
}
