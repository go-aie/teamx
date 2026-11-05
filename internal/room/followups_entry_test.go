package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func entryCLI(t *testing.T, svc *service) func([]string, any) error {
	t.Helper()
	svc.requests = make(chan struct{}, 32)
	svc.info.ServiceEpoch = "r5-test"
	server := httptest.NewServer(svc)
	t.Cleanup(server.Close)
	env := cliToolEnv(server.URL, svc, MemberState{AgentID: "A", MembershipID: "generation-A"})
	return func(args []string, out any) error {
		var stdout, stderr bytes.Buffer
		err := runCLI(context.Background(), args, &stdout, &stderr, env)
		if err == nil && out != nil {
			err = json.Unmarshal(stdout.Bytes(), out)
		}
		return err
	}
}
func entryChange(t *testing.T, command, op string, q any) []string {
	t.Helper()
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	return []string{command, op, "--change", string(data)}
}

func TestReferenceEntryCLIConversationAndRecovery(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	source := referenceTestSource("entry")
	receiveReference(t, h, "entry", source)
	inboxRunning(t, h, 1, 1)
	svc := referenceTestService(h)
	svc.referenceResolver = referenceTestResolver
	cli := entryCLI(t, svc)
	var inbox referenceInboxPage
	if err := cli([]string{"inbox", "read"}, &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Followups) != 1 || len(inbox.Sources) != 1 {
		t.Fatal(inbox)
	}
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.ConsumedThrough != 1 {
		t.Fatal(status)
	}
	u, err := h.store.readFollowup(h.id, inbox.Followups[0].ObjectRef, inbox.Followups[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	question := followupQuestionChange{followupCommand: conversationCommand(u, 1), Text: "这次按方案 A 回复，可以吗？"}
	if err := cli(entryChange(t, "matters", "notify", question), &u); err != nil {
		t.Fatal(err)
	}
	if len(u.Questions) != 1 || u.Questions[0].Seq == 0 {
		t.Fatal(u)
	}
	// A lost response keeps the old request version and reconciles one public send.
	if err := cli(entryChange(t, "matters", "notify", question), &u); err != nil {
		t.Fatal(err)
	}
	answer := postConversationReply(t, h, "entry-answer", "这次按方案 A 回复", u.Questions[0].Seq)
	var detail struct {
		Unit   followupUnit `json:"unit"`
		Target storedTarget `json:"target"`
	}
	read := []string{"matters", "read", "--object", u.ObjectRef, "--unit", u.Ref, "--turn", "1"}
	if err := cli(read, &detail); err != nil {
		t.Fatalf("read %v failed: %v; unit=%+v; answer=%+v", read, err, u, answer)
	}
	u = detail.Unit
	if len(u.Questions[0].Answers) != 1 || u.Questions[0].Answers[0].Message.Seq != answer.Seq || u.Status != "pending" {
		t.Fatal(u)
	}
	human := postConversationReply(t, h, "entry-rule", "以后项目123的使用问题有文档就直接回复，交付时间先问我", 0)
	var policy agentPolicyView
	if err := cli(entryChange(t, "policy", "update", conversationRule(human.Seq)), &policy); err != nil || len(policy.Effective) != 1 {
		t.Fatal(policy, err)
	}
	var sources followupSourcePage
	if err := cli([]string{"matters", "sources", "--object", u.ObjectRef}, &sources); err != nil || len(sources.Items) != 1 {
		t.Fatal(sources, err)
	}
	var event followupEvent
	if err := cli([]string{"matters", "source", "--seq", "1", "--source", sources.Items[0].SourceRef}, &event); err != nil || event.Source.ItemKey != "entry" {
		t.Fatal(event, err)
	}
	// Exercise the real durable Session rotation reference before opening a new runtime.
	backend := dshBackend{store: h.store}
	if _, err := backend.RotateSession(adapterOptions(h.store, h.id), false); err != nil {
		t.Fatal(err)
	}
	// A new runtime/turn recovers the same business identity and human evidence.
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	restored := referenceTestService(h)
	cli = entryCLI(t, restored)
	read[len(read)-1] = "2"
	if err := cli(read, &detail); err != nil || detail.Unit.Ref != u.Ref || detail.Unit.Questions[0].Answers[0].Message.Text != "这次按方案 A 回复" {
		t.Fatal(detail, err)
	}
	var current agentPolicyView
	if err := cli([]string{"policy", "read"}, &current); err != nil || current.PolicyToken != policy.PolicyToken {
		t.Fatal(current, err)
	}
}

func TestReferenceEntryRetiredContractsAndIdentity(t *testing.T) {
	h, svc, units, _ := conversationHarness(t, 1)
	cli := entryCLI(t, svc)
	u := units[0]
	for _, args := range [][]string{{"matters", "context"}, {"matters", "enrich", "--change", "{}"}, {"matters", "relate", "--change", "{}"}, {"matters", "update", "--seq", "1", "--id", "new", "--version", "0"}, {"policy", "suggest"}, {"policy", "suggest-decision"}, {"actions", "execute", "--request", `{"seq":1,"matter_id":"main","target":{"project_id":"123","mr_iid":"42"}}`}} {
		if err := cli(args, nil); err == nil {
			t.Fatal("retired contract accepted", args)
		}
	}
	for _, path := range []string{"/v1/matters/context", "/v1/matters/enrich", "/v1/matters/relate", "/v1/decisions/read", "/v1/decisions/update"} {
		req := apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(h.id)}
		data, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(data)))
		if w.Code == 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(path, w.Code, w.Body)
		}
	}
	// Management can navigate but cannot impersonate an Agent mutation.
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: h.id.AgentID, MembershipID: h.id.MembershipID, Followup: &followupRequest{followupCommand: conversationCommand(u, 1)}}
	if _, err := svc.memberFollowups(h.ctx, req, "/v1/matters/read"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.memberFollowups(h.ctx, req, "/v1/matters/update"); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	req.Caller = agentCallerIdentity(h.id)
	if _, err := svc.memberFollowups(h.ctx, req, "/v1/matters/read"); !errors.Is(err, ErrIdentity) {
		t.Fatal("selected another member", err)
	}
	for _, command := range []string{"matters", "actions", "policy"} {
		var out bytes.Buffer
		if err := runCLI(h.ctx, []string{command, "--help"}, &out, io.Discard, func(string) string { return "" }); err != nil || !strings.Contains(out.String(), "unit_ref") {
			t.Fatal(out.String(), err)
		}
	}
}

