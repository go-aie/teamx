package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/computer"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPersonalSpaceRegistrationResumesInterruptedPublication(t *testing.T) {
	for _, boundary := range []string{"users.json", "spaces.json", "completion"} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after=%t", boundary, after), func(t *testing.T) {
				c, _ := testAccessConfig(t, t.TempDir())
				base := defaultSnapshotOps()
				ops := base
				ops.rename = func(from, to string) error {
					match := filepath.Base(to) == boundary
					if boundary == "completion" && filepath.Base(to) == "personal-space-registration.json" {
						var record personalRegistration
						if err := readSnapshot(from, &record, maxPrivateFileBytes); err != nil {
							t.Fatal(err)
						}
						match = record.Completed
					}
					if match && !after {
						return errors.New("interrupted")
					}
					if err := base.rename(from, to); err != nil {
						return err
					}
					if match && after {
						return errors.New("interrupted after publish")
					}
					return nil
				}
				if _, err := registerPersonalSpace(c, "installation", &snapshotWriter{ops: ops}); err == nil {
					t.Fatal("fault was not reached")
				}
				var reserved personalRegistration
				if err := readIdentitySnapshot(filepath.Join(filepath.Dir(c.path), "personal-space-registration.json"), &reserved); err != nil {
					t.Fatal(err)
				}
				restored, err := registerPersonalSpace(c, "installation", &snapshotWriter{ops: base})
				if err != nil {
					t.Fatal(err)
				}
				if restored.Space.ID != reserved.Space.ID || restored.User != reserved.User || !restored.Completed {
					t.Fatal("retry changed identity")
				}
				next, err := registerPersonalSpace(c, "installation", &snapshotWriter{ops: base})
				if err != nil || !reflect.DeepEqual(next, restored) {
					t.Fatal("restart changed registration", err)
				}
			})
		}
	}
}

