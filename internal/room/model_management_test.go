package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func onlineModelManager(t *testing.T) (*roomManager, *modelController, startupConfig) {
	t.Helper()
	service, settings, initial := modelTestService(t)
	service.launcher.BinaryPath = os.Args[0]
	for _, id := range []string{"A", "B", "C"} {
		entry := modelTestEntry(id)
		entry.CredentialRef = ""
		if _, err := service.save(context.Background(), modelSave{Entry: entry, APIKey: "key-" + id, ExpectedVersion: service.snapshot().Version}); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.OpenFile(initial.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n[runtime.dsh]\nbinary_path = '" + os.Args[0] + "'\n")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	config, err := loadStartupConfig(initial.path, true)
	if err != nil {
		t.Fatal(err)
	}
	config.Runtime.ManagedModels = true
	m := directTestManager(t, filepath.Join(filepath.Dir(initial.path), "data"), config.Runtime)
	m.settings = newSystemSettings(config)
	_ = settings
	execution, err := loadModelExecution(config)
	if err != nil {
		t.Fatal(err)
	}
	m.config.models = execution
	control, err := newModelController(context.Background(), m, config, successfulModelProbe)
	if err != nil {
		t.Fatal(err)
	}
	control.inspect = func(ctx context.Context, o dsh.Options) (dsh.RuntimeIdentity, error) {
		result, _ := successfulModelProbe(ctx, o)
		result.Identity.Agent = dsh.Artifact{}
		return result.Identity, nil
	}
	m.modelControl = control
	m.factory = controlledActivationFactory(nil, nil)
	return m, control, config
}
func modelChange(t *testing.T, c *modelController, input modelChangeInput) *modelChangeOperation {
	t.Helper()
	if input.RequestID == "" {
		input.RequestID, _ = newOpaqueID()
	}
	input.ExpectedVersion = c.service.snapshot().Version
	if _, err := c.submit(input, humanOperator()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.manager.mu.Lock()
		op := c.operations[input.RequestID]
		running := c.running[op.ID]
		copy := *op
		c.manager.mu.Unlock()
		if !running {
			return &copy
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("model operation did not settle")
	return nil
}
func requireModelComplete(t *testing.T, op *modelChangeOperation) {
	t.Helper()
	if op.Status != "completed" {
		t.Fatalf("operation %s: %s; member errors: %v", op.Status, op.Message, func() []string {
			var e []string
			for _, m := range op.Members {
				e = append(e, m.Error)
			}
			return e
		}())
	}
}
func modelMember(t *testing.T, m *roomManager, room roomSummary) MemberIdentity {
	t.Helper()
	saved, _ := readManagedRoom(t, m, room)
	return MemberIdentity{room.RoomIdentity, saved.Members[0].AgentID, saved.Members[0].MembershipID}
}

func TestModelOperationOrderSurvivesPollingAndRestart(t *testing.T) {
	m, c, config := onlineModelManager(t)
	base := time.Now().UTC().Truncate(time.Second)
	for _, op := range []modelChangeOperation{
		{ID: "a-old", CreatedAt: base, Status: "cancelled"},
		{ID: "z-latest", CreatedAt: base.Add(2 * time.Second), Status: "cancelled"},
		{ID: "legacy", Status: "cancelled"},
	} {
		if err := c.persist(&op); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(c.dir, "legacy.json")
	legacyTime := base.Add(time.Second)
	if err := os.Chtimes(path, legacyTime, legacyTime); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		loaded, err := newModelController(context.Background(), m, config, successfulModelProbe)
		if err != nil {
			t.Fatal(err)
		}
		defer loaded.cancel()
		m.modelControl = loaded
		for poll := 0; poll < 20; poll++ {
			response := httptest.NewRecorder()
			m.serveModels(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
			var body struct {
				Operations []struct{ ID string } `json:"operations"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			ids := []string{}
			for _, op := range body.Operations {
				ids = append(ids, op.ID)
			}
			if !reflect.DeepEqual(ids, []string{"z-latest", "legacy", "a-old"}) {
				t.Fatalf("unstable or incorrect latest operation: %v", ids)
			}
		}
	}
}

func TestOnlineModelsDefaultExplicitClosedAndRestart(t *testing.T) {
	m, c, config := onlineModelManager(t)
	a := createDirectConversation(t, m, directSelection("follower"))
	b := createDirectConversation(t, m, directSelection("fixed"))
	aid, bid := modelMember(t, m, a), modelMember(t, m, b)
	fixed := modelChange(t, c, modelChangeInput{Kind: "selection", MemberIdentity: bid, Selection: modelSelection{Mode: "explicit", EntryID: "A"}, ExpectedSelectionVersion: 1})
	requireModelComplete(t, fixed)
	if len(fixed.Members) != 1 || fixed.Members[0].Rotate {
		t.Fatal("same execution rotated", fixed)
	}
	changed := modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "B"})
	requireModelComplete(t, changed)
	if len(changed.Members) != 1 || changed.Members[0].MemberIdentity != aid {
		t.Fatal("explicit A followed default", changed)
	}
	ca, _ := readManagedRoom(t, m, a)
	cb, _ := readManagedRoom(t, m, b)
	if ca.Members[0].ModelSelection.Mode != "default" || cb.Members[0].ModelSelection.EntryID != "A" {
		t.Fatal("selection modes changed")
	}
	entry, _ := c.service.snapshot().resolve(modelSelection{Mode: "explicit", EntryID: "A"})
	entry.ModelID = "A-edited"
	edited := modelChange(t, c, modelChangeInput{Kind: "save", Entry: entry})
	requireModelComplete(t, edited)
	if len(edited.Members) != 1 || edited.Members[0].MemberIdentity != bid {
		t.Fatal("entry revision impact incorrect", edited)
	}
	if len(m.runtimes) != 0 {
		t.Fatal("model update opened a closed room")
	}
	before, _ := os.ReadFile(filepath.Join(m.roomDir(b.RoomID), "agents", bid.MembershipID, "rotation.json"))
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStartupConfig(config.path, true)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := loadModelExecution(loaded)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Runtime.ManagedModels = true
	restarted := directTestManager(t, m.store.dir, loaded.Runtime)
	restarted.settings = newSystemSettings(loaded)
	restarted.config.models = execution
	restarted.modelControl, err = newModelController(context.Background(), restarted, loaded, func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		t.Error("restart reprobed")
		return dsh.ProbeResult{}, errors.New("unexpected")
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(restarted.roomDir(b.RoomID), "agents", bid.MembershipID, "rotation.json"))
	if string(before) != string(after) || execution.gate(aid) != "" || execution.gate(bid) != "" {
		t.Fatal("restart repeated completed application")
	}
	if loaded.Models.Default != "B" {
		t.Fatal("default not durable")
	}
}
func TestOnlineModelsWaitGatePauseAndIndependentPublication(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	room := createDirectConversation(t, m, directSelection("active"))
	id := modelMember(t, m, room)
	entered, finish := make(chan struct{}), make(chan struct{})
	var closed, platform atomic.Int32
	m.factory = controlledActivationFactory(func(_ MemberIdentity, ctx context.Context) error {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			t.Error("change cancelled current turn")
			return ctx.Err()
		}
	}, func(MemberIdentity) { closed.Add(1) })
	svc := openRoomOK(t, m, room, "open-active")
	svc.beginPlatformMemberChange = func(context.Context, MemberIdentity) (func(), error) { platform.Add(1); return func() {}, nil }
	managedPost(t, m, svc, "start-active")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not begin")
	}
	input := modelChangeInput{RequestID: "live-default", Kind: "default", EntryID: "B", ExpectedVersion: c.service.snapshot().Version}
	if _, err := c.submit(input, humanOperator()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.service.snapshot().Default != "B" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.service.snapshot().Default != "B" || platform.Load() != 0 || closed.Load() != 0 {
		t.Fatal("early platform or backend cleanup")
	}
	if _, err := svc.control(context.Background(), apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	// A disjoint model edit can publish while this member waits.
	entry, _ := c.service.snapshot().resolve(modelSelection{Mode: "explicit", EntryID: "C"})
	entry.Name = "renamed-C"
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "save", Entry: entry}))
	close(finish)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := c.running[input.RequestID]
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	op := *c.operations[input.RequestID]
	m.mu.Unlock()

	requireModelComplete(t, &op)
	status, err := svc.target(id.MembershipID).Call[MemberStatus](context.Background(), InspectMember{id})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Paused || (status.SessionTurns == nil || status.SessionTurns.Total != 1) || closed.Load() != 1 || platform.Load() != 1 {
		t.Fatal("intent/count/cleanup changed", status)
	}
}
func TestOnlineModelsValidationConflictCancellationAndPrivacy(t *testing.T) {
	m, c, config := onlineModelManager(t)
	_ = createDirectConversation(t, m, directSelection("still-working"))
	var probes atomic.Int32
	c.service.probe = func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		probes.Add(1)
		return dsh.ProbeResult{}, errors.New("upstream secret must never be returned")
	}
	entry, _ := c.service.snapshot().resolve(modelSelection{Mode: "explicit", EntryID: "A"})
	entry.ModelID = "bad-model"
	before := c.service.snapshot()
	failed := modelChange(t, c, modelChangeInput{Kind: "save", Entry: entry, APIKey: "private-candidate-key"})
	if failed.Status != "failed" || !reflect.DeepEqual(before, c.service.snapshot()) {
		t.Fatal("validation failure published")
	}
	bytes, err := os.ReadFile(filepath.Join(c.dir, failed.ID+".json"))
	if err != nil || strings.Contains(string(bytes), "private-candidate-key") || strings.Contains(string(bytes), "upstream secret") {
		t.Fatal("journal leaked secret", err)
	}
	if _, err := os.Stat(filepath.Join(c.service.credentials.dir, failed.Credential)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed candidate retained", err)
	}
	original, _ := os.ReadFile(config.path)
	_ = os.WriteFile(config.path, append(original, []byte("\n# external edit\n")...), 0600)
	c.service.probe = successfulModelProbe
	conflict := modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "B"})
	if conflict.Status != "failed" || conflict.Committed {
		t.Fatal("external config overwritten")
	}
	m.mu.Lock()
	for _, op := range c.operations {
		for _, member := range op.Members {
			if m.config.models.gate(member.MemberIdentity) != "" {
				t.Fatal("definite failed commit left gate")
			}
		}
	}
	m.mu.Unlock()
}
func TestOnlineModelsFailureIsolationRetryAndNoDuplicateRotation(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	a := createDirectConversation(t, m, directSelection("good"))
	b := createDirectConversation(t, m, directSelection("bad"))
	aid, bid := modelMember(t, m, a), modelMember(t, m, b)
	var fail atomic.Bool
	fail.Store(true)
	m.factory = controlledActivationFactory(nil, nil)
	asvc := openRoomOK(t, m, a, "open-good")
	bsvc := openRoomOK(t, m, b, "open-bad")
	bsvc.beginPlatformMemberChange = func(context.Context, MemberIdentity) (func(), error) {
		if fail.Load() {
			return nil, ErrBusy
		}
		return func() {}, nil
	}
	op := modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "B"})
	if op.Status != "failed" || !op.Committed || m.config.models.gate(aid) != "" || m.config.models.gate(bid) == "" {
		t.Fatal("failure not isolated", op)
	}
	opts := SessionOptions{MemberIdentity: aid, StatePath: filepath.Join(asvc.store.dir, "agents", aid.MembershipID, "session.json")}
	before, _, err := (dshBackend{store: asvc.store}).reference(opts)
	if err != nil {
		t.Fatal(err)
	}
	// The completed member can independently move to C before the failed
	// member retries B. Its older completion must survive rotation.json reuse.
	newer := modelChange(t, c, modelChangeInput{Kind: "selection", MemberIdentity: aid, Selection: modelSelection{Mode: "explicit", EntryID: "C"}, ExpectedSelectionVersion: 1})
	requireModelComplete(t, newer)
	before, _, err = (dshBackend{store: asvc.store}).reference(opts)
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(false)
	req := map[string]any{"id": op.ID, "action": "retry", "service_epoch": m.epoch, "caller": humanCaller}
	w := managerHTTP(m, "/v1/models/operation", req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := c.running[op.ID]
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	result := *c.operations[op.ID]
	m.mu.Unlock()
	requireModelComplete(t, &result)
	after, _, err := (dshBackend{store: asvc.store}).reference(opts)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("retry rerotated completed member", err)
	}
	duplicate, err := c.submit(op.Input, humanOperator())
	if err != nil || duplicate == nil {
		t.Fatal("stable request lost", err)
	}
	changed := op.Input
	changed.EntryID = "C"
	if _, err := c.submit(changed, humanOperator()); !errors.Is(err, ErrConflict) {
		t.Fatal("request ID accepted different input", err)
	}
}
func TestWorkspaceSnapshotComparisonExcludesOnlyModelReference(t *testing.T) {
	a := managedRoomConfig{Members: []managedMember{{Name: "same", Workspace: "workspace", ModelSelection: &modelSelection{Mode: "default"}, ModelSelectionVersion: 1}}}
	b := a
	b.Members = append([]managedMember(nil), a.Members...)
	b.Members[0].ModelSelection = &modelSelection{Mode: "explicit", EntryID: "B"}
	b.Members[0].ModelSelectionVersion = 2
	if !equalWorkspaceRoomConfig(a, b) {
		t.Fatal("model reference invalidated workspace receipt")
	}
	b.Members[0].Workspace = "other"
	if equalWorkspaceRoomConfig(a, b) {
		t.Fatal("workspace change bypassed authorization")
	}
}

func TestOnlineModelsRestartPendingTargetAndCommitUnknown(t *testing.T) {
	m, c, config := onlineModelManager(t)
	room := createDirectConversation(t, m, directSelection("pending"))
	id := modelMember(t, m, room)
	svc := openRoomOK(t, m, room, "open-pending")
	svc.beginPlatformMemberChange = func(context.Context, MemberIdentity) (func(), error) { return nil, ErrBusy }
	op := modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "B"})
	if op.Status != "failed" || !op.Committed {
		t.Fatal("failed application not durable")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStartupConfig(config.path, true)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := loadModelExecution(loaded)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Runtime.ManagedModels = true
	restarted := directTestManager(t, m.store.dir, loaded.Runtime)
	restarted.settings = newSystemSettings(loaded)
	restarted.config.models = execution
	restarted.modelControl, err = newModelController(context.Background(), restarted, loaded, func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		t.Error("replayed probe")
		return dsh.ProbeResult{}, ErrRequest
	})
	if err != nil {
		t.Fatal(err)
	}
	if execution.gate(id) != op.ID {
		t.Fatal("pending member was not frozen on restart")
	}
	restarted.modelControl.inspect = c.inspect
	restarted.factory = controlledActivationFactory(nil, nil)
	_ = openRoomOK(t, restarted, room, "reopen-pending")
	w := managerHTTP(restarted, "/v1/models/operation", map[string]any{"id": op.ID, "action": "retry", "service_epoch": restarted.epoch, "caller": humanCaller})
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		restarted.mu.Lock()
		running := restarted.modelControl.running[op.ID]
		restarted.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	restarted.mu.Lock()
	result := *restarted.modelControl.operations[op.ID]
	restarted.mu.Unlock()
	requireModelComplete(t, &result)
	if execution.gate(id) != "" {
		t.Fatal("retry retained gate")
	}
}

func TestOnlineModelsUnknownConfigKeepsOldSessionUntilReconciled(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	room := createDirectConversation(t, m, directSelection("unknown"))
	id := modelMember(t, m, room)
	var closed atomic.Int32
	m.factory = controlledActivationFactory(nil, func(MemberIdentity) { closed.Add(1) })
	svc := openRoomOK(t, m, room, "open-unknown")
	managedPost(t, m, svc, "create-session")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := svc.target(id.MembershipID).Call[MemberStatus](context.Background(), InspectMember{id})
		if status.Agent.TurnNumber > 0 && !status.Active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	original := c.service.document.ops.syncDir
	c.service.document.ops.syncDir = func(string) error { return errors.New("durability injected") }
	op := modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "B"})
	if op.Committed || op.Target == nil || m.config.models.gate(id) == "" || closed.Load() != 0 {
		t.Fatal("unknown commit destroyed old session")
	}
	c.service.document.mu.Lock()
	c.service.document.ops.syncDir = original
	c.service.document.mu.Unlock()
	w := managerHTTP(m, "/v1/models/operation", map[string]any{"id": op.ID, "action": "retry", "service_epoch": m.epoch, "caller": humanCaller})
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := c.running[op.ID]
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	result := *c.operations[op.ID]
	m.mu.Unlock()
	requireModelComplete(t, &result)
	if closed.Load() != 1 || m.config.models.catalog().Default != "B" {
		t.Fatal("retry did not reconcile exact target")
	}
}

func TestOnlineModelsCreationDuringValidationUsesPublishedDefault(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	entered, release := make(chan struct{}), make(chan struct{})
	c.service.probe = func(ctx context.Context, o dsh.Options) (dsh.ProbeResult, error) {
		close(entered)
		select {
		case <-release:
			return successfulModelProbe(ctx, o)
		case <-ctx.Done():
			return dsh.ProbeResult{}, ctx.Err()
		}
	}
	input := modelChangeInput{RequestID: "creation-window", Kind: "default", EntryID: "B", ExpectedVersion: c.service.snapshot().Version}
	if _, err := c.submit(input, humanOperator()); err != nil {
		t.Fatal(err)
	}
	<-entered
	room := createDirectConversation(t, m, directSelection("created-during-probe"))
	id := modelMember(t, m, room)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := c.running[input.RequestID]
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	op := *c.operations[input.RequestID]
	m.mu.Unlock()
	requireModelComplete(t, &op)
	if len(op.Members) != 1 || op.Members[0].MemberIdentity != id || op.Members[0].Target.EntryID != "B" {
		t.Fatal("concurrent creation missed publication")
	}
	later := createDirectConversation(t, m, directSelection("created-after-publication"))
	saved, _ := readManagedRoom(t, m, later)
	m.mu.Lock()
	target, err := m.activationConfigUnlockedForModelTest(later.RoomIdentity, saved)
	m.mu.Unlock()
	if err != nil || target.EntryID != "B" {
		t.Fatal("new member did not resolve published default", err)
	}
}

// Resolve only this test's saved profile; activationConfig takes manager.mu itself.
func (m *roomManager) activationConfigUnlockedForModelTest(id RoomIdentity, saved managedRoomConfig) (modelSessionSnapshot, error) {
	member := saved.Members[0]
	profile := saved.Runtime.profile()
	profile.Workspace = filepath.Join(m.roomDir(id.RoomID), member.Workspace)
	return m.config.models.target(MemberIdentity{id, member.AgentID, member.MembershipID}, *member.ModelSelection, member.ModelSelectionVersion, profile)
}

func TestOnlineModelsSameRoomPreservesIndependentExecutionTargets(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	team := testTeamDefinition(t)
	team.Runtime = m.defaultRuntime
	team.Members = append(team.Members, teamMember{ID: "writer", Name: "Writer", Instructions: "write", ModelSelection: &modelSelection{Mode: "explicit", EntryID: "A"}})
	m.teams[team.ID] = team
	room := createTeamRoom(t, m, "mixed-model-room")
	op := modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "B"})
	requireModelComplete(t, op)
	if len(op.Members) != 1 || op.Members[0].AgentID != "reviewer" {
		t.Fatal("same-room explicit selection followed default")
	}
	config, err := m.activationConfig(room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := readManagedRoom(t, m, room)
	if config.modelTargets[saved.Members[0].Runner.Profile].EntryID != "B" || config.modelTargets[saved.Members[1].Runner.Profile].EntryID != "A" {
		t.Fatal("member profiles collapsed to shared model")
	}
	// Each member receives a distinct backend/credential binding in the same service.
	opts := map[string]string{}
	for _, member := range saved.Members {
		target := config.modelTargets[member.Runner.Profile]
		credential, err := m.config.models.Credentials.get(target.CredentialRef)
		if err != nil {
			t.Fatal(err)
		}
		opts[member.AgentID] = target.Profile.Model.ModelID + ":" + credential.Value
	}
	if opts["reviewer"] != "B:key-B" || opts["writer"] != "A:key-A" {
		t.Fatal("same-room credentials mixed", opts)
	}
}

func TestOnlineModelsCancelBeforePublicationKeepsOriginalConfig(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	c.service.probe = func(ctx context.Context, o dsh.Options) (dsh.ProbeResult, error) {
		probes.Add(1)
		close(entered)
		select {
		case <-release:
			return successfulModelProbe(ctx, o)
		case <-ctx.Done():
			return dsh.ProbeResult{}, ctx.Err()
		}
	}
	before := c.service.snapshot()
	input := modelChangeInput{RequestID: "cancel-validation", Kind: "default", EntryID: "B", ExpectedVersion: before.Version}
	if _, err := c.submit(input, humanOperator()); err != nil {
		t.Fatal(err)
	}
	<-entered
	w := managerHTTP(m, "/v1/models/operation", map[string]any{"id": input.RequestID, "action": "cancel", "service_epoch": m.epoch, "caller": humanCaller})
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := c.running[input.RequestID]
		m.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.mu.Lock()
	op := *c.operations[input.RequestID]
	m.mu.Unlock()
	if op.Status != "cancelled" || op.Committed || !reflect.DeepEqual(c.service.snapshot(), before) {
		t.Fatal("cancelled operation published")
	}
	if _, err := c.submit(input, humanOperator()); err != nil {
		t.Fatal(err)
	}
	if probes.Load() != 1 {
		t.Fatal("cancelled request was replayed")
	}
}