func TestReferenceEntryUnknownSchema(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	state, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.store.followupRoot(h.id), "schema.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(b, []byte(`"version":2`), []byte(`"version":99`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.store.checkReferenceRoom(state); !errors.Is(err, ErrVersion) {
		t.Fatal(err)
	}
}

func TestReferenceEntryMissingSchemaIsNotRecreated(t *testing.T) {
	h, _, _, _ := conversationHarness(t, 1)
	state, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.store.followupRoot(h.id), "schema.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := h.store.checkReferenceRoom(state); !errors.Is(err, ErrVersion) {
		t.Fatal("unmarked records accepted", err)
	}
	if err := h.store.initReferenceFollowups(h.id); !errors.Is(err, ErrVersion) {
		t.Fatal("schema guessed", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("schema recreated", err)
	}
}

func TestReferenceEntryRegisteredActionsRespectNativeType(t *testing.T) {
	for _, kind := range []string{"MergeRequest", "Issue"} {
		t.Run(kind, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			source := referenceTestSource("capabilities")
			source.Reference.Object.Type = kind
			receiveReference(t, h, "capabilities", source)
			inboxRunning(t, h, 1, 1)
			svc := referenceTestService(h)
			svc.referenceResolver = referenceTestResolver
			svc.referenceSpecs = func(string) []platform.ActionSpec {
				specs := []platform.ActionSpec{}
				for _, a := range gitlabReferenceCapabilities().Actions {
					specs = append(specs, a.Spec)
				}
				return specs
			}
			cli := entryCLI(t, svc)
			var inbox referenceInboxPage
			if err := cli([]string{"inbox", "read"}, &inbox); err != nil {
				t.Fatal(err)
			}
			if len(inbox.Followups) != 1 {
				t.Fatal(inbox)
			}
			unit := inbox.Followups[0]
			var detail struct {
				Actions []platform.ActionSpec `json:"registered_actions"`
				Target  storedTarget          `json:"target"`
			}
			if err := cli([]string{"matters", "read", "--object", unit.ObjectRef, "--unit", unit.Ref, "--turn", "1"}, &detail); err != nil {
				t.Fatal(err)
			}
			want := 0
			if kind == "MergeRequest" {
				want = 1
			}
			if len(detail.Actions) != want {
				t.Fatal("incorrect action advertisement", kind, detail.Actions)
			}
			spec := gitlabReferenceCapabilities().Actions[platform.OperationObjectComment].Spec
			if validReferenceSpec(spec, referenceActionRequest{Operation: platform.OperationObjectComment}, detail.Target.Target) != (want == 1) {
				t.Fatal("execution disagrees with advertised native target type")
			}
		})
	}
}
