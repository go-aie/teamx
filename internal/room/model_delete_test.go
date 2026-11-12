package room

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func retryModelChange(t *testing.T, c *modelController, id string) *modelChangeOperation {
	t.Helper()
	w := managerHTTP(c.manager, "/v1/models/operation", map[string]any{"id": id, "action": "retry", "service_epoch": c.manager.epoch, "caller": humanCaller})
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.manager.mu.Lock()
		running := c.running[id]
		op := *c.operations[id]
		c.manager.mu.Unlock()
		if !running {
			return &op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retry did not finish")
	return nil
}
func TestModelDeletionDefaultExplicitFailureAndRetry(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	a := createDirectConversation(t, m, directSelection("follow"))
	b := createDirectConversation(t, m, directSelection("fixed"))
	aid, bid := modelMember(t, m, a), modelMember(t, m, b)
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "selection", MemberIdentity: bid, Selection: modelSelection{Mode: "explicit", EntryID: "A"}, ExpectedSelectionVersion: 1}))
	asvc := openRoomOK(t, m, a, "open-a")
	bsvc := openRoomOK(t, m, b, "open-b")
	var failing atomic.Bool
	failing.Store(true)
	bsvc.beginPlatformMemberChange = func(context.Context, MemberIdentity) (func(), error) {
		if failing.Load() {
			return nil, ErrBusy
		}
		return func() {}, nil
	}
	// A historical snapshot remains available after removal, with its credential.
	history := filepath.Join(m.roomDir(a.RoomID), "agents", aid.MembershipID, "retained-model-history.json")
	if err := os.WriteFile(history, []byte(`{"original":"A"}`), 0600); err != nil {
		t.Fatal(err)
	}
	op := modelChange(t, c, modelChangeInput{Kind: "delete", EntryID: "A", ReplacementID: "B"})
	catalog := c.service.snapshot()
	if op.Status != "failed" || !op.Committed || catalog.Default != "B" || !catalog.Entries[0].PendingDeletion || m.config.models.gate(aid) != "" || m.config.models.gate(bid) == "" {
		t.Fatalf("partial deletion: %+v", op)
	}
	ca, _ := readManagedRoom(t, m, a)
	cb, _ := readManagedRoom(t, m, b)
	if ca.Members[0].ModelSelection.Mode != "default" || *cb.Members[0].ModelSelection != (modelSelection{Mode: "explicit", EntryID: "B"}) {
		t.Fatal("selection semantics lost")
	}
	if _, err := catalog.resolve(modelSelection{Mode: "explicit", EntryID: "A"}); err == nil {
		t.Fatal("pending model selectable")
	}
	for _, input := range []modelChangeInput{{Kind: "default", EntryID: "A"}, {Kind: "save", Entry: catalog.Entries[0]}} {
		if result := modelChange(t, c, input); result.Committed {
			t.Fatal("pending model accepted")
		}
	}
	opts := SessionOptions{MemberIdentity: aid, StatePath: filepath.Join(asvc.store.dir, "agents", aid.MembershipID, "session.json")}
	before, _, _ := (dshBackend{store: asvc.store}).reference(opts)
	failing.Store(false)
	requireModelComplete(t, retryModelChange(t, c, op.ID))
	after, _, _ := (dshBackend{store: asvc.store}).reference(opts)
	if before.Directory != after.Directory {
		t.Fatal("completed member rotated again")
	}
	for _, entry := range c.service.snapshot().Entries {
		if entry.ID == "A" {
			t.Fatal("entry retained after completion")
		}
	}
	if _, err := os.Stat(history); err != nil {
		t.Fatal(err)
	}
	if _, err := c.service.credentials.get(catalog.Entries[0].CredentialRef); err != nil {
		t.Fatal("historical credential removed")
	}
}

func TestModelDeletionUnusedTemplateAndOnlyDefault(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	missing := modelChange(t, c, modelChangeInput{Kind: "delete", EntryID: "A"})
	if missing.Committed {
		t.Fatal("default deleted without replacement")
	}
	m.teams = map[string]teamDefinition{"blocked": {ID: "blocked", Members: []teamMember{{ID: "worker", ModelSelection: &modelSelection{Mode: "explicit", EntryID: "C"}}}}}
	blocked := modelChange(t, c, modelChangeInput{Kind: "delete", EntryID: "C", ReplacementID: "B"})
	if blocked.Committed || blocked.Message != modelChangeError(errModelTemplateReference) {
		t.Fatal("template reference ignored", blocked)
	}
	m.teams = nil
	c.service.probe = func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		t.Error("unused deletion probed")
		return dsh.ProbeResult{}, errors.New("no probe")
	}
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "delete", EntryID: "C"}))
}

