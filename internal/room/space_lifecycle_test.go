package room

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func startArchivableSpaceServerFixture(t *testing.T, home string, configure ...func(*SpaceRuntime)) (*spaceServer, string, func()) {
	t.Helper()
	configure = append(configure, func(r *SpaceRuntime) {
		r.setupRequired = true
		launcher, err := toml.Marshal(map[string]any{"runtime": map[string]any{"dsh": r.defaults.DSH}})
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.ReadFile(r.config.settings.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.config.settings.path, append(append(source, '\n'), launcher...), 0600); err != nil {
			t.Fatal(err)
		}
		startup, err := readStartupConfig(r.config.settings.path, true, false)
		if err != nil {
			t.Fatal(err)
		}
		r.startup.Runtime = startup.Runtime
		r.config.settings = newSystemSettings(startup)
		r.config.access.document = r.config.settings.configDocument
	})
	s, endpoint, stop := startSpaceServerFixture(t, home, configure...)
	// The general fixture's initial manager has an in-memory launcher. This
	// lifecycle fixture goes through setup so reopening reads a real model file.
	initial := waitSpaceState(t, s, s.initial.SpaceID, "setup_required")
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(initial.SpaceID)+"/v1/setup/model", setupModelInput{RequestID: "archive-default-model", ExpectedVersion: 0, ModelParameters: modelTestEntry("archive-default").ModelParameters, APIKey: "synthetic-key"}, nil); status != 202 {
		t.Fatal(status)
	}
	waitSpaceState(t, s, initial.SpaceID, "ready")
	return s, endpoint, stop
}

