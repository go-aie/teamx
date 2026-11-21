//go:build e2e && darwin

package lark

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Recheck a known original message with the corrected native verifier. This
// performs no write and deliberately leaves the historical action ledger intact.
func TestRealLarkImageSendReadback(t *testing.T) {
	path := os.Getenv("TEAMX_LARK_IMAGE_READBACK_INPUT")
	if path == "" {
		t.Skip("explicit known-message readback input required; read-only")
	}
	var q struct {
		Profile   string                   `json:"profile"`
		Account   platform.AccountIdentity `json:"account"`
		MessageID string                   `json:"message_id"`
		ChatID    string                   `json:"chat_id"`
		Body      string                   `json:"body"`
		Images    []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
			Key    string `json:"key"`
		} `json:"images"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &q) != nil || q.Profile == "" || !q.Account.Valid() || !nativeID(q.MessageID, "om_") || !nativeID(q.ChatID, "oc_") || len(q.Images) < 1 || len(q.Images) > 4 {
		t.Fatal("known original message/account/source input required")
	}
	images, keys := []SendImage{}, []string{}
	for _, source := range q.Images {
		data, err := os.ReadFile(source.Path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != source.SHA256 || !imageKeyPattern.MatchString(source.Key) {
			t.Fatal("original image bytes/upload key mismatch")
		}
		images = append(images, SendImage{Data: data, SHA256: source.SHA256})
		keys = append(keys, source.Key)
	}
	binary, err := exec.LookPath("lark-cli")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	pin, err := Pin(ctx, platform.CLIProfile{Version: 1, Executable: binary, Profile: q.Profile, Brand: "feishu", CLIVersion: CLIVersion}, q.Account)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	defer pin.Close()
	m, err := pin.MessageReader().Message(ctx, q.Account, q.MessageID)
	if err != nil || m.ID != q.MessageID || m.ChatID != q.ChatID || m.Deleted || m.ParentID != "" || m.RootID != "" || m.ThreadID != "" || m.Sender.Type != "user" || m.Sender.IDType != "open_id" || m.Sender.ID != q.Account.ID.Value || m.Sender.TenantKey != q.Account.Tenant.Value || !verifyImageContent(m.Type, m.Body.Content, q.Body, keys, len(images), images...) {
		t.Fatal("independent corrected native readback failed", ErrorCode(err))
	}
	evidence, _ := json.MarshalIndent(map[string]any{"status": "passed", "platform_writes": 0, "original_action_ledger_modified": false, "message_id": m.ID, "message": m}, "", "  ")
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "native-readback-evidence.json"), append(evidence, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("known native message verified with original image bytes; platform writes=0; historical ledger unchanged")
}

// Native reads only. Feed the six actual sent messages through production
// source classification; never enable or alter a daily notification subscription.
func TestRealLarkImageSendBackflow(t *testing.T) {
	path := os.Getenv("TEAMX_LARK_IMAGE_SEND_REPORT")
	if path == "" {
		t.Skip("explicit successful real image-send report required; read-only")
	}
	var report struct {
		Status  string `json:"status"`
		Profile string `json:"profile"`
		Samples []struct {
			Mode    string                   `json:"mode"`
			Status  string                   `json:"status"`
			Account platform.AccountIdentity `json:"account"`
			Actions []struct {
				Status string `json:"status"`
				Result struct {
					ResultID platform.ExternalID `json:"result_id"`
				} `json:"result"`
			} `json:"actions"`
		} `json:"samples"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &report) != nil || report.Status != "passed" || report.Profile == "" || len(report.Samples) != 2 {
		t.Fatal("successful six-message real report required")
	}
	binary, err := exec.LookPath("lark-cli")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	seen := map[string]bool{}
	for _, sample := range report.Samples {
		if sample.Status != "passed" || len(sample.Actions) != 3 || sample.Mode != "managed-self" && sample.Mode != "default-assistant-group" {
			t.Fatal("incomplete target evidence")
		}
		pin, err := Pin(ctx, platform.CLIProfile{Version: 1, Executable: binary, Profile: report.Profile, Brand: "feishu", CLIVersion: CLIVersion}, sample.Account)
		if err != nil {
			t.Fatal(ErrorCode(err))
		}
		defer pin.Close()
		for _, action := range sample.Actions {
			mid := action.Result.ResultID.Value
			if action.Status != "succeeded" || !action.Result.ResultID.Valid() || seen[mid] {
				t.Fatal("missing or duplicate remote message")
			}
			seen[mid] = true
			message, err := pin.MessageReader().Message(ctx, sample.Account, mid)
			if err != nil || message.ID != mid || message.Sender.ID != sample.Account.ID.Value || message.Sender.Type != "user" || message.Type != "image" && message.Type != "post" || len(message.ImageKeys()) == 0 {
				t.Fatal("real user image reread failed", ErrorCode(err))
			}
			chat, err := pin.client.NotificationChat(ctx, sample.Account, message.ChatID)
			if err != nil || !chat.Relation.ValidFor(sample.Account, chat.ID) {
				t.Fatal("native relationship check failed", ErrorCode(err))
			}
			created, err := strconv.ParseInt(message.Created, 10, 64)
			if err != nil || created <= 0 {
				t.Fatal("invalid native creation time")
			}
			binding := Binding{PlatformID: "realimage", Generation: "realimage", Account: sample.Account, CredentialVersion: 1, Fingerprint: strings.Repeat("a", 64)}
			cfg := Config{Enabled: true, PlatformID: binding.PlatformID, Private: PrivateConfig{Scope: "specified"}, Groups: GroupConfig{Scope: "specified", MessageMode: "all"}, Target: notification.Target{Mode: "inbox", AgentID: "assistant", MembershipID: "member"}, IntervalSeconds: 60}
			if sample.Mode == "managed-self" {
				cfg.Private.Enabled, cfg.Private.ChatIDs = true, []string{chat.ID}
			} else {
				cfg.Groups.Enabled, cfg.Groups.ChatIDs = true, []string{chat.ID}
			}
			state, err := Configure(platform.RoomIdentity{RoomID: "realimage", RoomInstance: "realimage"}, nil, binding, cfg, time.UnixMilli(created-1))
			if err != nil {
				t.Fatal(err)
			}
			state.acceptMessage(message, ChatProgress{Chat: chat})
			if sample.Mode == "managed-self" {
				if chat.Relation.Kind != "self" || len(state.Inbox) != 0 {
					t.Fatal("self image reentered automatic Inbox")
				}
			} else {
				if chat.Relation.Kind != "group" || len(state.Inbox) != 1 {
					t.Fatal("group image context missing")
				}
				for _, entry := range state.Inbox {
					view := NotificationCapabilities().Project(entry.Item)
					if entry.Item.Content == nil || !entry.Item.Content.ContextOnly || !view.ContextOnly {
						t.Fatal("own group image became actionable input")
					}
				}
			}
		}
	}
	t.Log("six real native messages: self excluded; own group images context_only; platform writes=0")
}