func TestModelDeletionRestartContinuesFixedTarget(t *testing.T) {
	m, c, config := onlineModelManager(t)
	a := createDirectConversation(t, m, directSelection("completed"))
	b := createDirectConversation(t, m, directSelection("pending"))
	aid, bid := modelMember(t, m, a), modelMember(t, m, b)
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "selection", MemberIdentity: bid, Selection: modelSelection{Mode: "explicit", EntryID: "A"}, ExpectedSelectionVersion: 1}))
	inspect := c.inspect
	c.inspect = func(ctx context.Context, opts dsh.Options) (dsh.RuntimeIdentity, error) {
		// Both use the same installation; fail once to retain a durable target.
		return dsh.RuntimeIdentity{}, errors.New("temporarily unavailable")
	}
	op := modelChange(t, c, modelChangeInput{Kind: "delete", EntryID: "A", ReplacementID: "B"})
	if !op.Committed || op.Status != "failed" {
		t.Fatal("deletion should await repair", op)
	}
	_ = aid
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
	control, err := newModelController(context.Background(), restarted, loaded, func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		t.Error("restart reprobed")
		return dsh.ProbeResult{}, ErrInvalid
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.modelControl = control
	control.inspect = inspect
	if execution.gate(bid) != op.ID {
		t.Fatal("unfinished deletion lost admission gate")
	}
	requireModelComplete(t, retryModelChange(t, control, op.ID))
	for _, entry := range control.service.snapshot().Entries {
		if entry.ID == "A" {
			t.Fatal("restart left dangling entry")
		}
	}
	for i, member := range control.operations[op.ID].Members {
		if member.RotationID != op.Members[i].RotationID {
			t.Fatal("retry allocated new generation")
		}
	}
}

func TestModelDeletionEquivalentTargetOnlyReplacesReference(t *testing.T) {
	m, c, _ := onlineModelManager(t)
	room := createDirectConversation(t, m, directSelection("fixed-equivalent"))
	id := modelMember(t, m, room)
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "selection", MemberIdentity: id, Selection: modelSelection{Mode: "explicit", EntryID: "A"}, ExpectedSelectionVersion: 1}))
	before, _ := c.service.snapshot().resolve(modelSelection{Mode: "explicit", EntryID: "A"})
	equivalent, _ := c.service.snapshot().resolve(modelSelection{Mode: "explicit", EntryID: "B"})
	equivalent.ModelParameters = before.ModelParameters
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "save", Entry: equivalent, APIKey: "key-A"}))
	oldCatalog := c.service.snapshot()
	var receipt modelValidation
	if err := readSnapshot(filepath.Join(filepath.Dir(c.service.credentials.dir), "model-validations", before.executionDigest()+".json"), &receipt, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	receipt.EntryID, receipt.Revision = before.ID, before.Revision
	profile, err := c.memberProfileLocked(id)
	if err != nil {
		t.Fatal(err)
	}
	target, err := newModelSessionSnapshot(oldCatalog, memberModelReference{id, modelSelection{Mode: "explicit", EntryID: "A"}, 2}, "session.json.dsh", profile, receipt)
	if err != nil {
		t.Fatal(err)
	}
	op := modelChange(t, c, modelChangeInput{Kind: "delete", EntryID: "A", ReplacementID: "B"})
	requireModelComplete(t, op)
	if len(op.Members) != 1 || op.Members[0].Rotate {
		t.Fatal("equivalent deletion rotated Session")
	}
	saved, _ := readManagedRoom(t, m, room)
	if saved.Members[0].ModelSelection.EntryID != "B" || saved.Members[0].ModelSelectionVersion != 3 {
		t.Fatal("equivalent deletion did not persist reference")
	}
	if !c.memberCompleted(op.Members[0]) {
		t.Fatal("reference progress not derived from disk")
	}
	store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(store.dir, "agents", id.MembershipID, "session.json")}
	backend := dshBackend{store: store, profile: target.Profile, model: &target, models: m.config.models, credentials: c.service.credentials}
	if err = backend.saveModelSnapshot(opts, dshReference{Directory: target.Generation}); err != nil {
		t.Fatal(err)
	}
	generation := "session.json.dsh-" + strings.Repeat("a", 32)
	if _, err = backend.switchSession(opts, target.Generation, generation); err != nil {
		t.Fatal(err)
	}
	var next modelSessionSnapshot
	if err = readSnapshot(filepath.Join(filepath.Dir(opts.StatePath), generation+".model.json"), &next, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if next.EntryID != "B" || next.Selection.EntryID != "B" {
		t.Fatal("ordinary rotation generated a new snapshot referencing deleted A")
	}
}
