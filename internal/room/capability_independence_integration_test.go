//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestSecondPlatformProviderRequiresToolTypeRegistration(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, gitlabAccount, members := cliToolFixture(t, `test -z "$GITLAB_TOKEN$GITLAB_HOST" || exit 91
test -z "$FIXTURE_TOKEN" || exit 92
test "$FIXTURE_INSTANCE" = https://fixture.invalid || exit 93
printf '%s' "$1"
`)

	verified := 0
	m.connectionProviders["fixture"] = connectionProvider{Normalize: registry.Normalize, ValidProfile: func(string, platform.CLIProfile) bool { return true }, VerifyProfile: func(_ context.Context, origin string, profile platform.CLIProfile) platform.Validation {
		verified++
		if origin != "https://fixture.invalid" || profile.Profile != "fixture" {
			t.Error("incorrect provider verification context")
		}
		return platform.Validation{Status: "valid", Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "fixture", Instance: origin}, ID: platform.ExternalID{Namespace: "fixture:account", Value: "opaque-user"}}, Username: "fixture-user", CheckedAt: time.Now().UTC()}
	}}
	d := m.toolCatalog[0]
	d.Name, d.Provider = "fixture-cli", "fixture"
	d.Environment = map[string]string{"FIXTURE_TOKEN": "token", "FIXTURE_INSTANCE": "instance"}
	m.toolCatalog = append(m.toolCatalog, d)
	req := platformTestRequest(m)
	req.ID, req.Name, req.Provider, req.URL = strings.Repeat("b", 32), "Fixture", "fixture", "https://fixture.invalid"
	req.CLIProfile = &platform.CLIProfile{Version: 1, Executable: "/fixture-cli", Profile: "fixture"}
	c, err := m.updatePlatform(req, false)
	if err != nil {
		t.Fatal(err)
	}
	c, err = m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || verified != 1 || c.Validation.Username != "fixture-user" {
		t.Fatal("registered verifier was not used", err)
	}
	m.mu.Lock()
	bindings, err := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(bindings.Bindings) != 0 || len(m.notifications.StatesLocked(room.RoomIdentity)) != 0 {
		t.Fatal("connection registration implicitly enabled another capability", err)
	}
	wrong := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Enabled: true,
		Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: d.Name, PlatformID: gitlabAccount.ID}, PlatformVersion: gitlabAccount.Version}
	if r := managerHTTP(m, "/v1/tools/bind", wrong); r.Code != 400 {
		t.Fatal("tool accepted another provider's account", r.Code)
	}
	// A verifier alone does not register a public tool implementation. The
	// static platform type catalog must opt in before an entry can be enabled.
	wrong.Binding.PlatformID, wrong.PlatformVersion = c.ID, c.Version
	if r := managerHTTP(m, "/v1/tools/bind", wrong); r.Code != 400 {
		t.Fatal("unregistered tool type acquired a binding", r.Code)
	}
	client, endpoint := toolPipeServer(t, m)
	var out, stderr bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"run", d.Name, "opaque arg"}, strings.NewReader(""), &out, &stderr, cliToolEnv(endpoint, svc, members[0]), client); err == nil {
		t.Fatal("unregistered type executed", out.String(), stderr.String())
	}
	m.mu.Lock()
	_, _, err = m.resolvePlatformAccountLocked(room.RoomIdentity, c.ID, "gitlab")
	m.mu.Unlock()
	if !errors.Is(err, ErrRequest) {
		t.Fatal("GitLab notification admission accepted another provider", err)
	}
	req.ID, req.Provider = strings.Repeat("c", 32), "unregistered"
	if _, err := m.updatePlatform(req, false); !errors.Is(err, ErrRequest) {
		t.Fatal("unknown provider accepted", err)
	}
}

func TestLocalGitFetchPushAndExactSHA(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.config.Workspace.Binding = testGitLabWorkspace().Binding
	bare := filepath.Join(t.TempDir(), "remote.git")
	workspaceGit(t, f.source, "clone", "--bare", f.source, bare)
	f.source = bare
	prepared, err := f.ensure(true)
	if err != nil {
		t.Fatal(err)
	}
	dir := f.member(0)
	base := workspaceGit(t, dir, "rev-parse", "HEAD")
	workspaceGit(t, dir, "checkout", "-b", "workspace-regression")
	if err := os.WriteFile(filepath.Join(dir, "change"), []byte("new commit"), 0600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, dir, "add", "change")
	workspaceGit(t, dir, "commit", "-m", "local regression")
	head := workspaceGit(t, dir, "rev-parse", "HEAD")
	// Git transport stays entirely local. Keep canonical HTTPS origin and the
	// prepared environment; Go askpass is exercised separately by the production Git askpass binary test.
	rewrite := "url.file://" + bare + ".insteadOf=https://gitlab.invalid/group/project.git"
	if _, err := runPreparation(context.Background(), dir, prepared.ToolEnv, "git", "-c", rewrite, "push", "origin", "HEAD:refs/heads/workspace-regression"); err != nil {
		t.Fatal("local Git push", err)
	}
	// Fetch into the other independent clone, which does not yet have head.
	dir = f.member(1)
	if _, err := runPreparation(context.Background(), dir, prepared.ToolEnv, "git", "cat-file", "-e", head); err == nil {
		t.Fatal("fetch fixture already contains the new commit")
	}
	for _, args := range [][]string{{"fetch", "origin", "workspace-regression"}, {"checkout", "--detach", base}, {"checkout", "--detach", head}} {
		if _, err := runPreparation(context.Background(), dir, prepared.ToolEnv, "git", append([]string{"-c", rewrite}, args...)...); err != nil {
			t.Fatal("local Git regression", args, err)
		}
		if len(args) == 3 && args[0] == "checkout" && workspaceGit(t, dir, "rev-parse", "HEAD") != args[2] {
			t.Fatal("checkout did not select the exact SHA")
		}
	}
	if workspaceGit(t, bare, "rev-parse", "refs/heads/workspace-regression") != head {
		t.Fatal("push did not reach local remote")
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("dirty retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ensure(false); err != nil {
		t.Fatal(err)
	}
	if workspaceGit(t, dir, "rev-parse", "HEAD") != head || string(readCapabilityFile(t, filepath.Join(dir, "README"))) != "dirty retained" {
		t.Fatal("Workspace revisit changed reviewed SHA or local files")
	}
}

func TestAccountlessToolWithoutPlatformsOrWorkspace(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "local-tool")
	_, snapshot := readManagedRoom(t, m, room)
	entry, _ := m.workspaceRoom(room.RoomIdentity)
	config, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		t.Fatal(err)
	}
	// The example prepares ordinary member directories. Remove only this
	// temporary fixture's directory to exercise the private-directory fallback.
	if err := os.RemoveAll(filepath.Join(m.roomDir(room.RoomID), config.Members[0].Workspace)); err != nil {
		t.Fatal(err)
	}
	svc := eventLiveService(t, &eventHarness{m: m, room: room})
	bin := filepath.Join(t.TempDir(), "local-tool")
	if err := os.WriteFile(bin, []byte(`#!/bin/sh