func spaceRecordForTest(t *testing.T, s *spaceServer, id string) SpaceRecord {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.directory.Spaces {
		if record.ID == id {
			return record
		}
	}
	t.Fatal("missing Space", id)
	return SpaceRecord{}
}
func waitSpaceArchived(t *testing.T, s *spaceServer, id string) SpaceRecord {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		record := spaceRecordForTest(t, s, id)
		s.mu.Lock()
		entry := s.entries[id]
		failure := ""
		if entry != nil {
			failure = entry.lifecycleError
		}
		s.mu.Unlock()
		if failure != "" {
			t.Fatal(failure)
		}
		if record.Status == "suspended" {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Space did not settle archive")
	return SpaceRecord{}
}
func archiveSpaceForTest(t *testing.T, s *spaceServer, id string) SpaceRecord {
	t.Helper()
	record := spaceRecordForTest(t, s, id)
	if _, err := s.changeSpaceStatus(id, "archive", record.Version); err != nil {
		t.Fatal(err)
	}
	return waitSpaceArchived(t, s, id)
}

func TestSpaceArchiveRestoreRebuildsOwnersAndRevokesOldGeneration(t *testing.T) {
	home := t.TempDir()
	s, endpoint, stop := startArchivableSpaceServerFixture(t, home)
	a := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	bID := strings.Repeat("b", 32)
	if _, err := s.create(spaceChangeInput{RequestID: bID, Name: "B"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, bID, "setup_required")
	svc := waitSpaceAssistant(t, a)
	before, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	token, revoke, err := a.config.access.issueExecution(executionBinding{Caller: Caller{Author: Author{Kind: "agent", ID: before.Members[0].AgentID}, MembershipID: before.Members[0].MembershipID}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	// A's open SSE must close on archive, without revoking the shared login.
	stream, err := http.Get(endpoint + spaceRoute(a.SpaceID) + updatesURL(a.manager.epoch, ServiceInfo{}))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			break
		}
	}
	streamClosed := make(chan struct{})
	go func() {
		defer close(streamClosed)
		for scanner.Scan() {
		}
	}()
	external, err := resolveFutureDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	retainedFile := filepath.Join(external, "keep.txt")
	if err := os.WriteFile(retainedFile, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.claims.reserve(a.SpaceID, before.RoomIdentity, map[string]string{before.Members[0].MembershipID: external}); err != nil {
		t.Fatal(err)
	}
	// Both scoped API forms and default control operations remain authorized.
	record := spaceRecordForTest(t, s, a.SpaceID)
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+a.SpaceID+"/archive", spaceChangeInput{ExpectedVersion: record.Version}, nil); status != 202 {
		t.Fatal(status)
	}
	archived := waitSpaceArchived(t, s, a.SpaceID)
	select {
	case <-streamClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("archived SSE stayed open")
	}
	if err := s.claims.reserve(bID, RoomIdentity{RoomID: "b", RoomInstance: "b-instance"}, map[string]string{"member": external}); err == nil {
		t.Fatal("archive released external directory claim", err)
	}
	if data, err := os.ReadFile(retainedFile); err != nil || string(data) != "keep" {
		t.Fatal("archive changed external work file", err)
	}
	if a.State() != "stopped" || !a.config.access.revoked.Load() {
		t.Fatal("archive left the old owner active")
	}
	for _, path := range []string{spaceRoute(a.SpaceID) + "/v1/rooms", "/v1/spaces/" + a.SpaceID + "/rooms", spaceRoute(a.SpaceID) + gitlabCallbackPath + "?code=old&state=old"} {
		if status := spaceHTTPRequest(t, endpoint, path, nil, nil); status != 503 {
			t.Fatal("archived path admitted work", path, status)
		}
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(a.SpaceID)+"/", nil, nil); status != 200 {
		t.Fatal("archived page unavailable", status)
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(bID)+"/v1/setup", nil, nil); status != 200 {
		t.Fatal("archive stopped B", status)
	}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+a.SpaceID+"/restore", spaceChangeInput{ExpectedVersion: archived.Version - 1}, nil); status != 409 {
		t.Fatal("stale restore accepted", status)
	}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+a.SpaceID+"/restore", spaceChangeInput{ExpectedVersion: archived.Version}, nil); status != 202 {
		t.Fatal(status)
	}
	fresh := waitSpaceState(t, s, a.SpaceID, "ready")
	if fresh.config.endpoint != a.config.endpoint {
		t.Fatal("restore changed the Session endpoint fingerprint")
	}
	if fresh == a || fresh.manager == a.manager || fresh.manager.notifications == a.manager.notifications || fresh.RuntimeEpoch == a.RuntimeEpoch || fresh.SpaceID != a.SpaceID || fresh.OwnerUserID != a.OwnerUserID {
		t.Fatal("restore reused stopped owners or changed identity")
	}
	afterSvc := waitSpaceAssistant(t, fresh)
	after, err := afterSvc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	if after.RoomIdentity != before.RoomIdentity || after.Members[0].MembershipID != before.Members[0].MembershipID {
		t.Fatal("restore rebuilt persistent member identity")
	}
	request, err := http.NewRequest("GET", endpoint+spaceRoute(a.SpaceID)+"/v1/rooms", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 && response.StatusCode != 403 {
		t.Fatal("old execution token survived restoration", response.StatusCode)
	}
	archiveSpaceForTest(t, s, a.SpaceID)
	archiveSpaceForTest(t, s, bID)
	stop()
	restarted, newEndpoint, _ := startSpaceServerFixture(t, home)
	restarted.mu.Lock()
	count := len(restarted.entries)
	restarted.mu.Unlock()
	if count != 0 {
		t.Fatal("restart started archived runtimes")
	}
	if status := spaceHTTPRequest(t, newEndpoint, "/v1/spaces", nil, nil); status != 200 {
		t.Fatal("archived default locked control plane", status)
	}
	record = spaceRecordForTest(t, restarted, a.SpaceID)
	if _, err := restarted.changeSpaceStatus(a.SpaceID, "restore", record.Version); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, restarted, a.SpaceID, "ready")
}