func TestPersonalSpaceRegistrationPreservesOldResourcesAndRejectsDamage(t *testing.T) {
	for _, damage := range []string{"none", "missing users", "missing spaces", "missing operation", "corrupt users", "symlink users", "permissions", "second user", "roots", "owner"} {
		t.Run(damage, func(t *testing.T) {
			c, _ := testAccessConfig(t, t.TempDir())
			home := filepath.Dir(c.path)
			// Representative persisted resources are opaque to registration.
			files := []string{filepath.Join(*c.Server.DataDir, "rooms", "old", "agents", "member", "session.json"), filepath.Join(home, "computer-state", "old.json"), filepath.Join(home, "model-credentials", "key"), filepath.Join(*c.Server.TeamsDir, "team.json")}
			for _, file := range files {
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("original identity and bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			first, err := registerPersonalSpace(c, "installation", &snapshotWriter{ops: defaultSnapshotOps()})
			if err != nil {
				t.Fatal(err)
			}
			users := filepath.Join(home, "users.json")
			switch damage {
			case "missing users":
				err = os.Remove(users)
			case "missing spaces":
				err = os.Remove(filepath.Join(home, "spaces.json"))
			case "missing operation":
				err = os.Remove(filepath.Join(home, "personal-space-registration.json"))
			case "corrupt users":
				err = os.WriteFile(users, []byte("{"), 0600)
			case "symlink users":
				err = os.Rename(users, users+".original")
				if err == nil {
					err = os.Symlink(users+".original", users)
				}
			case "permissions":
				err = os.Chmod(users, 0644)
			case "second user":
				err = (&snapshotWriter{ops: defaultSnapshotOps()}).save(users, userDirectory{1, []User{first.User, {ID: "another", Status: "active", AuthVersion: 1}}}, maxPrivateFileBytes)
			case "roots":
				root := t.TempDir()
				c.Server.DataDir = &root
			case "owner":
				c.Access.OperatorID = "owner_" + strings.Repeat("a", 32)
			}
			if err != nil {
				t.Fatal(err)
			}
			restored, err := registerPersonalSpace(c, "installation", &snapshotWriter{ops: defaultSnapshotOps()})
			if damage == "none" {
				if err != nil || restored.User.ID != c.Access.OperatorID || restored.Space.ID != first.Space.ID {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("damaged registration accepted")
			}
			for _, file := range files {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != "original identity and bytes" {
					t.Fatal("registration changed old resources", file, err)
				}
			}
		})
	}
}

func TestSpaceAuthorizationUsesSubjectAndExecutionScope(t *testing.T) {
	a, token, _ := accessFixture(t)
	called := 0
	handler := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if !a.ownsRequest(r) || requestOperator(r) != a.identity.User.ID || r.URL.Path != "/v1/ui" {
			t.Error("unbound request")
		}
	}))
	scoped := "/v1/spaces/" + a.space.SpaceID + "/ui"
	for _, path := range []string{"/v1/ui", scoped} {
		if w := accessRequest(handler, "GET", path, token, "", "", nil); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
	if called != 2 {
		t.Fatal(called)
	}
	for _, subject := range []userSubject{{}, {UserID: "another", AuthVersion: 1}, {UserID: a.space.OwnerUserID, AuthVersion: 2}} {
		if _, err := a.authorizeSpace(subject, a.space.SpaceID); err == nil {
			t.Fatal("foreign subject accepted")
		}
	}
	for _, path := range []string{"/v1/spaces/other/ui", "/v1/ui?caller_id=forged"} {
		if w := accessRequest(handler, "GET", path, token, "", "", nil); w.Code != 403 {
			t.Fatal("foreign scope accepted", w.Code)
		}
	}
	for _, extra := range []string{`"caller":{"kind":"human","id":"forged"}`, fmt.Sprintf(`"caller":{"kind":"human","id":%q},"space_id":"other"`, a.space.OwnerUserID), fmt.Sprintf(`"caller":{"kind":"human","id":%q},"user_id":"other"`, a.space.OwnerUserID)} {
		if w := accessRequest(handler, "POST", "/v1/send", token, "", "{"+extra+"}", nil); w.Code != 403 {
			t.Fatal("forged identity accepted", w.Code)
		}
	}
	cookie := accessLogin(t, handler, token)
	hash := sha256.Sum256([]byte(cookie.Value))
	saved := a.sessions[hash]
	a.sessions[hash] = accessSession{expires: saved.expires, Subject: userSubject{UserID: a.space.OwnerUserID, AuthVersion: 2}}
	if w := accessRequest(handler, "GET", scoped, "", "", "", cookie); w.Code != 403 {
		t.Fatal("stale auth version accepted", w.Code)
	}
	a.identity.User.Status = "suspended"
	if w := accessRequest(handler, "GET", scoped, token, "", "", nil); w.Code != 403 {
		t.Fatal("suspended user accepted", w.Code)
	}
	a.identity.User.Status = "active"
	b := executionBinding{ServiceInfo: ServiceInfo{ServiceEpoch: "room-epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}}
	execution, revoke, err := a.issueExecution(b)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	data, _ := json.Marshal(b)
	if w := accessRequest(a.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), "POST", "/v1/spaces/other/read", execution, "", string(data), nil); w.Code != 403 {
		t.Fatal("execution crossed Space", w.Code)
	}
	bad := b
	bad.SpaceContext = a.space
	bad.RuntimeEpoch = "previous"
	if _, _, err := a.issueExecution(bad); err == nil {
		t.Fatal("stale runtime accepted")
	}
	// Even a token accidentally put into this registry cannot cross a runtime.
	key := sha256.Sum256([]byte(execution))
	a.executions[key] = bad
	if w := accessRequest(handler, "POST", "/v1/read", execution, "", string(data), nil); w.Code != 403 {
		t.Fatal("foreign execution accepted", w.Code)
	}
	if requestOperator(httptest.NewRequest("GET", "/", nil)) != "" {
		t.Fatal("unauthenticated OS user fallback")
	}
}

func TestBrowserSessionsUpgradeBindsRegisteredUser(t *testing.T) {
	a, token, h := accessFixture(t)
	cookie := accessLogin(t, h, token)
	digest := sha256.Sum256([]byte(cookie.Value))
	expiry := time.Now().Add(time.Hour)
	legacy := struct {
		Version  int                  `json:"version"`
		Binding  string               `json:"binding"`
		Sessions map[string]time.Time `json:"sessions"`
	}{1, a.sessionBinding(), map[string]time.Time{hex.EncodeToString(digest[:]): expiry}}
	if err := (&snapshotWriter{ops: defaultSnapshotOps()}).save(a.sessionPath(), legacy, maxAccessSessionBytes); err != nil {
		t.Fatal(err)
	}
	fresh, handler := restartAccess(t, a)
	if w := accessRequest(handler, "GET", "/v1/access/session", "", "", "", cookie); w.Code != 200 {
		t.Fatal("legacy session lost", w.Code)
	}
	var saved accessSessionSnapshot
	if err := readSnapshot(a.sessionPath(), &saved, maxAccessSessionBytes); err != nil {
		t.Fatal(err)
	}
	session := saved.Sessions[hex.EncodeToString(digest[:])]
	if saved.Version != 2 || session.userSubject != fresh.subject() || !session.Expires.Equal(expiry) {
		t.Fatal("legacy cookie not bound to original user")
	}
}

func TestPersonalFirstStartRegistersWithoutAccountSteps(t *testing.T) {
	home := t.TempDir()
	var previous personalRegistration
	for turn := 0; turn < 2; turn++ {
		endpoint, stop := startPersonalSetupServer(t, home)
		response, err := http.Get(endpoint + "/v1/access/session")
		if err != nil {
			t.Fatal(err)
		}
		var session map[string]any
		err = json.NewDecoder(response.Body).Decode(&session)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || session["login_enabled"] != false || session["user_id"] == "" || session["space_id"] == "" {
			t.Fatal("first start added account step", session, err)
		}
		stop()

		var registration personalRegistration
		if err := readIdentitySnapshot(filepath.Join(home, "personal-space-registration.json"), &registration); err != nil {
			t.Fatal(err)
		}
		if turn > 0 && !reflect.DeepEqual(previous, registration) {
			t.Fatal("restart changed first-start identity")
		}
		if registration.Space.Name != "TeamX" {
			t.Fatalf("default Space name = %q, want TeamX", registration.Space.Name)
		}
		previous = registration
		if session["user_id"] != registration.User.ID || session["space_id"] != registration.Space.ID {
			t.Fatal("first start identity changed")
		}
	}
}

func TestRegisteredSpaceRejectsLostInstallationIdentity(t *testing.T) {
	a, _, _ := accessFixture(t)
	path := filepath.Join(*a.config.Server.DataDir, "installation-id")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := newServerAccess(a.config, a.internal); err == nil {
		t.Fatal("lost installation identity was replaced")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a new installation identity was written", err)
	}
}

func TestSpaceManagerAndSingleRoomRejectUnboundRequests(t *testing.T) {
	a, token, _ := accessFixture(t)
	svc, err := newService(t.TempDir(), "scoped", serviceConfig{access: a})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeService(t, svc) })
	manager := testManager(t, t.TempDir(), nil)
	manager.config.access = a
	for _, handler := range []http.Handler{svc, manager} {
		r := httptest.NewRequest("GET", a.internal+"/v1/ui", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("unbound business entry accepted", w.Code)
		}
		response := accessRequest(a.wrap(handler), "GET", "/v1/spaces/"+a.space.SpaceID+"/ui", token, "", "", nil)
		var bootstrap map[string]any
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &bootstrap) != nil || bootstrap["space_id"] != a.space.SpaceID {
			t.Fatal("business bootstrap lost Space", response.Code, response.Body.String())
		}
	}
}

