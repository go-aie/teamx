//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
)

func TestScionLogoutUnsettledCallKeepsCredentialsAndRevokesAdmission(t *testing.T) {
	parallelFixtureTest(t)
	m, _, _, c, _, _ := scionRoomFixture(t)
	c = verifyScion(t, m, c)
	call := &platformCall{platformID: c.ID, cancel: func() {}, done: make(chan struct{})}
	m.mu.Lock()
	m.platformCalls = map[*platformCall]bool{call: true}
	_, err := m.logoutScionLocked(scionAuthRequest{URL: c.URL, Profile: c.CLIProfile, Affected: []scionAuthPlatform{{ID: c.ID, Version: c.Version}}})
	m.finishPlatformCallLocked(call)
	file, loadErr := m.readPlatforms()
	m.mu.Unlock()
	if !errors.Is(err, ErrBusy) || loadErr != nil {
		t.Fatal("unsettled logout reported success", err, loadErr)
	}
	if _, err := os.Stat(filepath.Join(m.store.dir, "scion-profiles", c.CLIProfile.Profile, "credentials.json")); err != nil {
		t.Fatal("credentials removed without settlement", err)
	}
	found := false
	for _, record := range file.Platforms {
		if record.ID == c.ID {
			found = true
			if _, _, err := registry.ResolveAccessAccount(file, record); err == nil {
				t.Fatal("new admission survived failed settlement")
			}
		}
	}
	if !found {
		t.Fatal("logout deleted the connection configuration")
	}
}

func TestScionAuthDiscoveryLoginSaveAndNoSecretResponses(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), nil)
	exe := scionAuthProgram(t)
	id := strings.Repeat("a", 32)
	discover := func() scion.Discovery {
		t.Helper()
		r := managerHTTP(m, "/v1/platforms/scion/discover", scionAuthBody(m, map[string]any{"url": "https://scion.invalid", "executable": exe}))
		var d scion.Discovery
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &d) != nil || strings.Contains(r.Body.String(), "alice-pat") {
			t.Fatal(r.Code, r.Body.String())
		}
		return d
	}
	if len(discover().Profiles) != 0 {
		t.Fatal("unexpected global login discovery")
	}
	startScionAuth(t, m, exe, id, "alice-pat")
	view := awaitScionAuth(t, m, id)
	if view.Status != "succeeded" || view.Username != "alice" || view.Profile == nil || view.Profile.Profile == id {
		t.Fatal("invalid account result", view)
	}
	profiles := discover().Profiles
	if len(profiles) != 1 || profiles[0].Username != "alice" || profiles[0].Profile != view.Profile.Profile {
		t.Fatal(profiles)
	}
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("c", 32), Name: "Scion", Provider: "scion", URL: "https://scion.invalid", CLIProfile: view.Profile})
	c = verifyScion(t, m, c)
	oldProfile := *c.CLIProfile
	before, _ := os.ReadFile(filepath.Join(m.store.dir, "scion-profiles", oldProfile.Profile, "credentials.json"))
	startScionAuth(t, m, exe, strings.Repeat("b", 32), "bob-pat")
	replacement := awaitScionAuth(t, m, strings.Repeat("b", 32))
	if replacement.Status != "succeeded" || replacement.Profile.Profile == oldProfile.Profile {
		t.Fatal("replacement mutated old profile")
	}
	after, _ := os.ReadFile(filepath.Join(m.store.dir, "scion-profiles", oldProfile.Profile, "credentials.json"))
	if string(before) != string(after) {
		t.Fatal("in-place credential mutation")
	}
	unchanged := platformTestList(t, m)
	if len(unchanged) != 1 || unchanged[0].Version != c.Version || unchanged[0].Validation.Username != "alice" {
		t.Fatal("login silently changed saved connection")
	}
	r := managerHTTP(m, "/v1/platforms/scion/login", scionAuthBody(m, map[string]any{"session_id": id, "url": "https://scion.invalid", "executable": exe, "pat": "alice-pat"}))
	if r.Code != 409 {
		t.Fatal("duplicate login ID was replayed")
	}
	calls, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "calls"))
	if len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 2 || strings.Contains(string(calls), "-pat") {
		t.Fatal("secret in argv or duplicate login")
	}
	disk, _ := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json"))
	if strings.Contains(string(disk), "alice-pat") || strings.Contains(string(disk), "bob-pat") || strings.Contains(string(disk), id) {
		t.Fatal("auth secret/session in connection snapshot")
	}
	for _, bad := range []map[string]any{{"caller": agentCaller("assistant", "member"), "session_id": id}, {"caller": Caller{Author: Author{Kind: "human", ID: "other"}}, "session_id": id}, {"service_epoch": "stale", "session_id": id}} {
		if r := managerHTTP(m, "/v1/platforms/scion/status", scionAuthBody(m, bad)); r.Code == 200 {
			t.Fatal("borrowed authentication accepted")
		}
	}
	raw, _ := json.Marshal(scionAuthRequest{PAT: scionPAT("do-not-print")})
	if strings.Contains(string(raw), "do-not-print") || strings.Contains(fmt.Sprintf("%+v", scionAuthRequest{PAT: scionPAT("do-not-print")}), "do-not-print") {
		t.Fatal("request diagnostics expose PAT")
	}
}

