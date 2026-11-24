package room

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func startSpaceServerFixture(t *testing.T, home string, configure ...func(*SpaceRuntime)) (*spaceServer, string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	startup := freshAccessConfig(t, home)
	if err := prepareFirstAccess(startup); err != nil {
		t.Fatal(err)
	}
	startup = freshAccessConfig(t, home)
	access, err := newServerAccess(startup, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	factories := personalDeployment(controlledActivationFactory(nil, nil)).factories
	r, err := newSpaceRuntime(startup, serviceConfig{platformCatalog: defaultPlatformCatalog(), access: access, settings: newSystemSettings(startup), endpoint: endpoint, maxTurns: 7, modelProbe: successfulModelProbe}, factories)
	if err != nil {
		t.Fatal(err)
	}
	r.defaults = testTeamDefinition(t).Runtime
	for _, configureRuntime := range configure {
		configureRuntime(r)
	}
	s, err := newSpaceServer(t.Context(), r, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- s.Serve(ctx, listener, func(any) error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("space server startup timeout")
	}
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
			t.Fatal("space server shutdown timeout")
		}
	}
	t.Cleanup(stop)
	return s, endpoint, stop
}

func spaceHTTPRequest(t *testing.T, endpoint, path string, input any, out any) int {
	t.Helper()
	var body io.Reader
	method := http.MethodGet
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(data))
		method = http.MethodPost
	}
	request, err := http.NewRequest(method, endpoint+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Origin", endpoint)
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		t.Fatalf("decode %s (%d): %s", path, response.StatusCode, data)
	}
	return response.StatusCode
}

func waitSpaceState(t *testing.T, s *spaceServer, id, state string) *SpaceRuntime {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		entry := s.entries[id]
		s.mu.Unlock()
		if entry != nil {
			entry.mu.RLock()
			r, err := entry.runtime, entry.err
			available := false
			if entry.listener != nil {
				entry.listener.mu.RLock()
				available = entry.listener.handler != nil
				entry.listener.mu.RUnlock()
			}
			entry.mu.RUnlock()
			if err != nil {
				t.Fatalf("space %s failed: %v", id, err)
			}
			if r != nil && available && r.State() == state {
				return r
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("space %s did not reach %s", id, state)
	return nil
}

func TestSpacesIndependentSetupRoutingAndRestart(t *testing.T) {
	home := t.TempDir()
	s, endpoint, stop := startSpaceServerFixture(t, home)
	a := s.initial.SpaceID
	var record SpaceRecord
	id := strings.Repeat("b", 32)
	input := spaceChangeInput{RequestID: id, Name: "工作"}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", input, &record); status != 202 {
		t.Fatal(status, record)
	}
	b := waitSpaceState(t, s, id, "setup_required")
	if b.config.access.authority() != s.access || b.roots.Home == s.initial.roots.Home || b.config.endpoint != endpoint+spaceRoute(id) {
		t.Fatal("space reused roots, authority or unscoped execution route")
	}
	var setup map[string]any
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup", nil, &setup); status != 200 || setup["credential_configured"] != false {
		t.Fatal(status, setup)
	}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/foreign/rooms", nil, nil); status != 403 {
		t.Fatal("foreign Space accepted", status)
	}
	var aRooms roomsResponse
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(a)+"/v1/rooms", nil, &aRooms); status != 200 {
		t.Fatal(status)
	}
	if s.initial.manager.notifications == nil {
		t.Fatal("actual notification manager was not started")
	}
	// A duplicate creation returns the original identity. Rename uses a version
	// fence and never changes roots or requires a new registration journal.
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces", input, &record); status != 202 || record.ID != id {
		t.Fatal(status, record)
	}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+a, spaceChangeInput{Name: "个人", ExpectedVersion: 1}, nil); status != 200 {
		t.Fatal(status)
	}
	if status := spaceHTTPRequest(t, endpoint, "/v1/spaces/"+a, spaceChangeInput{Name: "覆盖", ExpectedVersion: 1}, nil); status != 409 {
		t.Fatal("stale rename", status)
	}
	model := setupModelInput{RequestID: "model-setup", ExpectedVersion: 1, ModelParameters: modelTestEntry("private-b").ModelParameters, APIKey: "private-space-b-key"}
	// A child launcher normally comes from the deployment. This fixture's
	// synthetic DSH component is bound explicitly without touching daily config.
	if b.startup.Runtime == nil {
		t.Fatal("launcher was not copied")
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", model, &setup); status != 202 {
		t.Fatal(status, setup)
	}
	b = waitSpaceState(t, s, id, "ready")
	if b == s.initial || b.manager == s.initial.manager || b.manager.notifications == s.initial.manager.notifications || b.computer.owner.ComputerID == s.initial.computer.owner.ComputerID {
		t.Fatal("mutable runtime resources or Computer identity were shared")
	}
	if err := s.claims.reserveComputer(a, "synthetic-instance"); err != nil {
		t.Fatal(err)
	}
	if err := s.claims.reserveComputer(id, "synthetic-instance"); !errors.Is(err, ErrConflict) {
		t.Fatal("Computer shared across spaces", err)
	}
	var bRooms roomsResponse
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/rooms", nil, &bRooms); status != 200 {
		t.Fatal(status)
	}
	if aRooms.ServiceEpoch == bRooms.ServiceEpoch {
		t.Fatal("catalog epochs shared")
	}
	// The exact A execution token cannot authenticate in B, including MCP.
	token, revoke, err := s.initial.config.access.issueExecution(executionBinding{ServiceInfo: ServiceInfo{ServiceEpoch: s.initial.manager.epoch}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "membership"}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	for _, path := range []string{"/v1/tools/list", "/mcp"} {
		req, _ := http.NewRequest(http.MethodPost, endpoint+spaceRoute(id)+path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatal("foreign execution token accepted", path, resp.StatusCode)
		}
	}
	// Native streaming is preserved; hello identifies B, never the shared
	// authentication authority's default Space.
	resp, err := http.Get(endpoint + spaceRoute(id) + updatesURL(b.manager.epoch, ServiceInfo{}))
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var hello map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &hello) == nil && hello["space_id"] == id {
				found = true
			}
			break
		}
	}
	resp.Body.Close()
	if !found {
		t.Fatal("stream lost Space identity")
	}
	stop()
	// Normal restart reloads the same IDs, private model/credential, and claims;
	// a fresh manager/runtime epoch is assembled instead of starting old objects.
	s2, endpoint2, _ := startSpaceServerFixture(t, home)
	b2 := waitSpaceState(t, s2, id, "ready")
	if b2.SpaceID != id || b2.RuntimeEpoch == b.RuntimeEpoch || b2.manager == b.manager {
		t.Fatal("restart replaced identity or reused stopped runtime")
	}
	var directory spaceDirectory
	if status := spaceHTTPRequest(t, endpoint2, "/v1/spaces", nil, &directory); status != 200 || len(directory.Spaces) != 2 || directory.Spaces[0].Name != "个人" {
		t.Fatal(status, directory)
	}
	if err := s2.claims.reserveComputer(id, "synthetic-instance"); !errors.Is(err, ErrConflict) {
		t.Fatal("Computer claim lost after restart", err)
	}
}