// Exercise automatic first access too; startTestServer intentionally initializes
// access credentials explicitly for older authenticated server fixtures.
func startPersonalSetupServer(t *testing.T, home string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runConfiguredServer(ctx, []string{"--home", home, "--listen", "127.0.0.1:0"}, io.Discard, io.Discard, serverOptions{ready: func(value any) error {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			var info serverTestReady
			if err := json.Unmarshal(data, &info); err != nil {
				return err
			}
			ready <- info.Endpoint
			return nil
		}})
	}()
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
		case <-time.After(5 * time.Second):
			t.Error("setup shutdown timeout")
		}
	}
	t.Cleanup(stop)
	select {
	case endpoint := <-ready:
		return endpoint, stop
	case err := <-done:
		stopped = true
		cancel()
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timeout")
	}
	return "", stop
}

func TestDefaultSpaceComputerIdentitySurvivesRegistrationRestart(t *testing.T) {
	config, _ := testAccessConfig(t, t.TempDir())
	config.installationID = strings.Repeat("a", 32)
	settings := newSystemSettings(config)
	controller := func(owner string) *computerController {
		c, err := newComputerController(t.Context(), config, settings, ServiceInfo{ServiceEpoch: "epoch"}, config.installationID, owner, func(computer.Member) (bool, error) { return false, nil })
		if err != nil {
			t.Fatal(err)
		}
		c.start()
		return c
	}
	before := controller(config.Access.OperatorID)
	original := before.owner
	if err := before.close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		access, err := newServerAccess(config, "http://127.0.0.1:8088")
		if err != nil {
			t.Fatal(err)
		}
		current := controller(access.space.OwnerUserID)
		if current.owner != original {
			t.Fatal("registration changed Computer identity", current.owner, original)
		}
		if err := current.close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfiguredServiceRequiresExplicitOwnerBeforeOpeningData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "unopened")
	if _, err := newConfiguredService(dir, "no-owner", serviceConfig{}); !errors.Is(err, ErrIdentity) {
		t.Fatal("missing owner accepted", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unowned assembly opened data", err)
	}
}