func TestSpaceArchiveSetupAndInterruptedRegistrationRecovery(t *testing.T) {
	home := t.TempDir()
	s, endpoint, stop := startSpaceServerFixture(t, home)
	id := strings.Repeat("c", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Setup"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	archiveSpaceForTest(t, s, id)
	record := spaceRecordForTest(t, s, id)
	if _, err := s.changeSpaceStatus(id, "restore", record.Version); err != nil {
		t.Fatal(err)
	}
	setup := waitSpaceState(t, s, id, "setup_required")
	// Fail after the durable rename: no success, resources fenced, original
	// reservation retained. Restart reconciles this exact interrupted archive.
	s.mu.Lock()
	ops := s.writer.ops
	s.writer.ops.rename = func(from, to string) error {
		if err := ops.rename(from, to); err != nil {
			return err
		}
		return errors.New("synthetic publication uncertainty")
	}
	s.mu.Unlock()
	record = spaceRecordForTest(t, s, id)
	if _, err := s.changeSpaceStatus(id, "archive", record.Version); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if !setup.config.access.revoked.Load() {
		t.Fatal("uncertain archive admitted business")
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(s.initial.SpaceID)+"/v1/rooms", nil, nil); status != 200 {
		t.Fatal("uncertain archive blocked healthy A", status)
	}
	stop()
	restarted, _, _ := startSpaceServerFixture(t, home)
	record = spaceRecordForTest(t, restarted, id)
	if record.Status != "suspended" {
		t.Fatal("interrupted archive not completed", record.Status)
	}
	restarted.mu.Lock()
	old := restarted.entries[id]
	restarted.mu.Unlock()
	if old != nil {
		t.Fatal("interrupted archive started business")
	}
	if _, err := restarted.changeSpaceStatus(id, "restore", record.Version); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, restarted, id, "setup_required")
}