func TestSpacesDirectoryConflictIsDurableAndAtomic(t *testing.T) {
	s, _, _ := startSpaceServerFixture(t, t.TempDir())
	root := t.TempDir()
	a := s.initial.SpaceID
	b := strings.Repeat("c", 32)
	if _, err := s.create(spaceChangeInput{RequestID: b, Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if err := s.claims.reserve(a, RoomIdentity{RoomID: "a", RoomInstance: "instance-a"}, map[string]string{"member": root}); err != nil {
		t.Fatal(err)
	}
	if err := s.claims.reserve(b, RoomIdentity{RoomID: "b", RoomInstance: "instance-b"}, map[string]string{"member": filepath.Join(root, "child")}); err == nil {
		t.Fatal("overlapping cross-space path accepted")
	}
	if err := s.claims.reserve(a, RoomIdentity{RoomID: "same-space", RoomInstance: "instance-c"}, map[string]string{"member": root}); err != nil {
		t.Fatal("space registry changed same-space sharing rules", err)
	}
	if _, err := newSpaceServer(t.Context(), s.initial, io.Discard); !errors.Is(err, ErrLocked) {
		t.Fatal("second control owner accepted", err)
	}
	if _, err := s.create(spaceChangeInput{RequestID: "unsafe-id", Name: "bad"}); !errors.Is(err, ErrRequest) {
		t.Fatal("noncanonical SpaceID accepted", err)
	}
	if _, err := os.Stat(filepath.Join(s.initial.roots.Home, "spaces", "unsafe-id")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid creation touched disk", err)
	}
}

func TestSpacesSharedLoginAndControlCredentialBoundary(t *testing.T) {
	s, endpoint, _ := startSpaceServerFixture(t, t.TempDir())
	id := strings.Repeat("d", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "B"}); err != nil {
		t.Fatal(err)
	}
	b := waitSpaceState(t, s, id, "setup_required")
	// Exercise the real wrapper with one root-owned cookie store. No Space can
	// establish another authentication store or accept another Space's token.
	s.access.mu.Lock()
	s.access.disabled = false
	s.access.mu.Unlock()
	token, err := randomAccessToken()
	if err != nil {
		t.Fatal(err)
	}
	s.access.admin = sha256.Sum256([]byte(token))
	endpoint = "http://127.0.0.1:8088"
	s.access.internal, s.access.public = endpoint, endpoint
	cookie := accessLogin(t, s.access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })), token)
	request := httptest.NewRequest("GET", endpoint+"/v1/setup", nil)
	request.AddCookie(cookie)
	request.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	b.config.access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestSpaceID(r) != id {
			t.Error("wrong authorized Space")
		}
		w.WriteHeader(204)
	})).ServeHTTP(w, request)
	if w.Code != 204 {
		t.Fatal("shared browser session rejected", w.Code, w.Body.String())
	}
	leaseRequest, err := b.config.access.bindSpaceRequest(request, b.config.access.subject())
	if err != nil {
		t.Fatal(err)
	}
	leaseRequest = leaseRequest.WithContext(context.WithValue(leaseRequest.Context(), accessContextKey{}, b.config.access))
	lease, err := b.config.access.watchUIUpdates(leaseRequest)
	if err != nil {
		t.Fatal(err)
	}
	logout := accessRequest(s.access.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), "POST", "/v1/access/logout", "", endpoint, "{}", cookie)
	if logout.Code != 200 {
		t.Fatal(logout.Code)
	}
	select {
	case <-lease.done:
	case <-time.After(time.Second):
		t.Fatal("logout did not revoke child Space stream")
	}
}