func TestMCPRejectsForeignSpaceBeforeOpeningResources(t *testing.T) {
	for _, input := range []bool{false, true} {
		t.Run(fmt.Sprint(input), func(t *testing.T) {
			a, _, _ := accessFixture(t)
			token, err := randomAccessToken()
			if err != nil {
				t.Fatal(err)
			}
			key := sha256.Sum256([]byte(token))
			foreign := a.space
			foreign.SpaceID = "another"
			if input {
				a.inputBindings = map[[32]byte]*inputMCPBinding{key: {SpaceContext: foreign, Subject: a.subject()}}
			} else {
				a.mcpExecutions = map[[32]byte]*mcpExecution{key: {identity: mcpIdentity{executionBinding: executionBinding{SpaceContext: foreign, Subject: a.subject()}}}}
			}
			// Resources deliberately do not exist: a mismatch must fail before reading
			// Store/Session state or dereferencing the input execution context.
			handler := a.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("MCP reached ordinary handler") }))
			if w := accessRequest(handler, "POST", "/mcp", token, "", "{}", nil); w.Code != 403 {
				t.Fatal("foreign MCP binding accepted", w.Code)
			}
		})
	}
}

func TestRegisteredSpaceMissingAccessCannotMintAnotherOwner(t *testing.T) {
	config, _ := testAccessConfig(t, t.TempDir())
	config.installationID = strings.Repeat("a", 32)
	if _, err := newServerAccess(config, "http://127.0.0.1:8088"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(adminTokenPath(config)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.path)
	if err != nil {
		t.Fatal(err)
	}
	config.Access.OperatorID = ""
	if err := prepareFirstAccess(config); err == nil {
		t.Fatal("missing access minted a replacement User")
	}
	after, err := os.ReadFile(config.path)
	if err != nil || string(before) != string(after) {
		t.Fatal("damaged access configuration was replaced", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config.path), "first-access.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a replacement identity was reserved", err)
	}
}
