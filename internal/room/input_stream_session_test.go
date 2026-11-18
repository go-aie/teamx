package room

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestUnifiedDSHPreservesReceiptFailureDiagnostic(t *testing.T) {
	state := historyRoom(1, "只读检查")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	svc := &service{unifiedInputs: true, inputAdapters: storageAdapters(), store: h.store, room: h.addr, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "fixture"}}
	var session *dshSession
	backend := dshBackend{store: h.store, inputService: svc, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{runPrompt: func(ctx context.Context, _ []dsh.ContentBlock) (dsh.TurnResult, error) {
			args, _ := json.Marshal(map[string]any{"stream_ref": roomStreamRef(id)})
			body, _, release, err := session.input.readStream(ctx, "receipt-call", args)
			if err != nil {
				t.Fatal(err)
			}
			release()
			if _, ok := body.Content[0].(mcp.TextContent); !ok {
				t.Fatal("missing input body")
			}
			if err = session.input.receiveGroupBodies(ctx, "receipt-call", []string{strings.Repeat("0", 64)}); !errors.Is(err, ErrRequest) {
				t.Fatal("wrong proof accepted", err)
			}
			return dsh.TurnResult{}, errors.New("Input result could not be verified")
		}}, nil
	}}
	opened, err := backend.OpenSession(h.ctx, adapterOptions(h.store, id))
	if err != nil {
		t.Fatal(err)
	}
	session = opened.(*dshSession)
	defer session.Close(context.Background())
	err = session.Run(h.ctx, UnreadChanged{MemberIdentity: id, LatestSeq: 1})
	if !errors.Is(err, ErrRequest) || !errors.Is(err, inputstream.ErrBusy) || !strings.Contains(err.Error(), "input result receipt confirmation failed") || strings.Count(err.Error(), inputstream.ErrBusy.Error()) != 1 {
		t.Fatal("receipt cause lost or duplicated", err)
	}
	var diagnostic dshDiagnostic
	if err = readSnapshot(filepath.Join(h.store.dir, "agents", id.MembershipID, "dsh-turn.json"), &diagnostic, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostic.Error, "input result receipt confirmation failed") {
		t.Fatal("saved diagnostic lost receipt cause", diagnostic.Error)
	}
}