func TestScionAuthFailureCancelAndShutdownSettleCLI(t *testing.T) {
	parallelFixtureTest(t)
	for _, action := range []string{"reject", "cancel", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			m := testManager(t, t.TempDir(), nil)
			exe := scionAuthProgram(t)
			id := strings.Repeat("a", 32)
			pat := "rejected-pat"
			if action != "reject" {
				pat = "alice-pat"
				os.WriteFile(filepath.Join(filepath.Dir(exe), "mode"), []byte("block"), 0600)
			}
			startScionAuth(t, m, exe, id, pat)
			if action == "reject" {
				v := awaitScionAuth(t, m, id)
				if v.Status != "failed" || v.Profile != nil || strings.Contains(v.ErrorCode, pat) {
					t.Fatal(v)
				}
			} else {
				deadline := time.Now().Add(4 * time.Second)
				for {
					if _, e := os.Stat(filepath.Join(filepath.Dir(exe), "started")); e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("CLI not started")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if action == "cancel" {
					r := managerHTTP(m, "/v1/platforms/scion/cancel", scionAuthBody(m, map[string]any{"session_id": id}))
					if r.Code != 200 || !strings.Contains(r.Body.String(), "cancelled") {
						t.Fatal(r.Code, r.Body.String())
					}
				} else {
					if err := m.close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			entries, _ := os.ReadDir(filepath.Join(m.store.dir, "scion-profiles"))
			if len(entries) != 0 {
				t.Fatal("failed or cancelled profile survived", entries)
			}
			m.mu.Lock()
			done := m.scionAuthSessions[id].done
			m.mu.Unlock()
			select {
			case <-done:
			default:
				t.Fatal("CLI remains owned after cancellation")
			}
		})
	}
}

func TestScionAuthHTTPInputGuards(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), nil)
	exe := scionAuthProgram(t)
	base := map[string]any{"session_id": strings.Repeat("a", 32), "url": "https://scion.invalid", "executable": exe, "pat": "alice-pat"}
	for _, bad := range []map[string]any{{"caller": agentCaller("assistant", "member")}, {"service_epoch": "stale"}, {"url": "https://name:secret@scion.invalid"}, {"url": "http://scion.invalid"}, {"session_id": "../escape"}, {"pat": "bad\ntoken"}, {"profile": "caller-chosen"}} {
		q := map[string]any{}
		for k, v := range base {
			q[k] = v
		}
		for k, v := range bad {
			q[k] = v
		}
		if r := managerHTTP(m, "/v1/platforms/scion/login", scionAuthBody(m, q)); r.Code == 200 {
			t.Fatal("invalid authentication input admitted")
		}
	}
	if r := managerHTTP(m, "/v1/platforms/scion/login", nil); r.Code != 405 {
		t.Fatal(r.Code)
	}
	if r := managerHTTP(m, "/v1/platforms/scion/unknown", scionAuthBody(m, nil)); r.Code != 404 {
		t.Fatal(r.Code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "calls")); !os.IsNotExist(err) {
		t.Fatal("rejected request started CLI")
	}
}

