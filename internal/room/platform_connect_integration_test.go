//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func connectWait(t *testing.T, m *roomManager, req platformConnectRequest, predicate func(platformConnectOperation) bool) platformConnectOperation {
	t.Helper()
	req.Input = nil
	req.SourceSeq = 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		op := connectHTTP(t, m, "status", req)
		if predicate(op) {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connection did not reach expected stage")
	return platformConnectOperation{}
}
func TestPlatformConnectNativeSetupAndCLIProcessLifetime(t *testing.T) {
	m, svc, member, req := connectFixture(t)
	bin, dir := roomSetupFixture(t, "block")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	server := httptest.NewServer(m)
	defer server.Close()
	binary := productionFixtureCLI(t)
	env := cliToolEnv(server.URL, svc, member)
	command := exec.Command(binary, "platforms", "connect", "start", "--provider", "lark", "--name", req.Input.Name, "--operation-id", req.OperationID, "--source-seq", "1", "--notify", "inbox")
	command.Env = os.Environ()
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		command.Env = append(command.Env, key+"="+env(key))
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	var started platformConnectOperation
	if json.Unmarshal(output, &started) != nil || started.ID != req.OperationID {
		t.Fatal(string(output))
	}
	// start's real CLI process has exited, while service-owned authorization waits.
	waiting := connectWait(t, m, req, func(op platformConnectOperation) bool { return op.Stage == "application" && op.Interaction.URL != "" })
	if waiting.Status != "waiting" || waiting.Interaction.QRCodeURL == "" {
		t.Fatal(waiting)
	}
	before, _ := os.ReadFile(m.connectPath(req.OperationID))
	if bytes.Contains(before, []byte("https://open.feishu.cn/page/cli?")) || bytes.Contains(before, []byte("device_code")) {
		t.Fatal("temporary authorization persisted")
	}
	if w := managerHTTP(m, "/v1/platforms/lark/setup", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Executable: bin, Brand: "feishu"}); w.Code != 409 {
		t.Fatal("settings bypassed shared setup reservation", w.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	observer := exec.CommandContext(ctx, binary, "platforms", "connect", "wait", "--operation-id", req.OperationID, "--timeout", "20s")
	observer.Env = command.Env
	if err := observer.Run(); err == nil {
		t.Fatal("wait did not interrupt")
	}
	setupRelease(t, dir, "app")
	authorized := connectWait(t, m, req, func(op platformConnectOperation) bool { return op.Stage == "authorization" && op.Interaction.URL != "" })
	if authorized.Application.Status != "applied" || authorized.Connection.Status == "connected" {
		t.Fatal("app setup reported connection success", authorized)
	}
	setupRelease(t, dir, "auth")
	done := connectWait(t, m, req, func(op platformConnectOperation) bool { return connectTerminal(op.Status) })
	if done.Status != "completed" || done.Connection.Status != "connected" || done.Account.ID.Value != "ou_reader" {
		t.Fatal(done)
	}
	if n := waitConnectDelivery(t, m, req.OperationID); n.Event.EventType != "operation_completed" || n.Receipt.Seq != 1 {
		t.Fatal("CLI exit lost result subscription", n)
	}
	replay := connectHTTP(t, m, "start", req)
	if replay.PlatformID != done.PlatformID || replay.ResultVersion != done.ResultVersion {
		t.Fatal("retry created another connection")
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(calls), `"init", "--new"`) != 1 {
		t.Fatal("app setup repeated")
	}
	// Persisted original source evidence survives Room history removal and restart.
	if err = m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, m.store.dir, nil)
	reopened.mu.Lock()
	restored, err := reopened.readPlatformConnectLocked(req.OperationID)
	reopened.mu.Unlock()
	if err != nil || restored.Status != "completed" || restored.PlatformID != done.PlatformID {
		t.Fatal("durable result lost", err)
	}
}
func TestPlatformConnectNativePartialExpiryAndCancellation(t *testing.T) {
	for _, scenario := range []string{"login-fail", "expired", "saved-block"} {
		t.Run(scenario, func(t *testing.T) {
			m, _, _, req := connectFixture(t)
			bin, dir := roomSetupFixture(t, scenario)
			t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
			connectHTTP(t, m, "start", req)
			if scenario == "saved-block" {
				connectWait(t, m, req, func(op platformConnectOperation) bool { return op.Interaction.URL != "" })
				deadline := time.Now().Add(5 * time.Second)
				for {
					data, _ := os.ReadFile(filepath.Join(dir, "profiles.json"))
					if strings.Contains(string(data), "cli_selected") {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("fixture did not save app")
					}
					time.Sleep(10 * time.Millisecond)
				}
				query := req
				query.Input = nil
				query.SourceSeq = 0
				connectHTTP(t, m, "cancel", query)
				m.mu.Lock()
				s := m.connectSessionLocked(req.OperationID)
				m.mu.Unlock()
				select {
				case <-s.done:
				case <-time.After(5 * time.Second):
					t.Fatal("cancel did not settle process")
				}
			}
			done := connectWait(t, m, req, func(op platformConnectOperation) bool {
				return connectTerminal(op.Status) && (scenario != "saved-block" || op.Application.Status == "applied")
			})
			switch scenario {
			case "login-fail":
				if done.Status != "failed" || done.Application.Status != "applied" || done.Profile == nil {
					t.Fatal("partial setup lost", done)
				}
			case "expired":
				if done.Status != "expired" {
					t.Fatal("expiry misreported", done)
				}
			case "saved-block":
				if done.Status != "cancelled" || done.Application.Status != "applied" {
					t.Fatal("cancel lost applied effect", done)
				}
			}
			file, err := m.platforms.Load()
			if err != nil || len(file.Platforms) != 0 {
				t.Fatal("unsuccessful operation saved connection", err)
			}
			query := req
			query.Input = nil
			query.SourceSeq = 0
			connectHTTP(t, m, "status", query)
			connectHTTP(t, m, "start", req)
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Count(string(calls), `"init", "--new"`) != 1 {
				t.Fatal("observation/replay restarted app setup")
			}
		})
	}
}