func TestSpacesMissingDefaultModelDoesNotBlockAnotherSpace(t *testing.T) {
	s, endpoint, _ := startSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) { r.setupRequired = true })
	id := strings.Repeat("e", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Independent"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	model := setupModelInput{RequestID: "independent-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("independent").ModelParameters, APIKey: "synthetic-private-key"}
	var result map[string]any
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", model, &result); status != 202 {
		t.Fatal(status, result)
	}
	waitSpaceState(t, s, id, "ready")
	if s.initial.manager != nil || s.initial.State() != "setup_required" {
		t.Fatal("B implicitly configured or started A")
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/rooms", nil, &result); status != 200 {
		t.Fatal(status, result)
	}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(s.initial.SpaceID)+"/v1/setup", nil, &result); status != 200 || result["credential_configured"] != false {
		t.Fatal("A imported B credentials", status, result)
	}
}

func TestSpacesUnknownCreationRecoversSameReservation(t *testing.T) {
	home := t.TempDir()
	s, _, stop := startSpaceServerFixture(t, home)
	id := strings.Repeat("f", 32)
	base := s.writer.ops
	s.writer.ops.rename = func(from, to string) error {
		if err := base.rename(from, to); err != nil {
			return err
		}
		return errors.New("synthetic failure after publish")
	}
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Interrupted"}); err == nil {
		t.Fatal("unknown publication reported success")
	}
	if len(s.entries) != 1 {
		t.Fatal("uncertain publication started resources")
	}
	if _, err := s.create(spaceChangeInput{RequestID: strings.Repeat("a", 32), Name: "Retry"}); err == nil {
		t.Fatal("unknown writer accepted another mutation")
	}
	stop()
	restarted, _, _ := startSpaceServerFixture(t, home)
	waitSpaceState(t, restarted, id, "setup_required")
	if len(restarted.directory.Spaces) != 2 {
		t.Fatal("creation replay allocated another Space")
	}
}

