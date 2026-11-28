package room

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func hostedResetFixture(t *testing.T) *spaceServer {
	t.Helper()
	s := hostedGatewayFixture(t, t.TempDir(), externalAuthConfig{Provider: "fixture", AllowedSubjects: []string{"alice", "bob"}}, false)
	s.assembly.factories.hostedResetAccepted = true // private acceptance only
	return s
}

func TestSpaceResetHostedOwnerIsolationAndDefaultModel(t *testing.T) {
	s := hostedResetFixture(t)
	// Exercise the production restricted-tool storage binding as well as the
	// hosted model path. Platform network acceptance has its own explicit gate.
	bin := t.TempDir()
	mustWriteResetFile(t, filepath.Join(bin, "lark-cli"), "#!/bin/sh\necho 'lark-cli version 1.0.99'\n")
	if err := os.Chmod(filepath.Join(bin, "lark-cli"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s.assembly.config.hostTools = hostToolPolicy{restricted: true}
	type fixture struct {
		cookie  *http.Cookie
		runtime *SpaceRuntime
		marker  string
	}
	var spaces []fixture
	for _, account := range []string{"alice", "bob"} {
		cookie, _, id := hostedLogin(t, s, account)
		for i := 0; i < 2; i++ {
			if i == 1 {
				id, _ = newOpaqueID()
				if w := hostedRequest(s, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "工作"}, cookie, ""); w.Code != 202 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			r := waitSpaceState(t, s, id, "ready")
			if got := templateContents(t, filepath.Join(r.roots.Teams, "seed", "team.json")); got != templateContents(t, filepath.Join(s.assembly.config.teamsSource, "seed", "team.json")) {
				t.Fatal("hosted space did not inherit deployment templates")
			}

			waitSpaceAssistant(t, r)
			if r.manager.config.platformTempRoot != filepath.Join(r.roots.Home, "platform-tmp") {
				t.Fatal("missing private temporary root")
			}
			marker := filepath.Join(r.roots.Home, "platform-cli", "lark", "private", "data", "master.key")
			mustWriteResetFile(t, marker, "synthetic-private-"+id)
			spaces = append(spaces, fixture{cookie, r, marker})
		}
	}
	target := spaces[0]
	before := spaceRecordForTest(t, s, target.runtime.SpaceID)
	input := resetInputForTest(before)
	if w := hostedRequest(s, "/v1/spaces/"+before.ID+"/reset", input, spaces[2].cookie, ""); w.Code != 403 {
		t.Fatal("foreign owner accepted", w.Code)
	}
	if next := spaceRecordForTest(t, s, before.ID); next.Version != before.Version || next.Reset != nil {
		t.Fatal("foreign write mutated directory")
	}
	oldCatalog := target.runtime.manager.config.models.catalog()
	oldRoom := waitSpaceAssistant(t, target.runtime).info.RoomIdentity
	keyPath := s.access.users.gatewayPath(before.OwnerUserID)
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(s.assembly.roots.Home, "gitlab-applications", "keep-secret")
	mustWriteResetFile(t, shared, "shared-service-application")
	for _, path := range []string{filepath.Join(before.Roots.Home, "platform-tmp", "crashed-tool", "secret"), filepath.Join(before.Roots.Home, "model-migration-backup", "old"), filepath.Join(before.Roots.Data, "platform-migration-backup", "token")} {
		mustWriteResetFile(t, path, "synthetic-old-secret")
	}
	w := hostedRequest(s, "/v1/spaces/"+before.ID+"/reset", input, target.cookie, "")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitResetForTest(t, s, before.ID)
	next := waitSpaceState(t, s, before.ID, "ready")
	newRoom := waitSpaceAssistant(t, next).info.RoomIdentity
	if next.RuntimeEpoch == target.runtime.RuntimeEpoch || newRoom == oldRoom {
		t.Fatal("old runtime or Room reused")
	}
	model, err := next.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
	if err != nil || model.ModelID != hostedDefaultModel || model.CredentialRef == oldCatalog.Entries[0].CredentialRef {
		t.Fatal("old model definition restored", err)
	}
	credential, err := next.manager.config.models.Credentials.get(model.CredentialRef)
	if err != nil || credential.Value != "gk_quota-for-alice" {
		t.Fatal("owner quota lost", err)
	}
	for _, path := range []string{target.marker, filepath.Join(before.Roots.Home, "platform-tmp", "crashed-tool"), filepath.Join(before.Roots.Home, "model-migration-backup"), filepath.Join(before.Roots.Data, "platform-migration-backup")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("private material survived", path, err)
		}
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("user gateway changed", err)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Fatal("shared application removed", err)
	}
	for _, other := range spaces[1:] {
		if active := waitSpaceState(t, s, other.runtime.SpaceID, "ready"); active != other.runtime {
			t.Fatal("other runtime replaced")
		}
		if _, err := os.Stat(other.marker); err != nil {
			t.Fatal("other credential removed", err)
		}
		if w := hostedRequest(s, spaceRoute(other.runtime.SpaceID)+"/v1/ui", nil, other.cookie, ""); w.Code != 200 {
			t.Fatal("other owner lost access", w.Code)
		}
	}
	newMarker := filepath.Join(before.Roots.Data, "new-state")
	mustWriteResetFile(t, newMarker, "new")
	if w := hostedRequest(s, "/v1/spaces/"+before.ID+"/reset", input, target.cookie, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(newMarker); err != nil {
		t.Fatal("completed retry cleaned again", err)
	}
}

func TestSpaceResetHostedDisableCompletesAndCanEnable(t *testing.T) {
	s := hostedResetFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	s.assembly.config.modelProbe = func(ctx context.Context, opts dsh.Options) (dsh.ProbeResult, error) {
		close(entered)
		<-release
		return successfulModelProbe(ctx, opts)
	}
	cookie, subject, id := hostedLogin(t, s, "alice")
	<-entered // a preaccepted model writer must settle before deletion
	before := spaceRecordForTest(t, s, id)
	marker := filepath.Join(before.Roots.Home, "model-migration-backup", "old")
	mustWriteResetFile(t, marker, "synthetic-old")
	input := resetInputForTest(before)
	if w := hostedRequest(s, "/v1/spaces/"+id+"/reset", input, cookie, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.adminMu.Lock()
	_, err := s.access.users.setStatus(subject.UserID, "disabled")
	s.fenceUser(subject.UserID)
	settleErr := s.settleUser(t.Context(), subject.UserID)
	s.adminMu.Unlock()
	if err != nil || !errors.Is(settleErr, ErrBusy) {
		t.Fatal(err, settleErr)
	}
	if current := spaceRecordForTest(t, s, id); current.Reset.Phase != "stopping" {
		t.Fatal("disable replaced reset phase")
	}
	if s.userSettled(subject.UserID) {
		t.Fatal("enable admitted before reset settled")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("deleted while writer owned data", err)
	}
	unblock()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		busy := s.resetBusy[id]
		s.mu.Unlock()
		current := spaceRecordForTest(t, s, id)
		if !busy && current.Reset.Phase == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	completed := spaceRecordForTest(t, s, id)
	if completed.Reset.Phase != "completed" || completed.Status != "suspended" || completed.Reset.Code != "owner_unavailable" || !s.userSettled(subject.UserID) {
		t.Fatal("disable and reset did not settle", completed)
	}
	s.assembly.config.modelProbe = successfulModelProbe
	if _, err := s.access.users.setStatus(subject.UserID, "active"); err != nil {
		t.Fatal(err)
	}
	_, nextSubject, _ := hostedLogin(t, s, "alice")
	if _, err := s.changeSpaceStatus(id, "restore", completed.Version, nextSubject); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "ready")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old material survived", err)
	}
}

func TestSpaceResetHostedArchivedResourceLimitAndRestore(t *testing.T) {
	s := hostedResetFixture(t)
	cookie, subject, id := hostedLogin(t, s, "alice")
	waitSpaceState(t, s, id, "ready")
	before := spaceRecordForTest(t, s, id)
	if _, err := s.changeSpaceStatus(id, "archive", before.Version, subject); err != nil {
		t.Fatal(err)
	}
	archived := waitSpaceArchived(t, s, id)
	other, _ := newOpaqueID()
	if w := hostedRequest(s, "/v1/spaces", spaceChangeInput{RequestID: other, Name: "占用容量"}, cookie, ""); w.Code != 202 {
		t.Fatal(w.Code)
	}
	waitSpaceState(t, s, other, "ready")
	s.assembly.config.executionResources = newExecutionResources(executionResourceLimits{ActiveSpaces: 1})
	input := resetInputForTest(archived)
	if w := hostedRequest(s, "/v1/spaces/"+id+"/reset", input, cookie, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		busy := s.resetBusy[id]
		s.mu.Unlock()
		r := spaceRecordForTest(t, s, id)
		if !busy && r.Reset.Phase == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	completed := spaceRecordForTest(t, s, id)
	if completed.Status != "suspended" || completed.Reset.Phase != "completed" || completed.Reset.Code != "resource_unavailable" {
		t.Fatal(completed)
	}
	marker := filepath.Join(completed.Roots.Data, "after-reset")
	mustWriteResetFile(t, marker, "retain")
	if w := hostedRequest(s, "/v1/spaces/"+id+"/reset", input, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("completed retry cleaned again", err)
	}
	// Remove the post-reset fixture marker before normal model setup; first use
	// deliberately rejects unexpected business files.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	otherRecord := spaceRecordForTest(t, s, other)
	if _, err := s.changeSpaceStatus(other, "archive", otherRecord.Version, subject); err != nil {
		t.Fatal(err)
	}
	waitSpaceArchived(t, s, other)
	if _, err := s.changeSpaceStatus(id, "restore", completed.Version, subject); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "ready")
}

func TestSpaceResetHostedModelFailureStaysInSetup(t *testing.T) {
	s := hostedResetFixture(t)
	cookie, _, id := hostedLogin(t, s, "alice")
	old := waitSpaceState(t, s, id, "ready")
	s.assembly.config.modelProbe = func(context.Context, dsh.Options) (dsh.ProbeResult, error) {
		return dsh.ProbeResult{}, errors.New("unavailable")
	}
	input := resetInputForTest(spaceRecordForTest(t, s, id))
	if w := hostedRequest(s, "/v1/spaces/"+id+"/reset", input, cookie, ""); w.Code != 202 {
		t.Fatal(w.Code)
	}
	waitResetForTest(t, s, id)
	r := waitSpaceState(t, s, id, "setup_required")
	if r.RuntimeEpoch == old.RuntimeEpoch {
		t.Fatal("old instance reused")
	}
	w := hostedRequest(s, spaceRoute(id)+"/v1/setup", nil, cookie, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "gk_quota") {
		t.Fatal(w.Code, w.Body.String())
	}
	marker := filepath.Join(r.roots.Data, "new-state")
	mustWriteResetFile(t, marker, "new")
	if w := hostedRequest(s, "/v1/spaces/"+id+"/reset", input, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("model failure retry cleaned new state", err)
	}
}

func TestSpaceResetHostedMissingGatewayStaysInSetup(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	s.assembly.factories.hostedResetAccepted = true
	s.assembly.config.teamsSource = resetSeedForTest(t)
	cookie, subject, id := hostedLogin(t, s, "alice")
	old := waitSpaceState(t, s, id, "setup_required")
	input := resetInputForTest(spaceRecordForTest(t, s, id))
	if w := hostedRequest(s, "/v1/spaces/"+id+"/reset", input, cookie, ""); w.Code != 202 {
		t.Fatal(w.Code)
	}
	waitResetForTest(t, s, id)
	next := waitSpaceState(t, s, id, "setup_required")
	if next.RuntimeEpoch == old.RuntimeEpoch {
		t.Fatal("old setup reused")
	}
	if key, _, err := s.access.users.gatewayKey(subject.UserID); err != nil || key != "" {
		t.Fatal("reset invented a gateway credential", err)
	}
	if w := hostedRequest(s, spaceRoute(id)+"/v1/setup", nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestSpaceResetHostedDisableAfterCompletedBeforeReopen(t *testing.T) {
	s := hostedResetFixture(t)
	_, subject, id := hostedLogin(t, s, "alice")
	waitSpaceState(t, s, id, "ready")
	before := spaceRecordForTest(t, s, id)
	if _, err := s.changeSpaceStatus(id, "archive", before.Version, subject); err != nil {
		t.Fatal(err)
	}
	record := waitSpaceArchived(t, s, id)
	// Simulate a worker paused after its durable completed publication and
	// before cleanup-lock handoff. A disable here must also become settled.
	record.Status = "active"
	record.Reset = &spaceResetOperation{FormatVersion: 1, RequestID: strings.Repeat("e", 32), ExpectedVersion: record.Version, Phase: "completed"}
	record.Version++
	s.mu.Lock()
	for i, current := range s.directory.Spaces {
		if current.ID == id {
			if err := s.saveSpaceLocked(i, record); err != nil {
				s.mu.Unlock()
				t.Fatal(err)
			}
		}
	}
	s.resetBusy = map[string]bool{id: true}
	s.mu.Unlock()
	s.adminMu.Lock()
	_, err := s.access.users.setStatus(subject.UserID, "disabled")
	s.fenceUser(subject.UserID)
	settleErr := s.settleUser(t.Context(), subject.UserID)
	s.adminMu.Unlock()
	if err != nil || !errors.Is(settleErr, ErrBusy) {
		t.Fatal(err, settleErr)
	}
	if err := s.finishSpaceReset(record); err != nil {
		t.Fatal(err)
	}
	if after := spaceRecordForTest(t, s, id); after.Status != "suspended" || after.Reset.Phase != "completed" || !s.userSettled(subject.UserID) {
		t.Fatal("disable left an impossible enable condition", after)
	}
}

func TestSpaceResetHostedInterruptedPhasesResume(t *testing.T) {
	for _, phase := range []string{"stopping", "cleaning", "initializing"} {
		t.Run(phase, func(t *testing.T) {
			s := hostedResetFixture(t)
			_, subject, id := hostedLogin(t, s, "alice")
			waitSpaceState(t, s, id, "ready")
			record := spaceRecordForTest(t, s, id)
			if _, err := s.changeSpaceStatus(id, "archive", record.Version, subject); err != nil {
				t.Fatal(err)
			}
			record = waitSpaceArchived(t, s, id)
			input := resetInputForTest(record)
			record.Reset = &spaceResetOperation{FormatVersion: 1, RequestID: input.RequestID, ExpectedVersion: input.ExpectedVersion, Phase: "stopping"}
			storage, err := s.resetStorage(record)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := s.resetDocument(record, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := storage.saveCandidate(doc); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(record.Roots.Home, "platform-cli", "lark", "private", "data", "master.key")
			mustWriteResetFile(t, marker, "synthetic-old-key")
			mustWriteResetFile(t, filepath.Join(record.Roots.Home, "platform-tmp", "crashed", "token"), "synthetic-old-token")
			if phase == "initializing" {
				if err := storage.clean(); err != nil {
					t.Fatal(err)
				}
			}
			record.Reset.Phase = phase
			record.Version++
			s.mu.Lock()
			for i, current := range s.directory.Spaces {
				if current.ID == id {
					err = s.saveSpaceLocked(i, record)
				}
			}
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.closeWithTimeout(); err != nil {
				t.Fatal(err)
			}
			restored := hostedFixture(t, s.assembly.roots.Home)
			restored.assembly.factories.hostedResetAccepted = true
			restored.assembly.config.teamsSource = s.assembly.config.teamsSource
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- restored.Serve(ctx, listener, func(any) error { return nil }) }()
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(10 * time.Second):
					t.Error("restarted control plane did not close")
				}
			}()
			completed := waitResetForTest(t, restored, id)
			if completed.ResetID() != input.RequestID || completed.Roots != record.Roots {
				t.Fatal("restart replaced operation or identity")
			}
			waitSpaceState(t, restored, id, "ready")
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("private credential survived restart", err)
			}
		})
	}
}