func TestUnifiedDSHDirectoryRecoverySupplyAndContract(t *testing.T) {
	state := historyRoom(1, "只读检查，不要评论或合并")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	svc := &service{unifiedInputs: true, inputAdapters: storageAdapters(), store: h.store, room: h.addr, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "fixture"}}
	// No material pre-injection; the first bounded history remains a read-only
	// recovery exception, and failed RunPrompt must supply it again.
	var prompts [][]dsh.ContentBlock
	failed := true
	var configured dsh.Options
	b := dshBackend{store: h.store, inputService: svc, recoveryContext: svc.prepareDSHRecoveryContext, open: func(ctx context.Context, o dsh.Options) (dshComponent, error) {
		configured = o
		return &adapterComponent{runPrompt: func(_ context.Context, p []dsh.ContentBlock) (dsh.TurnResult, error) {
			prompts = append(prompts, p)
			if failed {
				return dsh.TurnResult{}, errors.New("retryable fixture failure")
			}
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}}, nil
	}}
	opts := adapterOptions(h.store, id)
	session, err := b.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	if !strings.Contains(configured.Instructions, inputstream.Contract) || strings.Contains(configured.Instructions, "These channels are independent") || configured.ToolEnv["TEAMX_INPUT_CONTRACT"] != inputstream.Contract {
		t.Fatal("old contract in unified Session")
	}
	input := UnreadChanged{MemberIdentity: id, LatestSeq: 1}
	if err := session.Run(h.ctx, input); err == nil {
		t.Fatal("fixture failure hidden")
	}
	failed = false
	if err := session.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := session.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 3 || len(prompts[0]) != 2 || len(prompts[1]) != 2 || len(prompts[2]) != 1 {
		t.Fatal("first-supply scope changed", len(prompts))
	}
	for _, p := range prompts {
		if !strings.Contains(p[0].Text, "Input directory") || strings.Contains(p[0].Text, "只读检查") {
			t.Fatal("body injected before selection")
		}
	}
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("directory/recovery consumed")
	}
	if err := session.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	b.recover = true
	resumed, err := b.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 4 || len(prompts[3]) != 2 {
		t.Fatal("resume did not resupply bounded recovery")
	}
	if err := resumed.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	a, err := h.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	a.Turn.Status = "completed"
	if err := h.store.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	next, err := rotateAdapterForTest(b, opts, true)
	if err != nil {
		t.Fatal(err)
	}
	a.TurnNumber = 2
	a.Turn = &TurnRecord{Number: 2, Status: "running", SnapshotSeq: 1}
	if err := h.store.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	rotated, err := next.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Close(h.ctx)
	if err := rotated.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 5 || len(prompts[4]) != 2 {
		t.Fatal("rotation did not resupply bounded recovery")
	}
	for _, p := range prompts {
		for _, block := range p {
			if strings.Contains(block.Text, "matters source --seq") || strings.Contains(block.Text, "Read normal Room/Inbox") {
				t.Fatal("legacy dual-channel guidance in recovery")
			}
		}
	}
}
func TestUnifiedUnusedProviderDoesNotExpandBaseInformation(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	opts := adapterOptions(h.store, s.storage.id)
	backend := dshBackend{store: h.store, inputService: s.service}
	before, err := backend.componentOptions(opts, dshReference{})
	if err != nil {
		t.Fatal(err)
	}
	schema := marshalMCPToolDefinitions(t, newInputMCPHTTP(true))
	// A registered unused native adapter and its longer field vocabulary remain
	// outside Session instructions and static tool schemas.
	s.service.inputAdapters = storageAdapters()
	key := notification.SourceID{Provider: "approvals", Channel: "changes"}
	s.service.inputAdapters[key] = notificationAdapter{Source: inputFixtureSource{id: key}}
	h.store.notificationAdapters = func(provider string) notification.Capabilities {
		if provider != "approvals" {
			return notificationCapabilities(provider)
		}
		return notification.Capabilities{Input: &notification.InputCapability{Codec: approvalFixtureCodec{}, EncodeStoredInput: func(notification.Item) (inputstream.AppendRequest, error) { return inputstream.AppendRequest{}, nil }}}
	}
	codecs, err := inputStorageCodecs(s.service.inputAdapters, h.store.notificationCapabilities)
	if err != nil || len(codecs) != 3 {
		t.Fatal("third native source was not assembled", len(codecs), err)
	}
	after, err := backend.componentOptions(opts, dshReference{})
	if err != nil {
		t.Fatal(err)
	}
	otherSchema := marshalMCPToolDefinitions(t, newInputMCPHTTP(true))
	if before.Instructions != after.Instructions || string(schema) != string(otherSchema) {
		t.Fatal("unused platform expanded base information")
	}
	if strings.Contains(string(schema), "project_id") || strings.Contains(string(schema), "chat_id") || strings.Contains(string(schema), "operation_id") {
		t.Fatal("native field leaked into public schema")
	}
}

func TestUnifiedDSHDirectoryCannotBypassDurableDiscoveryBudget(t *testing.T) {
	state := historyRoom(1, "只读检查")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	svc := &service{unifiedInputs: true, inputAdapters: storageAdapters(), store: h.store, room: h.addr, info: ServiceInfo{RoomIdentity: h.id}}
	var supplied []dsh.ContentBlock
	b := dshBackend{store: h.store, inputService: svc, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{runPrompt: func(_ context.Context, p []dsh.ContentBlock) (dsh.TurnResult, error) {
			supplied = p
			return dsh.TurnResult{}, nil
		}}, nil
	}}
	session, err := b.OpenSession(h.ctx, adapterOptions(h.store, id))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(h.ctx)
	input := UnreadChanged{MemberIdentity: id, LatestSeq: 1}
	if err := session.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	s := session.(*dshSession).input
	current, err := s.storage.repository.State(s.execution)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.mutate(func(r *inputstream.Repository) error {
		return r.ChargeDiscovery(s.execution, (128<<10)-current.DiscoveryBytes)
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.Run(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	text := materialText(supplied)
	if !strings.Contains(text, "discovery_budget_exhausted") || strings.Contains(text, roomStreamRef(id)) {
		t.Fatal("uncharged directory was supplied", text)
	}
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("directory consumed Room")
	}
}