func TestSpacesScopedCLIAndCallback(t *testing.T) {
	id := strings.Repeat("a", 32)
	if err := validateAccessEndpoint("http://127.0.0.1:8088" + spaceRoute(id)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/s/other", "/s/" + id + "/extra", "/s/" + id + "?other=1", "/anything"} {
		if validateAccessEndpoint("http://127.0.0.1:8088"+path) == nil {
			t.Fatal("invalid scoped endpoint", path)
		}
	}
	s, endpoint, _ := startSpaceServerFixture(t, t.TempDir())
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "B"}); err != nil {
		t.Fatal(err)
	}
	b := waitSpaceState(t, s, id, "setup_required")
	m := &roomManager{config: b.config}
	if redirect, err := m.gitlabRedirectLocked(); err != nil || redirect != endpoint+spaceRoute(id)+gitlabCallbackPath {
		t.Fatal("callback lost Space", redirect, err)
	}
	completion := httptest.NewRecorder()
	completionRequest := httptest.NewRequest("GET", endpoint+gitlabCompletePath, nil)
	completionRequest.RemoteAddr = "127.0.0.1:12345"
	m.serveGitLabComplete(completion, completionRequest)
	if !strings.Contains(completion.Body.String(), spaceRoute(id)+"/#platform-settings") {
		t.Fatal("OAuth completion returned to another Space", completion.Body.String())
	}
	recorder := httptest.NewRecorder()
	writer := &spaceResponseWriter{ResponseWriter: recorder, prefix: spaceRoute(id)}
	http.SetCookie(writer, &http.Cookie{Name: "synthetic-oauth", Value: "private-state", Path: gitlabCookiePath})
	_, _ = writer.Write([]byte(`{}`))
	if cookies := recorder.Result().Cookies(); len(cookies) != 1 || cookies[0].Path != spaceRoute(id)+gitlabCookiePath {
		t.Fatal("callback cookie lost Space", cookies)
	}
}

func spaceNotificationFixture(t *testing.T) func(*SpaceRuntime) {
	return func(r *SpaceRuntime) {
		var factories []platforms.Factory
		for _, definition := range defaultPlatformCatalog().Definitions() {
			factories = append(factories, func() platforms.Definition { return definition })
		}
		factories = append(factories, func() platforms.Definition {
			return platforms.Definition{
				Type: platform.TypeDefinition{Provider: "fixture", NotificationChannels: []string{"updates"}},
				Sources: []platforms.Source{{ID: fixtureNotificationSourceID, New: func() notification.Source { return fixtureNotificationSource{records: []string{"same-key"}} }, Bind: func(source notification.Source, _ notification.RuntimeHost) (notification.Runtime, error) {
					return fixtureNotificationAdapter(source.(fixtureNotificationSource)), nil
				}}},
			}
		})
		catalog, err := platforms.NewCatalog(factories...)
		if err != nil {
			t.Fatal(err)
		}
		r.config.platformCatalog = catalog
	}
}

