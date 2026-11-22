package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func reasoningRequest(m *roomManager, c *modelController, id MemberIdentity, version uint64, effort string) memberReasoningInput {
	return memberReasoningInput{MemberIdentity: id, Caller: Caller{Author: Author{Kind: "human", ID: humanOperator()}}, ServiceEpoch: m.epoch, ExpectedVersion: c.service.snapshot().Version, ExpectedSelectionVersion: version, ReasoningEffort: effort}
}

func TestMemberReasoningPersistsAndPreservesExistingSession(t *testing.T) {
	parallelFixtureTest(t)
	m, c, startup := onlineModelManager(t)
	entry := c.service.snapshot().Entries[0]
	entry.ReasoningEffort = "low"
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "save", Entry: entry}))
	a := createDirectConversation(t, m, directSelection("reasoning-A"))
	b := createDirectConversation(t, m, directSelection("reasoning-B"))
	id := modelMember(t, m, a)
	fixed := modelChange(t, c, modelChangeInput{Kind: "selection", MemberIdentity: id, Selection: modelSelection{Mode: "explicit", EntryID: "A"}, ExpectedSelectionVersion: 1})
	requireModelComplete(t, fixed)
	saved, _ := readManagedRoom(t, m, a)
	original, err := m.activationConfigUnlockedForModelTest(a.RoomIdentity, saved)
	if err != nil {
		t.Fatal(err)
	}
	catalogBefore := c.service.snapshot()
	configBefore, _ := os.ReadFile(startup.path)
	store, err := openStoreWithCatalog(m.roomDir(a.RoomID), a.RoomID, nil, m.platformCatalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	opts := adapterOptions(store, id)
	backend := dshBackend{store: store, models: m.config.models, credentials: m.config.models.Credentials, model: &original, profile: original.Profile,
		open: func(context.Context, dsh.Options) (dshComponent, error) {
			t.Fatal("configuration launched DSH")
			return nil, nil
		}}
	response := managerHTTP(m, "/v1/models/member-reasoning", reasoningRequest(m, c, id, 2, "high"))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var view modelMemberView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || view.Selection.ReasoningEffort != "high" || view.Version != 3 {
		t.Fatal(view, err)
	}
	// A backend assembled before this edit must still use the new selection on
	// its very first open. PrepareOpen freezes state without starting a model.
	host := &sessionHost{dshSession: &dshSession{backend: backend, opts: opts}}
	launch, err := host.PrepareOpen(context.Background())
	if err != nil || launch.Model.ReasoningEffort != "high" {
		t.Fatal(launch.Model, err)
	}
	ref, _, err := host.backend.reference(opts)
	if err != nil {
		t.Fatal(err)
	}
	frozenPath := filepath.Join(filepath.Dir(opts.StatePath), ref.Directory+".model.json")
	frozenBefore, err := os.ReadFile(frozenPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.saveMemberReasoning(reasoningRequest(m, c, id, 3, "off"), humanOperator()); err != nil {
		t.Fatal(err)
	}
	frozenAfter, _ := os.ReadFile(frozenPath)
	if string(frozenBefore) != string(frozenAfter) {
		t.Fatal("existing generation was edited")
	}
	restored, err := host.backend.restoreModelSnapshot(opts, ref)
	if err != nil || restored.profile.Model.ReasoningEffort != "high" {
		t.Fatal("resume lost original reasoning", err)
	}
	latest, err := backend.latestModelSelection(opts)
	if err != nil || latest.model.Profile.Model.ReasoningEffort != "off" {
		t.Fatal("new generation lost override", err)
	}
	other, _ := readManagedRoom(t, m, b)
	otherTarget, err := m.activationConfigUnlockedForModelTest(b.RoomIdentity, other)
	if err != nil || otherTarget.Profile.Model.ReasoningEffort != "low" {
		t.Fatal("another member changed", err)
	}
	configAfter, _ := os.ReadFile(startup.path)
	if !reflect.DeepEqual(catalogBefore, c.service.snapshot()) || string(configBefore) != string(configAfter) || len(m.runtimes) != 0 {
		t.Fatal("reasoning changed global config or opened a room")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStartupConfig(startup.path, true)
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
	restarted.modelControl, err = newModelController(context.Background(), restarted, loaded, successfulModelProbe)
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = readManagedRoom(t, restarted, a)
	selection := *saved.Members[0].ModelSelection
	if selection.Mode != "explicit" || selection.EntryID != "A" || selection.ReasoningEffort != "off" {
		t.Fatal("member selection lost on restart", selection)
	}
	future, err := restarted.activationConfigUnlockedForModelTest(a.RoomIdentity, saved)
	if err != nil || future.Profile.Model.ModelID != "A" || future.Profile.Model.ReasoningEffort != "off" {
		t.Fatal(future, err)
	}
	active, err := restarted.activationConfig(a.RoomIdentity)
	if err != nil || active.modelTargets[saved.Members[0].Runner.Profile].Profile.Model.ReasoningEffort != "high" {
		t.Fatal("restart replaced the existing generation", err)
	}
	// Clearing the override returns future Sessions to the model default.
	_, err = restarted.modelControl.saveMemberReasoning(reasoningRequest(restarted, restarted.modelControl, id, 4, ""), humanOperator())
	if err != nil {
		t.Fatal(err)
	}
	saved, _ = readManagedRoom(t, restarted, a)
	future, err = restarted.activationConfigUnlockedForModelTest(a.RoomIdentity, saved)
	if err != nil || future.Profile.Model.ReasoningEffort != "low" {
		t.Fatal("inherit did not restore model default", err)
	}
}

func TestMemberReasoningRejectsStaleAndForeignWrites(t *testing.T) {
	parallelFixtureTest(t)
	m, c, _ := onlineModelManager(t)
	room := createDirectConversation(t, m, directSelection("reasoning"))
	id := modelMember(t, m, room)
	valid := reasoningRequest(m, c, id, 1, "high")
	for name, mutate := range map[string]func(*memberReasoningInput){
		"epoch":             func(v *memberReasoningInput) { v.ServiceEpoch = "stale" },
		"caller":            func(v *memberReasoningInput) { v.Caller.ID = "someone-else" },
		"agent caller":      func(v *memberReasoningInput) { v.Caller.Kind = "agent" },
		"membership":        func(v *memberReasoningInput) { v.MembershipID = "foreign" },
		"catalog version":   func(v *memberReasoningInput) { v.ExpectedVersion++ },
		"selection version": func(v *memberReasoningInput) { v.ExpectedSelectionVersion++ },
		"unknown effort":    func(v *memberReasoningInput) { v.ReasoningEffort = "extreme" },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if _, err := c.saveMemberReasoning(input, humanOperator()); err == nil {
				t.Fatal("invalid write accepted")
			}
		})
	}
	m.config.models.setGate(id, "model-operation")
	if _, err := c.saveMemberReasoning(valid, humanOperator()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	m.config.models.setGate(id, "")
	saved, _ := readManagedRoom(t, m, room)
	if saved.Members[0].ModelSelectionVersion != 1 || saved.Members[0].ModelSelection.ReasoningEffort != "" {
		t.Fatal("rejected writes changed selection")
	}
}

func TestMemberReasoningProtocolValidationAndCatalogImpacts(t *testing.T) {
	catalog := modelTestCatalog()
	catalog.Entries[0].ModelID = "deepseek-v4-flash"
	if _, err := (modelSelection{Mode: "default", ReasoningEffort: "xhigh"}).executionEntry(catalog.Entries[0]); err == nil {
		t.Fatal("unsupported DeepSeek effort accepted")
	}
	id := MemberIdentity{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "member"}
	refs := []memberModelReference{{id, modelSelection{Mode: "default", ReasoningEffort: "high"}, 1}}
	after := catalog.clone()
	after.Entries[0].ReasoningEffort = "low"
	impacts, err := modelImpacts(catalog, after, refs)
	if err != nil || len(impacts) != 0 {
		t.Fatal("global default changed an override", impacts, err)
	}
	refs[0].Selection.ReasoningEffort = ""
	impacts, err = modelImpacts(catalog, after, refs)
	if err != nil || len(impacts) != 1 {
		t.Fatal("inherited default did not change", impacts, err)
	}
}
