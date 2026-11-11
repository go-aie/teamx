package room

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestWebRoomAssetsAndBrowserOrigin(t *testing.T) {
	svc, client := testService(t)
	var config struct {
		ServiceInfo
		Operator string   `json:"operator"`
		Profiles []string `json:"profiles"`
	}
	if err := client.request(context.Background(), "/v1/ui", nil, &config); err != nil || config.ServiceInfo != svc.info || config.Operator == "" || len(config.Profiles) == 0 {
		t.Fatal(config, err)
	}
	for _, origin := range []string{client.endpoint, "https://unrelated.example", "null"} {
		req, _ := http.NewRequest(http.MethodGet, client.endpoint+"/v1/ui", nil)
		req.Header.Set("Origin", origin)
		response, err := client.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := http.StatusForbidden
		if origin == client.endpoint {
			want = http.StatusOK
		}
		if response.StatusCode != want {
			t.Fatalf("origin %s: %d, want %d", origin, response.StatusCode, want)
		}
	}
	// A browser on another origin cannot append messages to the local Room.
	data, _ := json.Marshal(apiRequest{ServiceInfo: svc.info, Caller: humanCaller, Text: "cross origin", ClientMessageID: "blocked"})
	req, _ := http.NewRequest(http.MethodPost, client.endpoint+"/v1/send", strings.NewReader(string(data)))
	req.Header.Set("Origin", "https://unrelated.example")
	response, err := client.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	list, err := svc.members(context.Background())
	if response.StatusCode != http.StatusForbidden || err != nil || list.LatestSeq != 0 {
		t.Fatal(response.StatusCode, list, err)
	}
}

func TestWebPreviewPreservesMemberUnread(t *testing.T) {
	svc, client := testService(t)
	addFixtureMemberCLI(t, svc, client, "reviewer")
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, Text: "<script>private-looking input</script>", ClientMessageID: "web-message"}
	var posted PostReceipt
	if err := client.request(context.Background(), "/v1/send", &req, &posted); err != nil {
		t.Fatal(err)
	}
	// The UI reads as the operator, never with a member's consuming identity.
	req.Mode, req.Latest = ReadLatest, 50
	var page ReadPage
	if err := client.request(context.Background(), "/v1/read", &req, &page); err != nil {
		t.Fatal(err)
	}
	list, err := svc.members(context.Background())
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Seq != posted.Seq || page.ReadReceipt != "" || list.Members[0].ReadThrough != 0 || list.Members[0].UnreadCount != 1 {
		t.Fatal(page, list, err)
	}
}

func TestMemberActivityIsOperatorOnlyAndGenerationBound(t *testing.T) {
	svc, client := testService(t)
	a := addFixtureMemberCLI(t, svc, client, "A")
	b := addFixtureMemberCLI(t, svc, client, "B")
	for _, req := range []apiRequest{a, b} {
		id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
		svc.store.mu.Lock()
		path, err := svc.store.dshPath(id, "dsh-turn.json", false)
		svc.store.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := saveDSHDiagnostic(path, dshDiagnostic{Result: dsh.TurnResult{StopReason: "end_turn", Text: "private-" + id.AgentID,
			TextTruncated: true, Tools: []dsh.ToolUpdate{{ID: "tool-1", Status: "completed"}}, ToolsTruncated: true}}); err != nil {
			t.Fatal(err)
		}
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: "A", MembershipID: a.Caller.MembershipID}
	var got memberActivity
	body, _ := json.Marshal(req)
	response, err := client.http.Post(client.endpoint+"/v1/member/activity", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(response.Status, response.Header)
	}
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Member.AgentID != "A" || got.LastOutput == nil || got.LastOutput.Result.Text != "private-A" || got.UpdatedAt == nil ||
		!got.LastOutput.Result.TextTruncated || !got.LastOutput.Result.ToolsTruncated || got.Member.Execution.Agent.TurnNumber != 0 {
		t.Fatal(got)
	}
	// Even the member itself cannot use this operator endpoint.
	for _, caller := range []Caller{a.Caller, b.Caller} {
		denied := req
		denied.Caller = caller
		if err := client.request(context.Background(), "/v1/member/activity", &denied, &got); err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatal("member read private diagnostics", caller, err)
		}
	}
	var members serviceMembers
	if err := client.request(context.Background(), "/v1/member/list", &req, &members); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(members)
	if strings.Contains(string(data), "private-A") || strings.Contains(string(data), "private-B") || members.LatestSeq != 0 {
		t.Fatal("private output leaked into list or Room history", members)
	}
	if _, err := svc.remove(req); err != nil {
		t.Fatal(err)
	}
	addFixtureMemberCLI(t, svc, client, "A")
	if err := client.request(context.Background(), "/v1/member/activity", &req, &got); err == nil {
		t.Fatal("removed generation could read orphan diagnostics")
	}
}