func TestSpacesPendingNotificationsRecoverWithoutCrossDeliveryOrReplay(t *testing.T) {
	testSpacesPendingRecovery(t, false)
}
func TestSpacesArchivedPendingNotificationsRecoverWithoutReplay(t *testing.T) {
	testSpacesPendingRecovery(t, true)
}
func testSpacesPendingRecovery(t *testing.T, archive bool) {
	home := t.TempDir()
	var s *spaceServer
	var endpoint string
	var stop func()
	if archive {
		s, endpoint, stop = startArchivableSpaceServerFixture(t, home, spaceNotificationFixture(t))
	} else {
		s, endpoint, stop = startSpaceServerFixture(t, home, spaceNotificationFixture(t))
	}
	id := strings.Repeat("c", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "B"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	model := setupModelInput{RequestID: "b-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("b").ModelParameters, APIKey: "synthetic-private-key"}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", model, nil); status != 202 {
		t.Fatal(status)
	}
	b := waitSpaceState(t, s, id, "ready")
	keys := map[string]notification.Key{}
	for _, runtime := range []*SpaceRuntime{waitSpaceState(t, s, s.initial.SpaceID, "ready"), b} {
		m := runtime.manager
		m.mu.Lock()
		owner := m.index.DefaultAssistant.RoomIdentity
		target := fixtureInboxTarget(t, m, owner)
		key := notification.Key{Owner: owner, Source: fixtureNotificationSourceID, SubscriptionID: strings.Repeat("a", 32)}
		keys[runtime.SpaceID] = key
		state := fixtureNotificationState{Target: target, Format: 53, Key: key, Version: 1, Enabled: true, Seen: map[string]bool{}, Inbox: map[string]notification.Item{}, RuntimeState: notification.RuntimeState{Status: "running"}}
		err := m.notifications.SaveLocked(state)
		runner := m.notifications.NewRunnerLocked(key)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if _, stopped := runner.Step(t.Context()); stopped {
			t.Fatal("controlled notification source failed")
		}
		current, ok := m.notifications.StateLocked(key)
		if !ok || current.Metadata().Pending != 1 {
			t.Fatal("pending source item lost")
		}
		// The Room Inbox commits; only the source acknowledgement fails. Restart
		// must retry the same delivery ID without generating a duplicate item.
		original := m.store.ops.createTemp
		m.store.ops.createTemp = func(string, string) (snapshotFile, error) {
			return nil, errors.New("synthetic lost source acknowledgement")
		}
		err = m.notifications.Deliver(t.Context(), key, 1)
		m.store.ops.createTemp = original
		if err == nil {
			t.Fatal("source acknowledgement failure hidden")
		}
		m.mu.Lock()
		svc, _ := m.runtimeServiceLocked(owner)
		m.mu.Unlock()
		page, err := svc.previewInbox(t.Context(), apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: runtime.OwnerUserID}}, AgentID: target.AgentID, MembershipID: target.MembershipID, Limit: 10})
		if err != nil || page.(inboxPreviewPage).SnapshotSeq != 1 {
			t.Fatal("notification did not reach its own Inbox", page, err)
		}
	}
	if archive {
		for _, space := range []string{s.initial.SpaceID, id} {
			archiveSpaceForTest(t, s, space)
		}
	}
	stop()
	s2, _, _ := startSpaceServerFixture(t, home, spaceNotificationFixture(t))
	if archive {
		s2.mu.Lock()
		count := len(s2.entries)
		s2.mu.Unlock()
		if count != 0 {
			t.Fatal("archived notification workers restarted")
		}
		for _, space := range []string{s.initial.SpaceID, id} {
			record := spaceRecordForTest(t, s2, space)
			if _, err := s2.changeSpaceStatus(space, "restore", record.Version); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, space := range []string{s.initial.SpaceID, id} {
		runtime := waitSpaceState(t, s2, space, "ready")
		m := runtime.manager
		key := keys[space]
		deadline := time.Now().Add(5 * time.Second)
		for {
			state, ok := m.notifications.StateLocked(key)
			if ok && state.Metadata().Pending == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("pending notification did not recover", space)
			}
			time.Sleep(10 * time.Millisecond)
		}
		m.mu.Lock()
		svc, _ := m.runtimeServiceLocked(key.Owner)
		m.mu.Unlock()
		state, _ := m.notifications.StateLocked(key)
		target := state.Metadata().Target
		page, err := svc.previewInbox(t.Context(), apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: runtime.OwnerUserID}}, AgentID: target.AgentID, MembershipID: target.MembershipID, Limit: 10})
		if err != nil || page.(inboxPreviewPage).SnapshotSeq != 1 {
			t.Fatal("restart duplicated or crossed notification delivery", page, err)
		}
	}
}

func TestSpacesFailedDefaultDoesNotBlockAnotherSpace(t *testing.T) {
	s, endpoint, _ := startSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) {
		open := r.factories.manager
		r.factories.manager = func(dir, teams string, turns uint64, defaults *teamRuntime, upgrade *inputUpgradeOptions, catalog *platforms.Catalog) (*roomManager, error) {
			if dir == r.roots.Data {
				return nil, errors.New("synthetic default startup failure")
			}
			return open(dir, teams, turns, defaults, upgrade, catalog)
		}
	})
	id := strings.Repeat("b", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Healthy"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	model := setupModelInput{RequestID: "healthy-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("healthy").ModelParameters, APIKey: "synthetic-key"}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", model, nil); status != 202 {
		t.Fatal(status)
	}
	waitSpaceState(t, s, id, "ready")
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/rooms", nil, nil); status != 200 {
		t.Fatal(status)
	}
	if status := spaceHTTPRequest(t, endpoint, "/ui/app.js", nil, nil); status != 200 {
		t.Fatal("failed default blocked shared assets", status)
	}
	var health map[string]any
	if status := spaceHTTPRequest(t, endpoint, "/healthz", nil, &health); status != 200 || health["status"] != "running" {
		t.Fatal("failed default marked the control entry unavailable", status, health)
	}
}