func TestSpaceArchiveTimeoutKeepsOriginalOwnerAndRetry(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	s, endpoint, _ := startArchivableSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) {
		create := r.factories.service
		r.factories.service = func(dir, id string, config serviceConfig) (*service, error) {
			if !pathWithin(r.roots.Data, dir) {
				return create(dir, id, config)
			}
			return controlledActivationFactory(func(MemberIdentity, context.Context) error {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				return nil
			}, nil)(dir, id, config)
		}
	})
	a := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	svc := waitSpaceAssistant(t, a)
	client := cliClient{endpoint: endpoint + spaceRoute(a.SpaceID), http: &http.Client{Timeout: 5 * time.Second}, anonymousHuman: true}
	var posted PostReceipt
	if err := serverTestRequest(t.Context(), client, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: a.OwnerUserID}}, ClientMessageID: "archive-long", Text: "controlled long task"}, &posted); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("turn not started")
	}
	record := spaceRecordForTest(t, s, a.SpaceID)
	if _, err := s.changeSpaceStatus(a.SpaceID, "archive", record.Version); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(35 * time.Second)
	for {
		s.mu.Lock()
		entry := s.entries[a.SpaceID]
		busy, failure := entry.lifecycleBusy, entry.lifecycleError
		s.mu.Unlock()
		if !busy {
			if failure == "" {
				t.Fatal("timeout reported success")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive timeout not reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	record = spaceRecordForTest(t, s, a.SpaceID)
	if record.Status != "archiving" || a.State() == "stopped" {
		t.Fatal("timeout discarded old owner")
	}
	if _, err := s.changeSpaceStatus(a.SpaceID, "restore", record.Version); !errors.Is(err, ErrBusy) {
		t.Fatal("restore bypassed settlement", err)
	}
	if lock, err := lockDirectory(a.roots.Data); !errors.Is(err, ErrLocked) {
		if lock != nil {
			_ = unlockDirectory(lock)
		}
		t.Fatal("timeout released data lock", err)
	}
	id := strings.Repeat("d", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Unaffected"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup", nil, nil); status != 200 {
		t.Fatal("A timeout blocked B", status)
	}
	close(release)
	released = true
	if _, err := s.changeSpaceStatus(a.SpaceID, "archive", record.Version); err != nil {
		t.Fatal(err)
	}
	archived := waitSpaceArchived(t, s, a.SpaceID)
	if _, err := s.changeSpaceStatus(a.SpaceID, "restore", archived.Version); err != nil {
		t.Fatal(err)
	}
	fresh := waitSpaceState(t, s, a.SpaceID, "ready")
	if fresh == a {
		t.Fatal("retry restarted old manager")
	}
}

func TestSpaceArchiveRetainsAuthenticatedControlAndRejectsExecution(t *testing.T) {
	s, endpoint, _ := startArchivableSpaceServerFixture(t, t.TempDir())
	a := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	token, err := randomAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	s.access.mu.Lock()
	s.access.disabled = false
	s.access.admin = sha256.Sum256([]byte(token))
	s.access.mu.Unlock()
	login, err := http.NewRequest("POST", endpoint+"/v1/access/login", strings.NewReader(`{"token":"`+token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Header.Set("Origin", endpoint)
	response, err := http.DefaultClient.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	response.Body.Close()
	if response.StatusCode != 200 || len(cookies) == 0 {
		t.Fatal("login failed", response.StatusCode)
	}
	execution, revoke, err := a.config.access.issueExecution(executionBinding{Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "membership"}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	record := spaceRecordForTest(t, s, a.SpaceID)
	mutate := func(id, action, bearer string, version uint64) int {
		input, _ := json.Marshal(spaceChangeInput{ExpectedVersion: version})
		request, _ := http.NewRequest("POST", endpoint+"/v1/spaces/"+id+"/"+action, strings.NewReader(string(input)))
		request.Header.Set("Origin", endpoint)
		if bearer != "" {
			request.Header.Set("Authorization", "Bearer "+bearer)
		} else {
			request.AddCookie(cookies[0])
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if status := mutate(a.SpaceID, "archive", execution, record.Version); status != 401 && status != 403 {
		t.Fatal("execution altered control directory", status)
	}
	if status := mutate(strings.Repeat("e", 32), "archive", "", 1); status != 403 {
		t.Fatal("unknown space lifecycle accepted", status)
	}
	if status := mutate(a.SpaceID, "archive", "", record.Version); status != 202 {
		t.Fatal(status)
	}
	archived := waitSpaceArchived(t, s, a.SpaceID)
	for _, path := range []string{"/v1/spaces", spaceRoute(a.SpaceID) + "/v1/access/session"} {
		request, _ := http.NewRequest("GET", endpoint+path, nil)
		request.AddCookie(cookies[0])
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("shared login lost during archive", path, response.StatusCode)
		}
	}
	if status := mutate(a.SpaceID, "restore", "", archived.Version); status != 202 {
		t.Fatal("cookie could not restore archived default", status)
	}
	waitSpaceState(t, s, a.SpaceID, "ready")
}

func TestSpaceArchiveFinalReceiptFailureRetriesSameOwner(t *testing.T) {
	s, _, _ := startSpaceServerFixture(t, t.TempDir())
	id := strings.Repeat("f", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Receipt retry"}); err != nil {
		t.Fatal(err)
	}
	old := waitSpaceState(t, s, id, "setup_required")
	base := s.writer.ops
	var writes atomic.Int32
	s.writer.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if writes.Add(1) == 2 {
			return nil, errors.New("synthetic final receipt preparation failure")
		}
		return base.createTemp(dir, pattern)
	}
	record := spaceRecordForTest(t, s, id)
	if _, err := s.changeSpaceStatus(id, "archive", record.Version); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		entry := s.entries[id]
		failed := !entry.lifecycleBusy && entry.lifecycleError != ""
		s.mu.Unlock()
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing final receipt failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if old.State() != "stopped" {
		t.Fatal("final receipt preceded business settlement")
	}
	record = spaceRecordForTest(t, s, id)
	if record.Status != "archiving" {
		t.Fatal("failed final receipt reported archived")
	}
	if _, err := s.changeSpaceStatus(id, "restore", record.Version); !errors.Is(err, ErrBusy) {
		t.Fatal("restore bypassed failed receipt", err)
	}
	// The known preparation failure leaves the writer usable. Retrying
	// publishes only the receipt and keeps the same original runtime owner.
	if _, err := s.changeSpaceStatus(id, "archive", record.Version); err != nil {
		t.Fatal(err)
	}
	waitSpaceArchived(t, s, id)
	s.mu.Lock()
	entry := s.entries[id]
	s.mu.Unlock()
	entry.mu.RLock()
	same := entry.runtime == old
	entry.mu.RUnlock()
	if !same {
		t.Fatal("final receipt retry replaced the original owner")
	}
}