test -z "$GITLAB_TOKEN$FIXTURE_TOKEN" || exit 91
test "$PWD" -ef "$HOME" || exit 92
printf '%s\n' "$@"
cat
`), 0700); err != nil {
		t.Fatal(err)
	}
	m.toolCatalog = []tool.Definition{{Name: "local-fixture", Version: "1", Executable: bin}}
	// Even an unreadable connection catalog must not block this capability.
	path := filepath.Join(m.store.dir, "platform-connections.json")
	if err := os.WriteFile(path, []byte("invalid connection snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	f := bindCLITool(t, m, room.RoomIdentity, snapshot.Members[0], managedPlatform{}, "local-fixture", 0)
	if len(f.Bindings) != 1 || f.Bindings[0].PlatformID != "" {
		t.Fatal("local tool required an account")
	}
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, snapshot.Members[0])
	var out, stderr bytes.Buffer
	if err := runToolsWithClient(context.Background(), []string{"list"}, nil, &out, &stderr, env, client); err != nil || !strings.Contains(out.String(), "local-fixture") {
		t.Fatal(err, out.String())
	}
	out.Reset()
	if err := runToolsWithClient(context.Background(), []string{"run", "local-fixture", "a b", "--", "$HOME"}, strings.NewReader("input\x00data"), &out, &stderr, env, client); err != nil || out.String() != "a b\n--\n$HOME\ninput\x00data" {
		t.Fatal(err, out.String(), stderr.String())
	}
}

func TestSecondNotificationProviderReloadAndDedup(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	seedEventInbox(t, h, 1)
	gitlabBefore := h.state()
	svc := eventLiveService(t, h)
	// Compose before starting workers; no production fixture or dynamic plugin.
	source := fixtureNotificationSource{records: []string{"opaque-z", "opaque-z", "opaque-a"}}
	h.m.notificationAdapters[fixtureNotificationSourceID] = fixtureNotificationAdapter(source)
	h.m.initPlatformNotifications()
	if err := h.m.loadPlatformNotificationsLocked(); err != nil {
		t.Fatal(err)
	}
	key := notification.Key{Owner: h.room.RoomIdentity, Source: fixtureNotificationSourceID, SubscriptionID: gitlabBefore.SubscriptionID}
	initial := fixtureNotificationState{Format: 53, Key: key, Version: 1, Enabled: true, Seen: map[string]bool{}, Inbox: map[string]notification.Item{}, RuntimeState: notification.RuntimeState{Status: "running"}}
	h.m.mu.Lock()
	err := h.m.notifications.SaveLocked(initial)
	runner := h.m.notifications.NewRunnerLocked(key)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, stop := runner.Step(context.Background()); stop {
		t.Fatal("second provider could not query through common admission")
	}
	// Commit to Room, then lose the inbox ack. Reload both source codecs from
	// disk and replay the stable delivery ID through the production Room path.
	original := h.m.store.ops.createTemp
	h.m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("injected ack failure") }
	err = h.m.notifications.Deliver(context.Background(), key, 1)
	h.m.store.ops.createTemp = original
	if err == nil {
		t.Fatal("lost acknowledgement was hidden")
	}
	h.m.initPlatformNotifications()
	if err := h.m.loadPlatformNotificationsLocked(); err != nil {
		t.Fatal("second provider codec could not reload", err)
	}
	h.m.mu.Lock()
	state, _ := h.m.notifications.StateLocked(key)
	runner = h.m.notifications.NewRunnerLocked(key)
	h.m.mu.Unlock()
	if s := state.(fixtureNotificationState); s.Offset != 1 || len(s.Inbox) != 1 {
		t.Fatal("reload lost source progress or unacknowledged item")
	}
	for range 2 {
		if _, stop := runner.Step(context.Background()); stop {
			t.Fatal("resumed provider stopped before completing its offset feed")
		}
	}
	for range 2 {
		if err := h.m.notifications.Deliver(context.Background(), key, 1); err != nil {
			t.Fatal(err)
		}
	}
	h.m.mu.Lock()
	state, _ = h.m.notifications.StateLocked(key)
	h.m.mu.Unlock()
	if s := state.(fixtureNotificationState); s.Offset != 3 || len(s.Seen) != 2 || len(s.Inbox) != 0 {
		t.Fatal("duplicate record or reload changed progress")
	}
	if !reflect.DeepEqual(h.state(), gitlabBefore) {
		t.Fatal("second provider changed GitLab state with the same subscription ID")
	}
	saved, err := svc.store.LoadRoom()
	if err != nil || len(saved.Messages) != 2 || len(saved.IntegrationReceipts) != 2 {
		t.Fatal("delivery lost or duplicated messages", err)
	}
	for _, message := range saved.Messages {
		if message.Source == nil || message.Source.Provider != "fixture" || message.Source.ProjectID != 0 {
			t.Fatal("delivery required a GitLab project or rewrote the source")
		}
	}
}

func TestToolDisablePreservesNotificationsWorkspaceAndSession(t *testing.T) {
	parallelFixtureTest(t)
	for _, channel := range []string{"events", "todos"} {
		t.Run(channel, func(t *testing.T) {
			var m *roomManager
			var room roomSummary
			var meta notification.Metadata
			if channel == "events" {
				h, _ := newIndependentEventHarness(t)
				seedEventInbox(t, h, 1)
				m, room, meta = h.m, h.room, h.state().Metadata()
			} else {
				h := newTodoHarness(t)
				h.pages.pending = []gitlab.Todo{h.todo(1, 0, "pending")}
				h.full()
				m, room, meta = h.m, h.room, h.state().Metadata()
			}
			entry, _ := m.workspaceRoom(room.RoomIdentity)
			config, err := m.readWorkspaceRoomConfig(entry)
			if err != nil {
				t.Fatal(err)
			}
			svc := eventLiveService(t, &eventHarness{m: m, room: room})
			beforeRoom, err := svc.store.LoadRoom()
			if err != nil {
				t.Fatal(err)
			}
			member := beforeRoom.Members[0]
			// Give the member actual files; tool changes must preserve them.
			dir := filepath.Join(m.roomDir(room.RoomID), config.Members[0].Workspace)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dir, "local-work")
			if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
			beforeConfig := readCapabilityFile(t, configPath)
			m.mu.Lock()
			before, _ := m.notifications.StateLocked(meta.Key)
			m.mu.Unlock()
			c := platformTestList(t, m)[0]
			m.toolCatalog = []tool.Definition{{Name: "fixture-tool", Provider: "gitlab", Executable: "/unused"}}
			f := bindCLITool(t, m, room.RoomIdentity, member, c, "fixture-tool", 0)
			r := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: f.Revision,
				Binding: tool.Binding{AgentID: member.AgentID, MembershipID: member.MembershipID, Tool: "fixture-tool", PlatformID: c.ID}})
			if r.Code != 200 {
				t.Fatal(r.Code, r.Body.String())
			}
			m.mu.Lock()
			after, _ := m.notifications.StateLocked(meta.Key)
			_, call, _, admissionErr := m.prepareToolLocked(context.Background(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(member.AgentID, member.MembershipID), Tool: "fixture-tool"})
			m.mu.Unlock()
			if call != nil {
				m.finishPlatformCall(call)
			}
			if admissionErr == nil || !reflect.DeepEqual(before, after) || !bytes.Equal(beforeConfig, readCapabilityFile(t, configPath)) || string(readCapabilityFile(t, marker)) != "keep" {
				t.Fatal("tool disable admitted a new call or changed independent state")
			}
			afterRoom, err := svc.store.LoadRoom()
			if err != nil || !reflect.DeepEqual(beforeRoom, afterRoom) {
				t.Fatal("tool binding changed Room members or Sessions", err)
			}
			if err := m.notifications.Deliver(context.Background(), meta.Key, meta.Version); err != nil {
				t.Fatal("disabled tool blocked notification delivery", err)
			}
			afterRoom, err = svc.store.LoadRoom()
			if err != nil || len(afterRoom.Messages) != len(beforeRoom.Messages)+1 {
				t.Fatal("disabled tool lost pending notification", err)
			}
		})
	}
}
