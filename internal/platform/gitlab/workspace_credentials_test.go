package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAskpassValidatesRepositoryBeforeCredentials(t *testing.T) {
	b := eventTestBinding()
	calls := 0
	env := func(k string) string {
		calls++
		if k == "ROOM_PAT" {
			return "offline-secret"
		}
		return ""
	}
	for _, prompt := range []string{"", "Password for 'https://evil.invalid/group/project.git': ", "Password for 'https://gitlab.example.com/group/other.git': ", "Password for 'https://oauth2:bad@gitlab.example.com/group/project.git': ", "Password for 'https://gitlab.example.com/group/project.git?': ", "Password for 'https://gitlab.example.com/group/project.git#x': ", "Password for 'https://gitlab.example.com/group': "} {
		if _, err := Askpass(context.Background(), b, prompt, env, nil); err == nil {
			t.Fatal("accepted foreign target")
		}
	}
	if calls != 0 {
		t.Fatal("resolved credential for invalid target")
	}
	answer, err := Askpass(context.Background(), b, "Username for 'https://gitlab.example.com/group/project.git': ", env, nil)
	if err != nil || answer != "oauth2" || calls != 0 {
		t.Fatal(err)
	}
	answer, err = Askpass(context.Background(), b, "Password for 'https://oauth2@gitlab.example.com/group/project.git': ", env, nil)
	if err != nil || answer != "offline-secret" || calls != 1 {
		t.Fatal("password resolution", err)
	}
}
func TestPrivateCredentialOwnershipModesAndBinding(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	b := eventTestBinding()
	b.TokenEnv = ""
	b.CredentialID = strings.Repeat("a", 32)
	b.CredentialFile = filepath.Join(dir, b.CredentialID+".json")
	record := map[string]any{"version": 1, "room_id": b.RoomID, "room_instance": b.RoomInstance, "credential_id": b.CredentialID, "pat": "private-secret"}
	save := func() {
		data, _ := json.Marshal(record)
		if err := os.WriteFile(b.CredentialFile, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	secret, err := WorkspaceCredential(context.Background(), b, func(string) string { return "" }, nil)
	if err != nil || secret.Reveal() != "private-secret" {
		t.Fatal(err)
	}
	record["room_instance"] = "other"
	save()
	if _, err := WorkspaceCredential(context.Background(), b, os.Getenv, nil); !errors.Is(err, errors.New("unused")) && err == nil {
		t.Fatal("foreign credential accepted")
	}
	record["room_instance"] = b.RoomInstance
	save()
	os.Chmod(b.CredentialFile, 0644)
	if _, err := WorkspaceCredential(context.Background(), b, os.Getenv, nil); err == nil {
		t.Fatal("public credential accepted")
	}
	os.Chmod(b.CredentialFile, 0600)
	os.Chmod(dir, 0755)
	if _, err := WorkspaceCredential(context.Background(), b, os.Getenv, nil); err == nil {
		t.Fatal("public credential directory accepted")
	}
	os.Chmod(dir, 0700)
	bindingPath := filepath.Join(dir, "binding.json")
	data, _ := json.Marshal(b)
	os.WriteFile(bindingPath, data, 0600)
	if got, err := ReadWorkspaceBinding(bindingPath); err != nil || got != b {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.json")
	os.Symlink(bindingPath, alias)
	if _, err := ReadWorkspaceBinding(alias); err == nil {
		t.Fatal("symlink binding accepted")
	}
}
func TestSharedAskpassExchangeScopeAndNoRedirect(t *testing.T) {
	b := eventTestBinding()
	b.TokenEnv = ""
	b.ConnectionID = strings.Repeat("a", 32)
	b.ConnectionGeneration = strings.Repeat("b", 32)
	values := map[string]string{"TEAMX_ENDPOINT": "http://127.0.0.1:1234", "TEAMX_ROOM_ID": b.RoomID, "TEAMX_ROOM_INSTANCE": b.RoomInstance, "TEAMX_SERVICE_EPOCH": "epoch", "TEAMX_AGENT_ID": "agent", "TEAMX_MEMBERSHIP_ID": "member"}
	env := func(k string) string { return values[k] }
	calls := 0
	body := map[string]any{"token": "shared-secret", "url": b.URL, "project": b.Project, "project_id": b.ProjectID, "connection_id": b.ConnectionID, "connection_generation": b.ConnectionGeneration}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.String() != "http://127.0.0.1:1234/v1/workspace/tool-credential" || r.Header.Get("X-TeamX-Tool") != "credential" {
			t.Fatal("incorrect private exchange")
		}
		payload, _ := io.ReadAll(r.Body)
		if strings.Contains(string(payload), "secret") || !strings.Contains(string(payload), `"service_epoch":"epoch"`) {
			t.Fatal("incorrect private payload")
		}
		data, _ := json.Marshal(body)
		return fixtureResponse(string(data), 200, nil), nil
	})
	token, err := Askpass(context.Background(), b, "Password for 'https://oauth2@gitlab.example.com/group/project.git': ", env, transport)
	if err != nil || token != "shared-secret" || calls != 1 {
		t.Fatal("shared exchange", err, calls)
	}
	for _, key := range []string{"url", "project", "project_id", "connection_generation", "connection_id"} {
		original := body[key]
		body[key] = "other"
		if _, err := WorkspaceCredential(context.Background(), b, env, transport); err == nil {
			t.Fatal("changed scope accepted", key)
		}
		body[key] = original
	}
	calls = 0
	values["TEAMX_ENDPOINT"] = "http://evil.invalid"
	if _, err := WorkspaceCredential(context.Background(), b, env, transport); err == nil || calls != 0 {
		t.Fatal("nonlocal exchange accepted")
	}
	values["TEAMX_ENDPOINT"] = "http://127.0.0.1:1234"
	calls = 0
	transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return fixtureResponse("", 302, http.Header{"Location": {"http://evil.invalid"}}), nil
	})
	if _, err := WorkspaceCredential(context.Background(), b, env, transport); err == nil || calls != 1 {
		t.Fatal("redirect followed", err, calls)
	}
}