func TestSpacesLongTurnAndIncompleteShutdownRetainOwners(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	s, endpoint, _ := startSpaceServerFixture(t, t.TempDir(), func(r *SpaceRuntime) {
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
	m := s.initial.manager
	m.mu.Lock()
	owner := m.index.DefaultAssistant.RoomIdentity
	svc, _ := m.runtimeServiceLocked(owner)
	m.mu.Unlock()
	svc = waitSpaceAssistant(t, s.initial)
	caller := Caller{Author: Author{Kind: "human", ID: s.initial.OwnerUserID}}
	aClient := cliClient{endpoint: endpoint + spaceRoute(s.initial.SpaceID), http: &http.Client{Timeout: 5 * time.Second}, anonymousHuman: true}
	aPostCtx, cancelAPost := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelAPost()
	var aPosted PostReceipt
	if err := serverTestRequest(aPostCtx, aClient, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: caller, ClientMessageID: "long-a", Text: "controlled long task"}, &aPosted); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A turn did not start")
	}
	id := strings.Repeat("e", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Usable B"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", setupModelInput{RequestID: "b-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("b").ModelParameters, APIKey: "synthetic-key"}, nil); status != 202 {
		t.Fatal(status)
	}
	b := waitSpaceState(t, s, id, "ready")
	b.manager.mu.Lock()
	bSvc, _ := b.manager.runtimeServiceLocked(b.manager.index.DefaultAssistant.RoomIdentity)
	b.manager.mu.Unlock()
	bSvc = waitSpaceAssistant(t, b)
	bClient := cliClient{endpoint: endpoint + spaceRoute(id), http: &http.Client{Timeout: 5 * time.Second}, anonymousHuman: true}
	postCtx, cancelPost := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelPost()
	var posted PostReceipt
	if err := serverTestRequest(postCtx, bClient, "/v1/send", apiRequest{ServiceInfo: bSvc.info, Caller: caller, ClientMessageID: "short-b", Text: "controlled short task"}, &posted); err != nil {
		t.Fatal("B could not accept work while A ran", err)
	}
	room, err := bSvc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	member := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: room.Members[0].AgentID, MembershipID: room.Members[0].MembershipID}
	deadline := time.Now().Add(5 * time.Second)
	for {
		agent, err := bSvc.store.LoadAgent(member)
		if err == nil && agent.Turn != nil && agent.Turn.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("B did not finish while A was running", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	short, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if err := s.Close(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("incomplete shutdown reported settled", err)
	}
	s.mu.Lock()
	retained := s.entries[s.initial.SpaceID]
	s.mu.Unlock()
	retained.mu.RLock()
	same := retained.runtime == s.initial
	retained.mu.RUnlock()
	if !same || s.registryLock == nil {
		t.Fatal("timeout discarded the original owner or lock")
	}
	if replacement, err := newSpaceServer(t.Context(), s.initial, io.Discard); !errors.Is(err, ErrLocked) {
		if replacement != nil {
			_ = replacement.closeWithTimeout()
		}
		t.Fatal("same roots reopened before settlement", err)
	}
	close(release)
	released = true
	settle, cancelSettle := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelSettle()
	if err := s.Close(settle); err != nil {
		t.Fatal("original owner retry failed", err)
	}
	if s.registryLock != nil || s.initial.State() != "stopped" {
		t.Fatal("settled retry retained control ownership")
	}
}

func waitSpaceAssistant(t *testing.T, r *SpaceRuntime) *service {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		m := r.manager
		m.mu.Lock()
		var svc *service
		if m.index.DefaultAssistant != nil {
			svc, _ = m.runtimeServiceLocked(m.index.DefaultAssistant.RoomIdentity)
		}
		m.mu.Unlock()
		if svc != nil {
			return svc
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("default assistant runtime unavailable")
	return nil
}

func TestSpaceHTTPShutdownKeepsFactoryUntilAdmittedHandlersSettle(t *testing.T) {
	listener := &spaceHTTPListener{addr: spaceAddress("127.0.0.1:8088"), done: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-release })}
	serving := make(chan error, 1)
	go func() { serving <- serveRuntimeHTTP(server, listener) }()
	for {
		listener.mu.RLock()
		published := listener.handler != nil
		listener.mu.RUnlock()
		if published {
			break
		}
		time.Sleep(time.Millisecond)
	}
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		listener.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/setup/model", nil))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	shutdown := make(chan error, 1)
	go func() { shutdown <- shutdownRuntimeHTTP(server, listener, ctx) }()
	<-ctx.Done()
	select {
	case err := <-shutdown:
		close(release)
		t.Fatal("factory returned while an admitted handler could still start work", err)
	default:
	}
	rejected := httptest.NewRecorder()
	listener.ServeHTTP(rejected, httptest.NewRequest("POST", "/v1/setup/model", nil))
	if rejected.Code != 503 {
		t.Fatal("shutdown admitted another handler")
	}
	close(release)
	<-handled
	if err := <-shutdown; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := <-serving; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	if err := shutdownRuntimeHTTP(server, listener, t.Context()); err != nil {
		t.Fatal("settled retry", err)
	}
}
