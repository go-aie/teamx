//go:build e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScionNewSpaceAccountLoginBrowser(t *testing.T) {
	requirePlatformSelectionBrowser(t)
	t.Setenv("USER", humanCaller.ID)
	exe := scionSpacesProgram(t)
	t.Setenv("TEAMX_SCION_BIN", exe)
	s, endpoint, _ := startSpaceServerFixture(t, t.TempDir())
	a := waitSpaceState(t, s, s.initial.SpaceID, "ready")
	auth := loginScionSpace(t, endpoint, a, strings.Repeat("a", 32), "alice-pat")
	saveScionSpace(t, endpoint, a, strings.Repeat("c", 32), auth)
	credential := filepath.Join(a.roots.Data, "scion-profiles", auth.Profile.Profile, "credentials.json")
	before, err := os.ReadFile(credential)
	if err != nil {
		t.Fatal(err)
	}
	b := createReadyScionSpace(t, s, endpoint, strings.Repeat("b", 32))
	card := scionSpaceAccountCard(t, endpoint, b, 0)
	input, _ := json.Marshal(map[string]any{"space_id": b.SpaceID, "room_id": card.RoomID, "room_instance": card.RoomInstance, "operation_id": card.ID})
	out := os.Getenv("TEAMX_SCION_LOGIN_OUTPUT")
	if out != "" {
		out = filepath.Join(out, "new-space")
		if err := os.MkdirAll(out, 0700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/scion_account_login_browser.cjs", endpoint, string(input), out)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	} else {
		t.Log(string(output))
	}
	b.manager.mu.Lock()
	done, readErr := b.manager.readPlatformSelectionLocked(card.ID)
	b.manager.deliverPlatformSelectionLocked(t.Context(), card.ID)
	n, notifyErr := b.manager.readSelectionNotificationLocked(done)
	file, loadErr := b.manager.readPlatforms()
	b.manager.mu.Unlock()
	if readErr != nil || done.Status != "completed" || done.Source.Seq != card.Source.Seq || done.Account.ID.Value != "alice" || notifyErr != nil || n.Receipt == nil || loadErr != nil || len(file.Platforms) != 1 {
		t.Fatal("new Space login/selection/Inbox failed", readErr, notifyErr, loadErr)
	}
	after, err := os.ReadFile(credential)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("new Space login changed default credentials", err)
	}
	if _, err := os.Stat(filepath.Join(b.roots.Data, "scion-profiles", file.Platforms[0].CLIProfile.Profile, "credentials.json")); err != nil {
		t.Fatal("new Space did not own credentials", err)
	}
}

func TestScionAccountSelectionLoginBrowser(t *testing.T) {
	requirePlatformSelectionBrowser(t)
	t.Setenv("USER", humanCaller.ID)
	for _, relogin := range []bool{false, true} {
		name := "login"
		if relogin {
			name = "relogin"
		}
		t.Run(name, func(t *testing.T) {
			h, member := platformMemberHarness(t)
			m := h.m
			exe := scionAuthProgram(t)
			m.config.environment = append(m.config.environment, "TEAMX_SCION_BIN="+exe)
			var old managedPlatform
			if relogin {
				authID := strings.Repeat("a", 32)
				startScionAuth(t, m, exe, authID, "alice-pat")
				view := awaitScionAuth(t, m, authID)
				connectionID, _ := newOpaqueID()
				old = platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: connectionID, Name: "Scion", Provider: "scion", URL: "https://scion.invalid", CLIProfile: view.Profile})
				old = verifyScion(t, m, old)
				m.mu.Lock()
				_, err := m.logoutScionLocked(scionAuthRequest{URL: old.URL, Profile: old.CLIProfile, Affected: []scionAuthPlatform{{ID: old.ID, Version: old.Version}}})
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			source, err := h.svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ClientMessageID: "scion-login-request", Text: "接入 Scion"})
			if err != nil {
				t.Fatal(err)
			}
			id, _ := newOpaqueID()
			mode := "inbox"
			selectionCall(t, m, "start", platformSelectionRequest{ServiceInfo: h.svc.info, Caller: member.Caller, OperationID: id, SourceSeq: source.Seq, Input: &platformSelectionInput{Provider: "scion", Kind: "account", Origin: "https://scion.invalid"}, Notify: &mode}, false)
			server := browserTestServer(t, m)
			defer server.Close()
			out := os.Getenv("TEAMX_SCION_LOGIN_OUTPUT")
			if out != "" {
				out = filepath.Join(out, name)
				if err := os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
			}
			input, _ := json.Marshal(map[string]any{"room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "operation_id": id, "relogin": relogin})
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/scion_account_login_browser.cjs", server.URL, string(input), out)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatal(err, string(output))
			} else {
				t.Log(string(output))
			}
			m.mu.Lock()
			done, err := m.readPlatformSelectionLocked(id)
			if err == nil {
				m.deliverPlatformSelectionLocked(t.Context(), id)
			}
			notification, notifyErr := m.readSelectionNotificationLocked(done)
			m.mu.Unlock()
			if err != nil || done.Status != "completed" || done.Account.ID.Value != "alice" || done.Source.Seq != source.Seq || done.ResultActor != humanCaller.Author {
				t.Fatal(done, err)
			}
			if notifyErr != nil || notification.Mode != "inbox" || notification.Event == nil || notification.Event.SourceSeq != source.Seq || notification.Receipt == nil {
				t.Fatal("selection result did not reach original Inbox", notification, notifyErr)
			}
			m.mu.Lock()
			file, loadErr := m.readPlatforms()
			m.mu.Unlock()
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			count := 0
			for _, c := range file.Platforms {
				if c.Provider != "scion" {
					continue
				}
				count++
				if c.ID != done.Draft.PlatformID || c.Version != done.Draft.PlatformVersion || c.Validation.Status != "valid" || relogin && c.ID != old.ID {
					t.Fatal("incorrect saved connection", c, done)
				}
			}
			if count != 1 {
				t.Fatal("duplicate or missing Scion connection", count)
			}
			calls, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "calls"))
			expected := 1
			if relogin {
				expected++
			}
			if err != nil || len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != expected || strings.Contains(string(calls), "alice-pat") {
				t.Fatal("login repeated or token exposed", err)
			}
		})
	}
}
