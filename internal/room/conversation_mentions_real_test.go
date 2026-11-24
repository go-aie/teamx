//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Uses the configured real model, installed DSH and production CLI/HTTP send.
// Human prompts do not instruct the model whether to mention the operator.
func TestDirectConversationRealAgentMentions(t *testing.T) {
	if os.Getenv("TEAMX_CONVERSATION_DSH_REAL") != "1" {
		t.Skip("set TEAMX_CONVERSATION_DSH_REAL=1, TEAMX_INPUT_COMPARISON_CONFIG and fresh TEAMX_DSH_OUTPUT")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "model": entry.ModelID, "is_direct": true,
		"scope": "real DSH/model and production send in an isolated direct conversation", "external_platform_writes": 0}
	defer imageRealReport(t, out, report)
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, task string }{
		{"reply", "帮我比较先整理需求和先做原型这两种启动方式，各写一句优点和适用情况。"},
		{"decision", "我计划下周发布新版本，有两个方案：周一上线可以早点收到反馈，周五上线能多测几天。请向我提出一个需要我拍板的问题，并简要说明两种选择的取舍。"},
		{"reminder", "请提醒我：发布前还需要核对回滚方案。现在告诉我这件事即可，不安排定时任务。"},
	}
	state := historyRoom(1, cases[0].task)
	state.IsDirect = true
	state.InputLog = &roomLogRoot{Version: 1}
	dir := filepath.Join(out, "room")
	store, err := OpenStore(dir, state.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRoom(state); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	access, _, _ := accessFixture(t)
	access.disabled = true
	bindFixtureAccessSubject(access, humanCaller.ID)
	var svc *service
	server := httptest.NewUnstartedServer(access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		svc.ServeHTTP(w, r)
	})))
	endpoint := "http://" + server.Listener.Addr().String()
	access.internal, access.public = endpoint, endpoint
	server.Start()
	defer server.Close()
	profile := dshProfile{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs,
		Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, Model: entry.materialize(), Instructions: assistantInstructions}
	svc, err = startService(dir, state.RoomID, serviceConfig{unifiedInputs: true, access: access, endpoint: endpoint,
		cliPath: productionFixtureCLI(t), maxTurns: 10, dshTurnTimeout: 90 * time.Second,
		dshProfiles: map[string]dshProfile{"fixture-ignore": profile}}, func(s *service, state RoomSnapshot) error {
		if err := s.loadMembers(state); err != nil {
			return err
		}
		s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
			m := s.newMember(id, runner, restored).(*MemberAgent)
			b := m.Backend.(dshBackend)
			b.open = func(ctx context.Context, opts dsh.Options) (dshComponent, error) {
				if !strings.Contains(opts.Instructions, "Current conversation: direct (one-to-one with the human operator).") {
					return nil, errors.New("real Session is missing direct conversation context")
				}
				opts.Credential = credential
				return dsh.Open(ctx, opts)
			}
			m.Backend = b
			return m
		}
		return s.attachMembers(state)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := svc.close(ctx)
		report["closed_cleanly"] = err == nil
		if err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "enable"); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	report["scenarios"] = &rows
	var since, previousTurn uint64
	for i, scenario := range cases {
		if i > 0 {
			if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: state.RoomIdentity, Caller: humanCaller,
				ClientMessageID: fmt.Sprintf("mention-acceptance-%d", i), Text: scenario.task}); err != nil {
				t.Fatal(err)
			}
		}
		row := map[string]any{"scenario": scenario.name, "task": scenario.task}
		rows = append(rows, row)
		// Wait for the entire turn, so a later extra publication cannot be hidden.
		for {
			members, err := svc.members(ctx)
			if err != nil || len(members.Members) != 1 {
				t.Fatal("cannot inspect real Agent", err)
			}
			execution := members.Members[0].Execution
			if execution != nil && !execution.Active && execution.Agent.TurnNumber > previousTurn {
				row["execution"] = execution
				if execution.Error != "" || execution.Agent.Turn == nil || execution.Agent.Turn.Status != "completed" {
					t.Fatal("real Agent turn did not complete", execution.Error)
				}
				previousTurn = execution.Agent.TurnNumber
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(200 * time.Millisecond):
			}
		}
		room, err := svc.store.LoadRoom()
		if err != nil {
			t.Fatal(err)
		}
		messages := []RoomMessage{}
		for _, message := range room.Messages {
			if message.Seq > since && message.Author.Kind == "agent" {
				messages = append(messages, message)
			}
		}
		row["messages"] = messages
		if len(messages) == 0 {
			t.Fatal("real Agent did not publish a reply", scenario.name)
		}
		row["status"] = "passed"
		for _, message := range messages {
			if newHumanMentionMatcher(access.space.OwnerUserID).matches(message.Text) || strings.HasPrefix(strings.TrimSpace(message.Text), "@") {
				row["status"] = "failed"
				t.Error("unexpected mention in real direct-conversation publication", scenario.name, message.Text)
			}
		}
		since = room.LatestSeq
		t.Log("real Agent publication:", scenario.name, row["status"])
	}
	report["status"] = "passed"
}
