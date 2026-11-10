//go:build e2e && darwin

package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// Reuses a previously accepted real delivery. It never creates a send intent,
// runs a model, starts scheduling, or writes to Lark. Recovery runs in two new
// processes on an isolated copy; the original acceptance evidence is read-only.
func TestRealLarkSelfReadOnlyRecovery(t *testing.T) {
	source := os.Getenv("TEAMX_LARK_SELF_RECOVERY_SOURCE")
	if source == "" {
		t.Skip("set TEAMX_LARK_SELF_RECOVERY_SOURCE to an existing successful self-send rooms root")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "platform_writes": 0, "scope": "previous real self receipt, independent process recovery, native GET verification", "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, output, report)
	paths, err := filepath.Glob(filepath.Join(source, "rooms", "*", "agents", "*", "actions", "records", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record storedAction
	found := 0
	for _, path := range paths {
		var r storedAction
		if err := readSnapshot(path, &r, followupBytes); err != nil {
			t.Fatal(err)
		}
		if r.Action.Request.Operation == platform.OperationMessageSendSelf && r.Action.Status == "succeeded" {
			record = r
			found++
		}
	}
	if found != 1 || record.Action.Result == nil || record.Action.CapabilityBasis == nil || record.Action.Attempts != 1 {
		t.Fatal("source must contain exactly one accepted self delivery")
	}
	id, a := record.MemberIdentity, record.Action
	root := filepath.Join(output, "rooms-root")
	roomDir := filepath.Join("rooms", id.RoomID)
	memberDir := filepath.Join(roomDir, "agents", id.MembershipID)
	files := []string{"rooms.json", "platform-connections.json", filepath.Join(roomDir, "room.json"), filepath.Join(roomDir, "room-config.json"), filepath.Join(roomDir, "tool-bindings.json"), filepath.Join(memberDir, "agent.json"), filepath.Join(memberDir, "followups", "schema.json")}
	// Copy only immutable inputs and the authoritative ledger, excluding model
	// history, credentials, live locks and unrelated acceptance artifacts.
	err = filepath.WalkDir(filepath.Join(source, memberDir, "actions"), func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		b, err := os.ReadFile(filepath.Join(source, file))
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(root, file)
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	q := agentActionRequest{followupCommand: followupCommand{TurnNumber: 99}, ID: a.Request.ID, TargetRef: a.Request.TargetRef, Operation: a.Request.Operation, Input: a.Request.Input}
	epochs := []string{}
	for range 2 {
		p := startSelfSendProcess(t, root, roomSummary{RoomIdentity: id.RoomIdentity})
		epochs = append(epochs, p.info.ServiceEpoch)
		cli := contractCLI(t, p.endpoint, &service{info: p.info}, id)
		r, err := executeSelfSend(cli, q)
		if err != nil || r.Status != "succeeded" || r.Attempts != 1 || r.Result == nil || r.Result.ResultID != a.Result.ResultID {
			t.Fatal("real receipt lost on process restart", err)
		}
		q.Input.Body += "changed"
		if _, err := executeSelfSend(cli, q); err == nil {
			t.Fatal("same ID accepted changed content")
		}
		q.Input = a.Request.Input
		p.stop(t, false)
	}
	if epochs[0] == epochs[1] {
		t.Fatal("recovery reused process epoch")
	}
	m := testManager(t, root, managerTestProfiles(t))
	m.mu.Lock()
	account, _, err := m.resolvePlatformAccountLocked(id.RoomIdentity, a.Access.PlatformID, "lark")
	m.mu.Unlock()
	if err != nil || account.Identity != a.Access.Account || account.CLIProfile == nil {
		t.Fatal("original real account unavailable", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pin, err := lark.Pin(ctx, *account.CLIProfile, account.Identity)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	message, err := pin.MessageReader().Message(ctx, account.Identity, a.Result.ResultID.Value)
	var body struct {
		Text string `json:"text"`
	}
	if err != nil || message.ID != a.Result.ResultID.Value || message.ChatID != a.Target.Containers[0].ID.Value || message.Sender.ID != account.Identity.ID.Value || message.Sender.TenantKey != account.Identity.Tenant.Value || message.Sender.Type != "user" || message.Sender.IDType != "open_id" || message.Type != "text" || message.Deleted || message.ParentID != "" || message.RootID != "" || message.ThreadID != "" || json.Unmarshal([]byte(message.Body.Content), &body) != nil || body.Text != a.Request.Input.Body {
		t.Fatal("native read-only receipt verification failed", err)
	}
	self, err := lark.ResolveSendTarget(ctx, platform.TargetLocator{Self: true}, larkReferenceAccess(id, account, platform.SourceRef{}), pin.SendReader())
	if err != nil || self.Target == nil || !sameNativeTarget(*self.Target, a.Target) {
		t.Fatal("current native self relationship changed", err)
	}
	report["status"], report["service_epochs"], report["action"], report["verified_message"] = "passed", epochs, a, message
}