func TestMemberActivityMissingOrInvalidOutputDoesNotAffectRoom(t *testing.T) {
	svc, client := testService(t)
	a := addFixtureMemberCLI(t, svc, client, "A")
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: "A", MembershipID: a.Caller.MembershipID}
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: "A", MembershipID: req.MembershipID}
	svc.store.mu.Lock()
	path, err := svc.store.dshPath(id, "dsh-turn.json", false)
	svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var got memberActivity
	if err := client.request(context.Background(), "/v1/member/activity", &req, &got); err != nil || got.LastOutput != nil || got.OutputError != "" {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("viewing missing output must not create it", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"broken JSON", strings.Repeat("x", int(maxPrivateFileBytes)+1)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		got = memberActivity{}
		if err := client.request(context.Background(), "/v1/member/activity", &req, &got); err != nil || got.LastOutput != nil || got.OutputError == "" || got.Member.AgentID != "A" {
			t.Fatal(got, err)
		}
	}
	// Optional diagnostic failures do not freeze authoritative Room writes.
	req.Text, req.ClientMessageID = "still writable", "after-diagnostic-error"
	var posted PostReceipt
	if err := client.request(context.Background(), "/v1/send", &req, &posted); err != nil {
		t.Fatal(err)
	}
	list, err := svc.members(context.Background())
	if err != nil || list.Members[0].ReadThrough != 0 || list.Members[0].UnreadCount != 1 || list.Members[0].Execution.Agent.TurnNumber != 0 {
		t.Fatal(list, err)
	}
}

func TestMemberActivityLiveRunWithoutListener(t *testing.T) {
	svc, err := newFixtureService(t.TempDir(), "live-activity", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: "A", Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}
	member, err := svc.add(req)
	if err != nil {
		t.Fatal(err)
	}
	req.MembershipID = member.MembershipID
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: "A", MembershipID: member.MembershipID}
	opts := adapterOptions(svc.store, id)
	// An old completed output must not replace the live turn, even when its
	// diagnostic file is unreadable. Reading never writes a live file.
	path := filepath.Join(filepath.Dir(opts.StatePath), "dsh-turn.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken old output"), 0600); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(finish) }) }
	defer release()
	current := dsh.TurnResult{Text: "partial", Thought: "checking files", Tools: []dsh.ToolUpdate{{ID: "tool", Title: "shell", Input: `{"command":"go test"}`, Status: "in_progress"}}}
	current.Events = []dsh.ActivityEvent{{Seq: 1, Kind: "thought", Text: "checking files"}, {Seq: 2, Kind: "tool", Tool: &current.Tools[0]}, {Seq: 3, Kind: "text", Text: "partial"}}
	finished := current
	finished.Text, finished.StopReason = "finished", "end_turn"
	finished.Events = append(append([]dsh.ActivityEvent{}, current.Events...), dsh.ActivityEvent{Seq: 4, Kind: "text", Text: "finished"})
	session := &dshSession{opts: opts, backend: dshBackend{store: svc.store}, component: &adapterComponent{
		activity: func() (dsh.TurnResult, bool) { return current, true },
		run: func(context.Context, string) (dsh.TurnResult, error) {
			close(entered)
			<-finish
			return finished, nil
		},
	}}
	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background(), UnreadChanged{MemberIdentity: id}) }()
	<-entered
	read := func(request apiRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(request)
		response := httptest.NewRecorder()
		svc.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/member/activity", strings.NewReader(string(body))))
		return response
	}
	response := read(req)
	var got memberActivity
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(response.Body.Bytes(), &got) != nil || got.CurrentOutput == nil || got.CurrentOutput.Result.Thought != "checking files" || got.CurrentOutput.Result.Tools[0].Input == "" || got.LastOutput != nil || got.OutputError != "" {
		t.Fatal(response.Code, response.Body.String())
	}
	if len(got.CurrentOutput.Result.Events) != 3 || got.CurrentOutput.Result.Events[1].Tool.Input != `{"command":"go test"}` {
		t.Fatal("live event order lost", got.CurrentOutput)
	}
	denied := req
	denied.Caller = agentCaller("A", member.MembershipID)
	if read(denied).Code == 200 {
		t.Fatal("member read live output")
	}
	before, err := svc.members(context.Background())
	if err != nil || before.LatestSeq != 0 || before.Members[0].ReadThrough != 0 || before.Members[0].Execution.Agent.TurnNumber != 0 {
		t.Fatal(before, err)
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not settle")
	}
	if _, ok := svc.store.activeDSH.Load(id); ok {
		t.Fatal("settled live entry retained")
	}
	response = read(req)
	got = memberActivity{}
	if json.Unmarshal(response.Body.Bytes(), &got) != nil || got.CurrentOutput != nil || got.LastOutput == nil || got.LastOutput.Result.Text != "finished" {
		t.Fatal(response.Body.String())
	}
	if len(got.LastOutput.Result.Events) != 4 || got.LastOutput.Result.Events[3].Seq != 4 {
		t.Fatal("saved event order lost", got.LastOutput)
	}
	if _, err := svc.remove(req); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.add(req); err != nil {
		t.Fatal(err)
	}
	if read(req).Code == 200 {
		t.Fatal("retired generation read activity")
	}
}
