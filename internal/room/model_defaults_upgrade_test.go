package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestHostedDefaultUpgradeAppliesExistingMembersAndPreservesChoices(t *testing.T) {
	parallelFixtureTest(t)
	m, c, config := onlineModelManager(t)
	legacy := modelTestEntry("daily")
	legacy.ModelParameters = ModelParameters{Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel}
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "save", Entry: legacy, APIKey: "owner-gateway-key"}))
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "daily"}))
	room := createDirectConversation(t, m, directSelection("existing"))
	id := modelMember(t, m, room)
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(m.roomDir(id.RoomID), "agents", id.MembershipID, "session.json")}
	store, err := openStoreWithCatalog(m.roomDir(id.RoomID), id.RoomID, nil, m.platformCatalog)
	if err != nil {
		t.Fatal(err)
	}
	// A failed turn must retain its history and enabled intent through repair.
	agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 7, Turn: &TurnRecord{Number: 7, Status: "failed", Error: "DSH stopped: max_tokens"}}
	if err := store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	if err := store.saveScheduling(memberScheduling{Version: schedulingVersion, MemberIdentity: id, Enabled: true, Used: 7, SessionGeneration: "session.json.dsh"}); err != nil {
		t.Fatal(err)
	}
	if err := store.save(opts.StatePath, sessionReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dshStateDir(opts.StatePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dshStateDir(opts.StatePath), "backend.json"), []byte(`{"version":1,"sessionId":"old","configSHA256":"test","stopped":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := c.service.snapshot()
	if err := c.upgradeHostedDefaultModel(humanOperator()); err != nil {
		t.Fatal(err)
	}
	after := c.service.snapshot()
	entry, err := after.resolve(modelSelection{Mode: "default"})
	if err != nil || !reflect.DeepEqual(entry.ModelParameters, hostedDefaultModelParameters()) {
		t.Fatal("advanced defaults were not applied", entry, err)
	}
	if entry.CredentialRef != before.Entries[len(before.Entries)-1].CredentialRef || after.Default != before.Default {
		t.Fatal("upgrade changed credential or default selection")
	}
	for i := 0; i < len(before.Entries)-1; i++ {
		if !reflect.DeepEqual(before.Entries[i], after.Entries[i]) {
			t.Fatal("upgrade changed a custom model")
		}
	}
	ref, _, err := readSessionReference(opts.StatePath, id)
	if err != nil || !ref.Pending || ref.Directory == "session.json.dsh" {
		t.Fatal("member did not receive a fresh generation", ref, err)
	}
	frozen, err := readModelSessionSnapshot(filepath.Join(filepath.Dir(opts.StatePath), ref.Directory+".model.json"), id, ref.Directory)
	if err != nil || !reflect.DeepEqual(parametersFromLegacyModel(frozen.Profile.Model), hostedDefaultModelParameters()) {
		t.Fatal("member kept old execution parameters", frozen.Profile.Model, err)
	}
	store, err = openStoreWithCatalog(m.roomDir(id.RoomID), id.RoomID, nil, m.platformCatalog)
	if err != nil {
		t.Fatal(err)
	}
	scheduling, err := store.loadScheduling(id)
	if err != nil || !scheduling.Enabled || scheduling.Used != 7 {
		t.Fatal("upgrade lost enable intent or counters", scheduling, err)
	}
	saved, err := store.LoadAgent(id)
	if err != nil || !reflect.DeepEqual(saved, agent) {
		t.Fatal("upgrade rewrote failed task history", err)
	}
	_ = store.Close()
	if len(m.runtimes) != 0 {
		t.Fatal("upgrade opened an unsolicited worker")
	}
	// A later manual edit remains authoritative, including after restart.
	entry.MaxTokens = 16384
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "save", Entry: entry}))
	version := c.service.snapshot().Version
	if err := c.upgradeHostedDefaultModel(humanOperator()); err != nil || c.service.snapshot().Version != version {
		t.Fatal("completed upgrade ran again", err)
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
	control, err := newModelController(context.Background(), restarted, loaded, func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		t.Error("completed upgrade reprobed on restart")
		return dsh.ProbeResult{}, errors.New("unexpected probe")
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.modelControl = control
	if err := control.upgradeHostedDefaultModel(humanOperator()); err != nil || control.service.snapshot().Version != version {
		t.Fatal("restart repeated upgrade", err)
	}
}

func TestHostedDefaultUpgradeValidationFailureCanRetryWithoutPublishing(t *testing.T) {
	parallelFixtureTest(t)
	_, c, _ := onlineModelManager(t)
	legacy := modelTestEntry("daily")
	legacy.ModelParameters = ModelParameters{Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel}
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "save", Entry: legacy, APIKey: "owner-key"}))
	before, err := os.ReadFile(c.service.document.path)
	if err != nil {
		t.Fatal(err)
	}
	c.service.probe = func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		return dsh.ProbeResult{}, errors.New("gateway unavailable")
	}
	if err := c.upgradeHostedDefaultModel(humanOperator()); err == nil {
		t.Fatal("failed validation reported success")
	}
	after, _ := os.ReadFile(c.service.document.path)
	if string(before) != string(after) {
		t.Fatal("failed validation published advanced defaults")
	}
	c.service.probe = successfulModelProbe
	if err := c.upgradeHostedDefaultModel(humanOperator()); err != nil {
		t.Fatal("validation retry failed", err)
	}
}

func TestHostedDefaultUpgradeRestartContinuesCommittedTarget(t *testing.T) {
	parallelFixtureTest(t)
	m, c, config := onlineModelManager(t)
	legacy := modelTestEntry("daily")
	legacy.ModelParameters = ModelParameters{Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel}
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "save", Entry: legacy, APIKey: "owner-key"}))
	requireModelComplete(t, modelChange(t, c, modelChangeInput{Kind: "default", EntryID: "daily"}))
	createDirectConversation(t, m, directSelection("existing"))
	inspect := c.inspect
	c.inspect = func(context.Context, dsh.Options) (dsh.RuntimeIdentity, error) {
		return dsh.RuntimeIdentity{}, errors.New("runtime unavailable")
	}
	if err := c.upgradeHostedDefaultModel(humanOperator()); err == nil {
		t.Fatal("failed application reported success")
	}
	var pending *modelChangeOperation
	for _, op := range c.operations {
		if op.Committed && op.Status == "failed" {
			pending = op
		}
	}
	if pending == nil || len(pending.Members) != 1 {
		t.Fatal("committed upgrade target not retained")
	}
	version, target := c.service.snapshot().Version, pending.Members[0].Target
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
		t.Error("committed target was revalidated")
		return dsh.ProbeResult{}, errors.New("unexpected")
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.modelControl, control.inspect = control, inspect
	if err := control.upgradeHostedDefaultModel(humanOperator()); err != nil {
		t.Fatal("restart failed to apply the original target", err)
	}
	if control.service.snapshot().Version != version || control.operations[pending.ID].Status != "completed" || !reflect.DeepEqual(control.operations[pending.ID].Members[0].Target, target) {
		t.Fatal("restart replaced or republished the target")
	}
}