func TestScionLogoutSharedProfilesGuardsPersistenceAndLateVerification(t *testing.T) {
	parallelFixtureTest(t)
	root := t.TempDir()
	m := testManager(t, root, nil)
	exe := scionAuthProgram(t)
	startScionAuth(t, m, exe, strings.Repeat("a", 32), "alice-pat")
	view := awaitScionAuth(t, m, strings.Repeat("a", 32))
	if view.Profile == nil {
		t.Fatal(view)
	}
	var saved []managedPlatform
	for _, id := range []string{"c", "d"} {
		c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat(id, 32), Name: id, Provider: "scion", URL: "https://scion.invalid", CLIProfile: view.Profile})
		saved = append(saved, verifyScion(t, m, c))
	}
	startScionAuth(t, m, exe, strings.Repeat("b", 32), "bob-pat")
	other := awaitScionAuth(t, m, strings.Repeat("b", 32))
	if other.Profile == nil {
		t.Fatal(other)
	}
	credential := filepath.Join(m.store.dir, "scion-profiles", view.Profile.Profile, "credentials.json")
	acks := []scionAuthPlatform{{ID: saved[0].ID, Version: saved[0].Version}, {ID: saved[1].ID, Version: saved[1].Version}}
	base := map[string]any{"url": "https://scion.invalid", "cli_profile": view.Profile, "affected": acks}
	for _, bad := range []map[string]any{
		{"caller": agentCaller("assistant", "member")}, {"service_epoch": "stale"},
		{"affected": []scionAuthPlatform{}}, {"affected": []scionAuthPlatform{acks[0], acks[0]}},
		{"affected": []scionAuthPlatform{{ID: saved[0].ID, Version: 1}, acks[1]}},
		{"affected": []scionAuthPlatform{{ID: saved[0].ID, Version: saved[0].Version, ReferencedRooms: []RoomIdentity{{RoomID: "foreign", RoomInstance: "foreign"}}}, acks[1]}},
		{"url": "https://other.invalid"}, {"pat": "must-not-be-accepted"}, {"session_id": strings.Repeat("f", 32)},
	} {
		extra := map[string]any{}
		for k, v := range base {
			extra[k] = v
		}
		for k, v := range bad {
			extra[k] = v
		}
		if r := managerHTTP(m, "/v1/platforms/scion/logout", scionAuthBody(m, extra)); r.Code == 200 {
			t.Fatal("invalid logout admitted", r.Body.String())
		}
		if _, err := os.Stat(credential); err != nil {
			t.Fatal("rejected logout changed credentials", err)
		}
	}
	m.mu.Lock()
	ticket, err := m.platforms.BeginVerification(saved[0].ID, saved[0].Version)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	validation := m.platforms.Query(context.Background(), ticket)
	r := managerHTTP(m, "/v1/platforms/scion/logout", scionAuthBody(m, base))
	if r.Code != 200 || strings.Contains(r.Body.String(), "alice-pat") {
		t.Fatal(r.Code, r.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(credential)); !os.IsNotExist(err) {
		t.Fatal("profile survived", err)
	}
	if _, err := os.Stat(filepath.Join(m.store.dir, "scion-profiles", other.Profile.Profile, "credentials.json")); err != nil {
		t.Fatal("other profile removed", err)
	}
	m.mu.Lock()
	_, err = m.platforms.CompleteVerification(ticket, validation)
	m.mu.Unlock()
	if !errors.Is(err, registry.ErrConflict) {
		t.Fatal("late verification restored logged out account", err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restarted := testManager(t, root, nil)
	platforms := platformTestList(t, restarted)
	if len(platforms) != 2 {
		t.Fatal("connection configuration lost")
	}
	for _, c := range platforms {
		if c.Validation.Status != "failed" || c.Validation.ErrorCode != "credential_unavailable" || c.Version <= saved[0].Version {
			t.Fatal("logout did not persist", c)
		}
	}
	startScionAuth(t, restarted, exe, strings.Repeat("e", 32), "alice-pat")
	relogin := awaitScionAuth(t, restarted, strings.Repeat("e", 32))
	if relogin.Profile == nil || relogin.Profile.Profile == view.Profile.Profile {
		t.Fatal("relogin did not create a fresh profile")
	}
	c := platforms[0]
	c = platformTestSave(t, restarted, managedPlatformRequest{ServiceEpoch: restarted.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version, Name: c.Name, Provider: c.Provider, URL: c.URL, CLIProfile: relogin.Profile})
	c = verifyScion(t, restarted, c)
	if c.ID != platforms[0].ID || c.Validation.Username != "alice" {
		t.Fatal("relogin lost connection configuration", c)
	}
	for _, other := range platformTestList(t, restarted) {
		if other.ID != c.ID && other.Validation.Status != "failed" {
			t.Fatal("relogin restored an unrelated connection")
		}
	}
}

func TestScionLogoutWaitsBeforeDeletingCredentialsAndRejectsConcurrentEdits(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), nil)
	exe := scionAuthProgram(t)
	startScionAuth(t, m, exe, strings.Repeat("a", 32), "alice-pat")
	view := awaitScionAuth(t, m, strings.Repeat("a", 32))
	if view.Profile == nil {
		t.Fatal(view)
	}
	c := verifyScion(t, m, platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("c", 32), Name: "Scion", Provider: "scion", URL: "https://scion.invalid", CLIProfile: view.Profile}))
	ctx, cancel := context.WithCancel(context.Background())
	call := &platformCall{platformID: c.ID, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	m.platformCalls = map[*platformCall]bool{call: true}
	m.mu.Unlock()
	t.Cleanup(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.platformCalls[call] {
			m.finishPlatformCallLocked(call)
		}
	})
	result := make(chan int, 1)
	go func() {
		r := managerHTTP(m, "/v1/platforms/scion/logout", scionAuthBody(m, map[string]any{"url": c.URL, "cli_profile": c.CLIProfile, "affected": []scionAuthPlatform{{ID: c.ID, Version: c.Version}}}))
		result <- r.Code
	}()
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("old call not cancelled")
	}
	// Wait for the join to release the catalog gate; cancellation alone is not settlement.
	m.mu.Lock()
	busy := m.busy
	m.mu.Unlock()
	if !busy {
		t.Fatal("logout did not own the catalog while settling")
	}
	m.mu.Lock()
	file, err := m.readPlatforms()
	if err == nil {
		_, _, err = registry.ResolveAccessAccount(file, file.Platforms[0])
	}
	m.mu.Unlock()
	if err == nil {
		t.Fatal("new credential admission remained available while settling")
	}
	if _, err := os.Stat(filepath.Join(m.store.dir, "scion-profiles", c.CLIProfile.Profile, "credentials.json")); err != nil {
		t.Fatal("credentials removed before settlement", err)
	}
	if _, err := m.updatePlatform(managedPlatformRequest{ID: c.ID, Version: c.Version, Name: "rename", Provider: c.Provider, URL: c.URL}, false); !errors.Is(err, ErrBusy) {
		t.Fatal("edit crossed logout boundary", err)
	}
	m.finishPlatformCall(call)
	select {
	case code := <-result:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("logout did not finish")
	}
}
