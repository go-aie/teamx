//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/testutil"
)

// The input copies only one saved notification and a CLI profile reference, never
// OAuth credentials. All Room, Inbox and connection writes use a disposable store.
func TestRealLarkImageMaterials(t *testing.T) {
	path := os.Getenv("TEAMX_LARK_IMAGE_INPUT")
	if path == "" {
		t.Skip("explicit private saved notification input required")
	}
	var input struct {
		Profile platform.CLIProfile `json:"profile"`
		Item    notification.Item   `json:"item"`
		Model   dsh.ModelConfig     `json:"model"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &input) != nil || len(findNotificationImages(input.Item.Text, input.Item.Source).Images) != 1 {
		t.Fatal("invalid saved image input")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "remote_writes": 0, "live_room_modified": false, "model_requested": os.Getenv("TEAMX_LARK_IMAGE_MODEL") == "1", "browser": "not exercised", "scheduler": "seeded fixed turn only"}
	defer imageRealReport(t, output, report)
	h := newLarkHarness(t)
	h.m.larkClients = nil // Real acceptance retains process-wide production pacing.
	c := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "isolated image acceptance", Provider: "lark", URL: lark.Origin(input.Profile.Brand), CLIProfile: &input.Profile})
	c, err = h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal("real profile verification failed")
	}
	h.c = c
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	s := h.configure(realLarkScopeConfig(t, c, []string{input.Item.Source.Reference.ChatID.Value}, inboxTarget(id)))
	item := input.Item
	item.Source.SubscriptionID = s.SubscriptionID
	item.Route = notification.Route{Target: inboxTarget(id), Version: s.SubscriptionVersion}
	if err := h.svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	beforeInbox, _ := h.svc.memberInboxStatus(context.Background(), id)
	beforeRoom, _ := h.svc.store.LoadRoom()
	imagePath := findNotificationImages(item.Text, item.Source).Images[0].Path
	query := url.Values{"room_id": {id.RoomID}, "room_instance": {id.RoomInstance}, "service_epoch": {h.svc.info.ServiceEpoch}, "caller_id": {"operator"}, "agent_id": {id.AgentID}, "membership_id": {id.MembershipID}, "seq": {"1"}, "path": {imagePath}}
	w := attachmentHTTP(h.m, query)
	report["preview_http_status"], report["mime"], report["bytes"] = w.Code, w.Header().Get("Content-Type"), w.Body.Len()
	if w.Code != 200 {
		t.Fatal("real resource preview failed", w.Code)
	}
	data := append([]byte(nil), w.Body.Bytes()...)
	report["sha256"] = fmt.Sprintf("%x", sha256.Sum256(data))
	if err := os.WriteFile(filepath.Join(output, "image.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	saveMaterialTurn(t, h.svc, id, 0, 1)
	turnInput := materialInput(id, 0, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	materials, err := h.svc.prepareDSHMaterials(ctx, turnInput, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	blocks := materials.blocks("acceptance")
	if materialImageCount(blocks) != 1 || !bytes.Equal(blocks[4].Data, data) {
		t.Fatal("real image not supplied to model materials")
	}
	if err := materials.check(ctx); err != nil {
		t.Fatal(err)
	}
	report["download_and_preparation"] = "passed"
	if os.Getenv("TEAMX_LARK_IMAGE_BROWSER") == "1" {
		server := httptest.NewServer(h.m)
		defer server.Close()
		ready, _ := json.Marshal(map[string]any{"page": server.URL, "inbox_image": server.URL + "/v1/platform/attachments?" + query.Encode()})
		if err := os.WriteFile(filepath.Join(output, "browser-ready.json"), ready, 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("browser ready:", filepath.Join(output, "browser-ready.json"))
		deadline := time.NewTimer(2 * time.Minute)
		defer deadline.Stop()
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		waiting := true
		for waiting {
			select {
			case <-deadline.C:
				t.Fatal("browser not acknowledged; no visual acceptance")
			case <-tick.C:
				if _, err := os.Stat(filepath.Join(output, "browser-done")); err == nil {
					waiting = false
				}
			}
		}
		report["browser"] = "driver acknowledged; inspect screenshot"
	}
	if os.Getenv("TEAMX_LARK_IMAGE_MODEL") == "1" {
		key := input.Model.APIKeyEnv
		if key == "" {
			t.Fatal("model credential environment name required")
		}
		imageRealCredential(t, key)
		install, err := testutil.InstalledDSH(ctx)
		if err != nil {
			t.Fatal(err)
		}
		profile := dshProfile{BinaryPath: install.BinaryPath, BinaryArgs: install.BinaryArgs, Workspace: t.TempDir(), Model: input.Model, Instructions: `This is a read-only image acceptance turn. Do not use tools, delegate, read files or send messages. Inspect only supplied material. Return a concise JSON object with visible_error, location, and coverage. Transcribe the visible error text from the image. Never infer missing image content. Ordinary output is the private test result.`}
		backend := dshBackend{store: h.svc.store, prepare: h.svc.prepareDSHMaterials, profile: profile, turnTimeout: 2 * time.Minute}
		backend.open = func(c context.Context, opts dsh.Options) (dshComponent, error) {
			opts.StateDir = filepath.Join(output, "dsh")
			session, err := dsh.Open(c, opts)
			if session == nil {
				return nil, err
			}
			report["promptCapabilities.image"] = session.SupportsImages()
			return &imageRealComponent{Session: session, row: report}, err
		}
		session, err := backend.OpenSession(ctx, adapterOptions(h.svc.store, id))
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close(context.Background())
		if err := session.Run(ctx, turnInput); err != nil {
			t.Fatal(err)
		}
		result := report["result"].(dsh.TurnResult)
		if len(result.Tools) != 0 {
			t.Fatal("model used tools")
		}
		text := result.Text
		if err := os.WriteFile(filepath.Join(output, "model-result.txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		report["model_returned"] = text != ""
		if text == "" {
			t.Fatal("empty model result")
		}
		var expected []string
		if json.Unmarshal([]byte(os.Getenv("TEAMX_LARK_IMAGE_EXPECT")), &expected) == nil && len(expected) > 0 {
			for _, value := range expected {
				if !strings.Contains(text, value) {
					t.Fatal("pixel-only expected text missing")
				}
			}
			report["pixel_text_check"] = "passed"
		} else {
			report["pixel_text_check"] = "manual comparison required"
		}
	}
	afterInbox, _ := h.svc.memberInboxStatus(context.Background(), id)
	afterRoom, _ := h.svc.store.LoadRoom()
	if !reflect.DeepEqual(beforeInbox, afterInbox) || !reflect.DeepEqual(beforeRoom, afterRoom) {
		t.Fatal("business protocol state changed")
	}
	report["protocol_state_unchanged"] = true
	report["status"] = "passed"
}
