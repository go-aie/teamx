package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

func resetSeedForTest(t *testing.T) string {
	t.Helper()
	root, err := resolveFutureDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "seed")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	team := testTeamDefinition(t)
	team.Runtime = nil
	raw, err := json.Marshal(team)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "team.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
func waitResetForTest(t *testing.T, s *spaceServer, id string) SpaceRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		record := spaceRecordForTest(t, s, id)
		s.mu.Lock()
		busy := s.resetBusy[id]
		s.mu.Unlock()
		if !busy && record.Reset != nil {
			if record.Reset.Code != "" {
				t.Fatal(record.Reset.Phase, record.Reset.Code, record.Reset.Message)
			}
			if record.Reset.Phase == "completed" {
				return record
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reset did not finish", spaceRecordForTest(t, s, id))
	return SpaceRecord{}
}
func resetInputForTest(record SpaceRecord) spaceChangeInput {
	return spaceChangeInput{RequestID: strings.Repeat("e", 32), ExpectedVersion: record.Version}
}

func TestSpaceResetReturnsToFirstUseAndPreservesOtherOwners(t *testing.T) {
	home := t.TempDir()
	s, endpoint, stop := startArchivableSpaceServerFixture(t, home)
	seed := s.assembly.config.teamsSource
	seedBefore := templateContents(t, filepath.Join(seed, "seed", "team.json"))
	defer stop()
	old := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	oldService := waitSpaceAssistant(t, old)
	oldInfo := oldService.info
	otherID := strings.Repeat("b", 32)
	if _, err := s.create(spaceChangeInput{RequestID: otherID, Name: "其他空间"}); err != nil {
		t.Fatal(err)
	}
	other := waitSpaceState(t, s, otherID, "setup_required")
	otherEpoch := other.RuntimeEpoch
	external, err := resolveFutureDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(external, "keep.txt"), "external")
	if err := s.claims.reserve(old.SpaceID, oldInfo.RoomIdentity, map[string]string{"member": external}); err != nil {
		t.Fatal(err)
	}
	if err := s.claims.reserve(otherID, RoomIdentity{RoomID: "shared-public", RoomInstance: "other"}, map[string]string{"member": external}); err != nil {
		t.Fatal("public directory could not be shared before reset", err)
	}
	if err := s.claims.reserveComputer(old.SpaceID, "keep-instance"); err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(home, "computer-credentials", "private"), "computer-secret")
	mustWriteResetFile(t, filepath.Join(home, "computer-state", "keep"), "computer-state")
	mustWriteResetFile(t, filepath.Join(home, "unknown-private-file"), "unknown")
	deploymentRoot := filepath.Join(old.roots.Data, "deployment")
	s.assembly.config.workspaceProtected = append(s.assembly.config.workspaceProtected, deploymentRoot)
	mustWriteResetFile(t, filepath.Join(deploymentRoot, "installation.json"), "installation-record")
	mustWriteResetFile(t, filepath.Join(home, "model-migration-backup", "old"), "old-secret")
	mustWriteResetFile(t, filepath.Join(old.roots.Data, "platform-connections.json"), "old-platform-secret")
	mustWriteResetFile(t, filepath.Join(old.roots.Data, "default-workspace", "file"), "old-work")
	if err := os.Symlink(external, filepath.Join(old.roots.Data, "outside-link")); err != nil {
		t.Fatal(err)
	}
	rootBefore, _ := os.Stat(old.roots.Data)
	lockBefore, _ := os.Stat(filepath.Join(old.roots.Data, ".lock"))
	identityBefore, _ := os.ReadFile(filepath.Join(old.roots.Data, "installation-id"))
	// Exercise a deployment credential nested in Data through the complete reset
	// and first-model setup, rather than only through a storage-only fixture.
	adminRelative := "data/custom-auth/admin-token"
	mustWriteResetFile(t, filepath.Join(home, adminRelative), "keep-custom-admin-token")
	s.access.mu.Lock()
	doc := old.config.settings.configDocument
	doc.mu.Lock()
	err = doc.commitLocked(func(source []byte) ([]byte, func(), error) {
		var values map[string]any
		if err := toml.Unmarshal(source, &values); err != nil {
			return nil, nil, err
		}
		values["access"].(map[string]any)["admin_token_file"] = adminRelative
		next, err := toml.Marshal(values)
		return next, func() { s.access.config.Access.AdminTokenFile = adminRelative }, err
	})
	doc.mu.Unlock()
	s.access.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	tokenBefore, _ := os.ReadFile(adminTokenPath(s.access.config))
	record := spaceRecordForTest(t, s, old.SpaceID)
	input := resetInputForTest(record)
	var response map[string]any
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", input, &response); status != 202 {
		t.Fatal(status, response)
	}
	done := waitResetForTest(t, s, record.ID)
	if done.ID != record.ID || done.Name != record.Name || done.Roots != record.Roots || done.Version <= record.Version {
		t.Fatal(done)
	}
	fresh := waitSpaceState(t, s, record.ID, "setup_required")
	if got := templateContents(t, filepath.Join(seed, "seed", "team.json")); got != seedBefore {
		t.Fatal("deployment defaults changed")
	}
	if got := templateContents(t, filepath.Join(old.roots.Teams, "seed", "team.json")); got != seedBefore {
		t.Fatal("reset did not restore deployment defaults")
	}

	if fresh == old || fresh.RuntimeEpoch == old.RuntimeEpoch {
		t.Fatal("old runtime reused")
	}
	rootAfter, _ := os.Stat(old.roots.Data)
	lockAfter, _ := os.Stat(filepath.Join(old.roots.Data, ".lock"))
	if !os.SameFile(rootBefore, rootAfter) || !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("root/lock inode replaced")
	}
	for path, want := range map[string]string{filepath.Join(external, "keep.txt"): "external", filepath.Join(home, "computer-credentials", "private"): "computer-secret", filepath.Join(home, "computer-state", "keep"): "computer-state", filepath.Join(home, "unknown-private-file"): "unknown", filepath.Join(deploymentRoot, "installation.json"): "installation-record", adminTokenPath(s.access.config): string(tokenBefore), filepath.Join(old.roots.Data, "installation-id"): string(identityBefore)} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("protected material changed %s: %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(home, "model-migration-backup"), filepath.Join(old.roots.Data, "platform-connections.json"), filepath.Join(old.roots.Data, "default-workspace"), filepath.Join(old.roots.Data, "outside-link")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("business data survived", path, err)
		}
	}
	creds, err := os.ReadDir(filepath.Join(home, "model-credentials"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(creds) != 0 {
		t.Fatal("model keys survived")
	}
	if other.RuntimeEpoch != otherEpoch || other.ctx.Err() != nil {
		t.Fatal("other Space interrupted")
	}
	s.claims.mu.Lock()
	otherClaimRetained := false
	for _, claim := range s.claims.saved.Claims {
		if claim.SpaceID == old.SpaceID {
			t.Error("external binding retained")
		}
		if claim.SpaceID == otherID && len(claim.Paths) == 1 && claim.Paths[0] == external {
			otherClaimRetained = true
		}
	}
	if !otherClaimRetained {
		t.Error("reset released another Space's public directory claim")
	}
	if s.claims.saved.Computers["keep-instance"] != old.SpaceID {
		t.Error("Computer claim lost")
	}
	s.claims.mu.Unlock()
	var receipt SpaceRecord
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", input, &receipt); status != 200 || receipt.ResetID() != input.RequestID {
		t.Fatal(status, receipt)
	}
	changed := input
	changed.ExpectedVersion++
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", changed, nil); status != 409 {
		t.Fatal("same ID changed body", status)
	}
	// A page from before reset cannot submit setup against the fresh catalog.
	stale := setupModelInput{RuntimeEpoch: old.RuntimeEpoch, RequestID: "stale", ExpectedVersion: 0, ModelParameters: modelTestEntry("fresh").ModelParameters, APIKey: "secret"}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(record.ID)+"/v1/setup/model", stale, nil); status != 403 {
		t.Fatal("stale setup admitted", status)
	}
	stale.RuntimeEpoch = fresh.RuntimeEpoch
	stale.RequestID = "fresh"
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(record.ID)+"/v1/setup/model", stale, nil); status != 202 {
		t.Fatal(status)
	}
	ready := waitSpaceState(t, s, record.ID, "ready")
	newService := waitSpaceAssistant(t, ready)
	if newService.info.RoomIdentity == oldInfo.RoomIdentity {
		t.Fatal("old Room reused")
	}
	// Retrying the completed reset must not delete newly created business data.
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", input, nil); status != 200 {
		t.Fatal(status)
	}
	if newService.ctx.Err() != nil {
		t.Fatal("completed reset closed new service")
	}
}

func mustWriteResetFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceResetConfigRestoresBusinessDefaultsAndKeepsDeployment(t *testing.T) {
	source := []byte(`[server]
 time_zone="UTC"
 session_max_turns=3
 merge_window="2s"
 dsh_turn_timeout="2m"
 inbox_expire_after_days=9
 inbox_skip_expired=false
 inbox_mode="cli"
 data_dir="./custom-data"
 teams_dir="./custom-teams"
 listen="127.0.0.1:8090"
 workspace_roots=["/tmp"]
 [server.image_materials]
 enabled=false
 [runtime.dsh]
 binary_path="/deploy/bin/dsh"
 [runtime.model]
 protocol="openai-completions"
 model_id="old-model"
 api_key_env="CUSTOM_MODEL_KEY"
 [env]
 CUSTOM_MODEL_KEY="old-secret"
 OPENAI_API_KEY="also-secret"
 PATH="/deploy/bin"
 HTTPS_PROXY="http://proxy"
 [computer]
 enabled=false
 [access]
 admin_token_file="custom-data/auth/token"
 operator_id="operator"
`)
	data, err := spaceResetConfig(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	mustWriteResetFile(t, path, string(data))
	startup, err := readStartupConfig(path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	config, err := mapSpaceBusinessConfig(serviceConfig{maxTurns: 3, dshTurnTimeout: 2 * time.Minute}, startup)
	if err != nil {
		t.Fatal(err)
	}
	if config.maxTurns != defaultMaxTurns || config.dshTurnTimeout != 15*time.Minute || config.mergeWindow != 100*time.Millisecond || config.inboxExpiry.AfterDays != 3 || !config.inboxExpiry.SkipExpired || config.inboxMode != "" || *startup.Server.TimeZone != defaultTimeZone {
		t.Fatal("defaults not mapped", config.maxTurns, config.dshTurnTimeout)
	}
	if startup.Models == nil || len(startup.Models.Entries) != 0 || startup.Runtime.Model.ModelID != "" || len(startup.Env) != 2 || startup.Env["PATH"] != "/deploy/bin" || startup.Access.AdminTokenFile != "custom-data/auth/token" || *startup.Server.Listen != "127.0.0.1:8090" {
		t.Fatal("reset classification lost fields")
	}
	if strings.Contains(string(data), "secret") {
		t.Fatal("legacy key retained")
	}
	var oldValues, newValues map[string]any
	_ = toml.Unmarshal(source, &oldValues)
	_ = toml.Unmarshal(data, &newValues)
	for _, key := range []string{"computer", "access"} {
		a, _ := json.Marshal(oldValues[key])
		b, _ := json.Marshal(newValues[key])
		if string(a) != string(b) {
			t.Fatal(key, "changed")
		}
	}
	if _, err := spaceResetConfig([]byte("[env]\nUNCLASSIFIED=\"private\"\n")); err == nil {
		t.Fatal("custom env accepted")
	}
}

func TestSpaceResetPreflightFailureLeavesRuntimeAndData(t *testing.T) {
	s, endpoint, _ := startArchivableSpaceServerFixture(t, t.TempDir())
	if err := os.RemoveAll(s.assembly.config.teamsSource); err != nil {
		t.Fatal(err)
	}
	old := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	record := spaceRecordForTest(t, s, old.SpaceID)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", resetInputForTest(record), nil); status != 400 {
		t.Fatal(status)
	}
	if current := spaceRecordForTest(t, s, record.ID); current.Reset != nil || current.Version != record.Version || old.ctx.Err() != nil {
		t.Fatal("preflight modified runtime", current)
	}
	if _, err := s.requestSpaceReset(record.ID, resetInputForTest(record), userSubject{UserID: "foreign", AuthVersion: 1}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestSpaceResetInterruptedPhasesResumeBeforeBusinessStartup(t *testing.T) {
	for _, phase := range []string{"stopping", "cleaning", "initializing", "initialized"} {
		t.Run(phase, func(t *testing.T) {
			home := t.TempDir()
			seed := resetSeedForTest(t)
			s, _, stop := startArchivableSpaceServerFixture(t, home, func(r *SpaceRuntime) { r.config.teamsSource = seed })
			old := waitSpaceState(t, s, s.initial.SpaceID, "ready")
			record := archiveSpaceForTest(t, s, old.SpaceID)
			input := resetInputForTest(record)
			record.Reset = &spaceResetOperation{FormatVersion: 1, RequestID: input.RequestID, ExpectedVersion: input.ExpectedVersion, Phase: "stopping"}
			storage, err := s.resetStorage(record)
			if err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			entry := s.entries[record.ID]
			s.mu.Unlock()
			doc, err := s.resetDocument(record, entry)
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.saveCandidate(doc); err != nil {
				t.Fatal(err)
			}
			mustWriteResetFile(t, filepath.Join(old.roots.Data, "old-private"), "secret")
			// An invalid migration preimage must never be reopened/replayed on startup.
			mustWriteResetFile(t, filepath.Join(home, "model-migration.json"), "partial old migration")
			if phase == "cleaning" {
				record.Reset.Phase = "cleaning"
				if err := storage.remove(filepath.Join(old.roots.Data, "rooms")); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "initializing" || phase == "initialized" {
				if err := storage.clean(); err != nil {
					t.Fatal(err)
				}
				record.Reset.Phase = "initializing"
				if phase == "initialized" {
					candidate, err := storage.loadCandidate()
					if err != nil {
						t.Fatal(err)
					}
					if err := storage.initialize(candidate); err != nil {
						t.Fatal(err)
					}
				}
			}
			record.Version++
			s.mu.Lock()
			err = s.saveSpaceLocked(0, record)
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			stop()
			selected := personalDeployment(controlledActivationFactory(nil, nil))
			if err := initializeTeamTemplates(seed, teamDefaultsPath(filepath.Join(home, "config.toml"))); err != nil {
				t.Fatal(err)
			}
			_, stopAgain := startResetServerForTest(t, []string{"--home", home}, serverOptions{deployment: &selected, probe: successfulModelProbe})
			defer stopAgain()
			restarted := selected.spaces
			done := waitResetForTest(t, restarted, record.ID)
			if done.ResetID() != input.RequestID || done.Roots != record.Roots {
				t.Fatal(done)
			}
			waitSpaceState(t, restarted, record.ID, "setup_required")
			if _, err := os.Stat(filepath.Join(old.roots.Data, "old-private")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("old secret survived", err)
			}
		})
	}
}

func TestSpaceResetStoppingRequiresDurableExecutionSettlementAndOriginalRequest(t *testing.T) {
	home := t.TempDir()
	seed := resetSeedForTest(t)
	s, endpoint, _ := startArchivableSpaceServerFixture(t, home, func(r *SpaceRuntime) { r.config.teamsSource = seed })
	old := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	record := archiveSpaceForTest(t, s, old.SpaceID)
	generation := filepath.Join(old.roots.Data, "rooms", "orphan", "agents", "member", "session.json.dsh-old")
	mustWriteResetFile(t, filepath.Join(generation, "sensitive"), "old-secret")
	input := resetInputForTest(record)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", input, nil); status != 202 {
		t.Fatal(status)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.mu.Lock()
		busy := s.resetBusy[record.ID]
		s.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no failure reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	failed := spaceRecordForTest(t, s, record.ID)
	if failed.Reset.Phase != "stopping" || failed.Reset.Code == "" || failed.Status != "suspended" {
		t.Fatal(failed)
	}
	if _, err := os.Stat(filepath.Join(generation, "sensitive")); err != nil {
		t.Fatal("deleted unsettled session", err)
	}
	if _, err := s.changeSpaceStatus(record.ID, "restore", failed.Version); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	changed := input
	changed.RequestID = strings.Repeat("f", 32)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", changed, nil); status != 409 {
		t.Fatal(status)
	}
	// Existing adapter settlement evidence permits retry; a remote unknown result
	// is not replayed or investigated by reset.
	mustWriteResetFile(t, filepath.Join(generation, "settled.json"), `{"version":1}`)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+record.ID+"/reset", input, nil); status != 202 {
		t.Fatal(status)
	}
	waitResetForTest(t, s, record.ID)
}

func TestSpaceResetPathsProtectNestedAdminAndRejectReplacedRoots(t *testing.T) {
	home, err := resolveFutureDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seed := resetSeedForTest(t)
	record := SpaceRecord{Roots: spaceRoots{home, filepath.Join(home, "custom-data"), filepath.Join(home, "custom-teams")}}
	for _, root := range []string{record.Roots.Data, record.Roots.Teams} {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(record.Roots.Data, "rooms", "protected", "admin-token")
	mustWriteResetFile(t, keep, "keep-admin")
	mustWriteResetFile(t, filepath.Join(filepath.Dir(keep), "business"), "delete")
	x := &spaceResetStorage{record: record, seed: seed, protected: []string{keep}, roots: map[string]resetFileIdentity{}}
	if err := x.validate(); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	mustWriteResetFile(t, filepath.Join(external, "outside"), "outside")
	if err := os.Symlink(external, filepath.Join(record.Roots.Data, "nested")); err != nil {
		t.Fatal(err)
	}
	if err := x.clean(); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{keep: "keep-admin", filepath.Join(external, "outside"): "outside"} {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != value {
			t.Fatal(path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(keep), "business")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("protected parent kept business sibling", err)
	}
	if err := os.Rename(record.Roots.Data, record.Roots.Data+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record.Roots.Data, 0700); err != nil {
		t.Fatal(err)
	}
	mustWriteResetFile(t, filepath.Join(record.Roots.Data, "replacement"), "retain")
	if err := x.clean(); err == nil {
		t.Fatal("replaced root accepted")
	}
	if _, err := os.Stat(filepath.Join(record.Roots.Data, "replacement")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(record.Roots.Teams); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, record.Roots.Teams); err != nil {
		t.Fatal(err)
	}
	if err := x.validate(); err == nil {
		t.Fatal("top-level symlink accepted")
	}
}

func TestSpaceResetUnavailableCompositionRejectsBeforeMutation(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	s.assembly.factories.hostedResetAccepted = false
	cookie, _, id := hostedLogin(t, s, "alice")
	before := spaceRecordForTest(t, s, id)
	response := hostedRequest(s, "/v1/spaces/"+id+"/reset", resetInputForTest(before), cookie, "")
	if response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	after := spaceRecordForTest(t, s, id)
	if after.Version != before.Version || after.Status != before.Status || after.Reset != nil {
		t.Fatal("hosted state mutated", after)
	}
}

// Unlike startTestServer this preserves existing access configuration; resetting
// it with access init would correctly invalidate a prepared reset candidate.
func startResetServerForTest(t *testing.T, args []string, options serverOptions) (serverTestReady, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	ready := make(chan serverTestReady, 1)
	done := make(chan error, 1)
	options.ready = func(value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var info serverTestReady
		if err := json.Unmarshal(data, &info); err != nil {
			return err
		}
		ready <- info
		return nil
	}
	args = append(args, "--listen", "127.0.0.1:0")
	go func() { done <- runConfiguredServer(ctx, args, io.Discard, io.Discard, options) }()
	stopped := false
	stop := func() {
		t.Helper()
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(35 * time.Second):
			t.Fatal("reset server shutdown timeout")
		}
	}
	t.Cleanup(stop)
	select {
	case info := <-ready:
		return info, stop
	case err := <-done:
		stopped = true
		cancel()
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("reset server startup timeout")
	}
	return serverTestReady{}, stop
}

func TestSpaceResetUncertainAcceptanceStaysFencedUntilRestart(t *testing.T) {
	home := t.TempDir()
	seed := resetSeedForTest(t)
	s, _, stop := startArchivableSpaceServerFixture(t, home, func(r *SpaceRuntime) { r.config.teamsSource = seed })
	old := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	path := filepath.Join(old.roots.Data, "must-not-delete")
	mustWriteResetFile(t, path, "keep")
	record := spaceRecordForTest(t, s, old.SpaceID)
	input := resetInputForTest(record)
	s.mu.Lock()
	ops := s.writer.ops
	s.writer.ops.rename = func(from, to string) error {
		if err := ops.rename(from, to); err != nil {
			return err
		}
		return errors.New("synthetic commit unknown")
	}
	s.mu.Unlock()
	if _, err := s.requestSpaceReset(record.ID, input, s.access.subject()); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("uncertain acceptance deleted business", err)
	}
	s.mu.Lock()
	busy := s.resetBusy[record.ID]
	s.mu.Unlock()
	if !busy || old.ctx.Err() == nil {
		t.Fatal("uncertain owner was admitted")
	}
	if _, err := s.changeSpaceStatus(record.ID, "restore", record.Version); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	stop()
	restarted, _, _ := startSpaceServerFixture(t, home, func(r *SpaceRuntime) { r.config.teamsSource = seed })
	completed := waitResetForTest(t, restarted, record.ID)
	if completed.ResetID() != input.RequestID {
		t.Fatal("original request lost")
	}
}

func setupEpochForTest(t *testing.T, client cliClient) string {
	t.Helper()
	var state struct {
		RuntimeEpoch string `json:"runtime_epoch"`
	}
	if err := client.request(t.Context(), "/v1/setup", nil, &state); err != nil || state.RuntimeEpoch == "" {
		t.Fatal("setup epoch unavailable", err)
	}
	return state.RuntimeEpoch
}

func TestSpaceResetJoinsAdmittedSetupWriterBeforeDeleting(t *testing.T) {
	seed := resetSeedForTest(t)
	s, endpoint, _ := startArchivableSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) { r.config.teamsSource = seed })
	entered, release := make(chan struct{}, 1), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	s.assembly.config.modelProbe = func(ctx context.Context, opts dsh.Options) (dsh.ProbeResult, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return successfulModelProbe(ctx, opts)
	}
	id := strings.Repeat("c", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "设置中的空间"}); err != nil {
		t.Fatal(err)
	}
	runtime := waitSpaceState(t, s, id, "setup_required")
	marker := filepath.Join(runtime.roots.Data, "private-material")
	mustWriteResetFile(t, marker, "old-secret")
	input := setupModelInput{RuntimeEpoch: runtime.RuntimeEpoch, RequestID: "in-flight-validation", ExpectedVersion: 1, ModelParameters: modelTestEntry("old").ModelParameters, APIKey: "private-old-key"}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", input, nil); status != 202 {
		t.Fatal(status)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("probe not entered")
	}
	record := spaceRecordForTest(t, s, id)
	reset := resetInputForTest(record)
	var diagnostic map[string]any
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+id+"/reset", reset, &diagnostic); status != 202 {
		t.Fatal(status, diagnostic)
	}
	// Cancellation is deliberately ignored by this admitted writer until release.
	time.Sleep(100 * time.Millisecond)
	waiting := spaceRecordForTest(t, s, id)
	if waiting.Reset.Phase != "stopping" {
		t.Fatal("advanced while writer owned", waiting)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("deleted before joining writer", err)
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(s.initial.SpaceID)+"/v1/rooms", nil, nil); status != 200 {
		t.Fatal("other Space interrupted", status)
	}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+id+"/reset", reset, nil); status != 202 {
		t.Fatal("duplicate original request failed", status)
	}
	close(release)
	released = true
	waitResetForTest(t, s, id)
	waitSpaceState(t, s, id, "setup_required")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old data retained", err)
	}
}
