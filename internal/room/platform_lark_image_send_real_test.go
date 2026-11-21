//go:build e2e && darwin

package room

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type larkImageSendRealInput struct {
	Profile        string `json:"profile"`
	ExpectedUserID string `json:"expected_user_id"`
	SelfChatID     string `json:"self_chat_id"`
	GroupID        string `json:"group_id"`
	GroupName      string `json:"group_name"`
	Caption        string `json:"caption"`
	Images         []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"images"`
	DispatchDir string `json:"dispatch_dir"`
}

func larkImageSendRealSample(t *testing.T) (larkImageSendRealInput, [][]byte) {
	t.Helper()
	path := os.Getenv("TEAMX_LARK_IMAGE_SEND_INPUT")
	var q larkImageSendRealInput
	raw, err := os.ReadFile(path)
	if err != nil || strictToolJSON(raw, &q) != nil || q.Profile == "" || !strings.HasPrefix(q.ExpectedUserID, "ou_") || !strings.HasPrefix(q.SelfChatID, "oc_") || !strings.HasPrefix(q.GroupID, "oc_") || q.SelfChatID == q.GroupID || q.GroupName == "" || !handlingText(q.Caption) || len(q.Caption) > 4096 || len(q.Images) != 2 || !filepath.IsAbs(q.DispatchDir) {
		t.Fatal("exact reviewed account, self/group targets, caption, two images and persistent dispatch directory required")
	}
	images := make([][]byte, 2)
	for i, image := range q.Images {
		if filepath.IsAbs(image.Path) || !filepath.IsLocal(image.Path) {
			t.Fatal("sample paths must be local to the reviewed manifest")
		}
		images[i], err = os.ReadFile(filepath.Join(filepath.Dir(path), image.Path))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(images[i])) != image.SHA256 || len(images[i]) > 2<<20 || len(images[i]) < 8 || string(images[i][:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatal("reviewed original PNG unavailable or changed", i+1)
		}
	}
	if q.Images[0].SHA256 == q.Images[1].SHA256 || len(images[0])+len(images[1]) > 4<<20 {
		t.Fatal("two distinct bounded originals required")
	}
	return q, images
}

// This check never uploads, sends, logs in or changes the original connection.
func TestRealLarkImageSendPreflight(t *testing.T) {
	if os.Getenv("TEAMX_LARK_IMAGE_SEND_PREFLIGHT") != "1" {
		t.Skip("explicit read-only native preflight")
	}
	q, _ := larkImageSendRealSample(t)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pin := larkImageSendRealPreflight(t, ctx, q)
	defer pin.Close()
	if err := pin.ImageUploadReady(ctx); err != nil {
		t.Fatal("image upload is not ready; no write attempted:", lark.ErrorCode(err))
	}
	t.Log("exact user/self/group and image.upload ready; uploads=0 messages=0")
}

func larkImageSendRealPreflight(t *testing.T, ctx context.Context, q larkImageSendRealInput) *lark.Pinned {
	t.Helper()
	binary, err := exec.LookPath("lark-cli")
	if err != nil {
		t.Fatal(err)
	}
	p := platform.CLIProfile{Version: 1, Executable: binary, Profile: q.Profile, Brand: "feishu", CLIVersion: lark.CLIVersion}
	client, err := lark.New(p)
	if err != nil {
		t.Fatal(lark.ErrorCode(err))
	}
	v, err := client.Verify(ctx)
	if err != nil || v.Account.ID.Value != q.ExpectedUserID {
		t.Fatal("reviewed user mismatch", lark.ErrorCode(err))
	}
	pin, err := lark.Pin(ctx, p, v.Account)
	if err != nil {
		t.Fatal(lark.ErrorCode(err))
	}
	t.Cleanup(pin.Close)
	self, err := pin.SendReader().(interface {
		SelfChat(context.Context, platform.AccountIdentity) (lark.Chat, error)
	}).SelfChat(ctx, v.Account)
	if err != nil || self.ID != q.SelfChatID || self.Relation.Kind != "self" || !self.Relation.ValidFor(v.Account, self.ID) {
		t.Fatal("reviewed self relationship mismatch", lark.ErrorCode(err))
	}
	group, err := pin.SendReader().SendGroup(ctx, v.Account, q.GroupID)
	if err != nil || group.Name != q.GroupName {
		t.Fatal("reviewed supported group mismatch", lark.ErrorCode(err))
	}
	permission, err := pin.SendPermission(ctx)
	if err != nil || !permission.SelfSendEnabled || !permission.GroupSendEnabled {
		t.Fatal("target sending is not ready", lark.ErrorCode(err))
	}
	return pin
}

type larkImageRealCase struct {
	ID     string              `json:"id"`
	Key    string              `json:"key"`
	Input  lark.ImageSendInput `json:"input"`
	Hashes []string            `json:"hashes"`
}

type larkImageRealSingleProof struct {
	Case   larkImageRealCase
	Action referenceAction
	Audit  *policyRealAudit
}

// Six authorized messages, ten uploads: self from a managed Room and one group
// from the default assistant, each single image / two images / caption+two images.
// Native writes use the same managed implementation as fixture acceptance. The
// wrapper forwards real responses and permanently bounds each native invocation.
func TestRealLarkImageDSHSendAndRecovery(t *testing.T) {
	if os.Getenv("TEAMX_LARK_IMAGE_SEND_REAL") != "1" {
		t.Skip("explicit six-message/ten-upload authorization and reviewed manifest required")
	}
	q, images := larkImageSendRealSample(t)
	operator := Caller{Author: Author{Kind: "human", ID: humanOperator()}}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	defer cancel()
	independent := larkImageSendRealPreflight(t, ctx, q)
	defer independent.Close()
	if err := independent.ImageUploadReady(ctx); err != nil {
		t.Fatal("image upload is not ready; no write attempted:", lark.ErrorCode(err))
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	out := os.Getenv("TEAMX_DSH_OUTPUT")
	resume := os.Getenv("TEAMX_LARK_IMAGE_SEND_RESUME") == "1"
	retryNotApplied := os.Getenv("TEAMX_LARK_IMAGE_SEND_RETRY_NOT_APPLIED") == "1"
	reusePath := os.Getenv("TEAMX_LARK_IMAGE_SEND_REUSE_SINGLE")
	var reused *larkImageRealSingleProof
	if reusePath != "" {
		if os.Getenv("TEAMX_LARK_IMAGE_SEND_EXTRA_APPROVED") != "1" || !filepath.IsAbs(reusePath) || filepath.Dir(reusePath) == out {
			t.Fatal("explicit extra-message approval and a distinct original single-image report required")
		}
		var old struct {
			Profile string `json:"profile"`
			Samples []struct {
				Mode    string              `json:"mode"`
				Cases   []larkImageRealCase `json:"cases"`
				Actions []referenceAction   `json:"actions"`
				Audit   *policyRealAudit    `json:"audit"`
			} `json:"samples"`
		}
		raw, err := os.ReadFile(reusePath)
		if err != nil || json.Unmarshal(raw, &old) != nil || old.Profile != q.Profile {
			t.Fatal("original single-image evidence unavailable")
		}
		for _, sample := range old.Samples {
			if sample.Mode != "managed-self" {
				continue
			}
			for _, action := range sample.Actions {
				if action.Request.ID != "real-images-managed-self-single" || action.Status != "succeeded" || action.Result == nil || action.Attempts != 1 || action.Access.Account != independent.Account {
					continue
				}
				for _, original := range sample.Cases {
					if original.ID == action.Request.ID && original.Key == action.IdempotencyKey && original.Input.Body == "" && reflect.DeepEqual(original.Hashes, []string{q.Images[0].SHA256}) {
						verifyRealImageMessage(t, ctx, independent, q, original, action)
						reused = &larkImageRealSingleProof{Case: original, Action: action, Audit: sample.Audit}
					}
				}
			}
		}
		if reused == nil {
			t.Fatal("only a verified original single-image receipt can be reused; unknown receipts are never replayed")
		}
	}
	previousAudits := map[string]*policyRealAudit{}
	previousRows := map[string]map[string]any{}
	openAttempt := fmt.Sprintf("%d", time.Now().UnixNano())
	if resume {
		if !filepath.IsAbs(out) {
			t.Fatal("exact original absolute evidence directory required for resume")
		}
		raw, err := os.ReadFile(filepath.Join(out, "report.json"))
		var previous struct {
			Status  string `json:"status"`
			Profile string `json:"profile"`
			Samples []struct {
				Mode  string           `json:"mode"`
				Audit *policyRealAudit `json:"audit"`
			} `json:"samples"`
		}
		if err != nil || json.Unmarshal(raw, &previous) != nil || previous.Profile != q.Profile || previous.Status != "failed" {
			t.Fatal("original failed local acceptance report required; no new dispatch")
		}
		for _, sample := range previous.Samples {
			if sample.Audit != nil {
				previousAudits[sample.Mode] = sample.Audit
			}
		}
		var savedReport struct {
			Samples []map[string]any `json:"samples"`
		}
		if json.Unmarshal(raw, &savedReport) != nil {
			t.Fatal("invalid original sample report")
		}
		for _, sample := range savedReport.Samples {
			if name, ok := sample["mode"].(string); ok {
				previousRows[name] = sample
			}
		}
		backup := filepath.Join(out, fmt.Sprintf("report-before-resume-%d.json", time.Now().UnixNano()))
		if err := os.WriteFile(backup, raw, 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		out = imageRealOutput(t)
	}
	report := map[string]any{"status": "failed", "platform": "real Feishu user", "maximum_messages": 6, "maximum_uploads": 10, "model": entry.materialize().ModelID, "recorded_at": time.Now().UTC(), "profile": q.Profile}
	if reused != nil {
		report["maximum_messages"], report["maximum_uploads"] = 7, 12
		report["reused_single_report"], report["historical_unknown_preserved"] = reusePath, true
	}
	defer imageRealReport(t, out, report)
	// Reserve immediately before the first model task, after local preparation.
	// Never remove a reservation containing native upload/send records.
	if _, err := os.Stat(q.DispatchDir); !resume && reused == nil && !os.IsNotExist(err) || (resume || reused != nil) && err != nil {
		t.Fatal("dispatch directory already exists or is unavailable; inspect original receipts before any further write")
	}
	dispatchReserved := resume || reused != nil
	const credentialKey = "TEAMX_LARK_IMAGE_SEND_MODEL_KEY"
	t.Setenv(credentialKey, credential.Value)
	runtime := *cfg.Runtime
	runtime.Model = entry.materialize()
	runtime.Model.APIKeyEnv = credentialKey
	runtime.Model.InputModalities = []string{"text"}
	binary := filepath.Join(out, "teamx")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	native, err := exec.LookPath("lark-cli")
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{}
	defer func() { report["samples"] = rows }()
	for _, mode := range []string{"managed-self", "default-assistant-group"} {
		if saved := previousRows[mode]; saved != nil && saved["status"] == "passed" {
			var proof struct {
				Cases   []larkImageRealCase `json:"cases"`
				Actions []referenceAction   `json:"actions"`
			}
			raw, _ := json.Marshal(saved)
			if json.Unmarshal(raw, &proof) != nil || len(proof.Cases) != 3 || len(proof.Actions) != 3 {
				t.Fatal("completed sample is incomplete")
			}
			for i, action := range proof.Actions {
				if action.Status != "succeeded" || action.Access.Account != independent.Account || action.Result == nil {
					t.Fatal("completed sample cannot reuse an unknown receipt")
				}
				verifyRealImageMessage(t, ctx, independent, q, proof.Cases[i], action)
			}
			rows = append(rows, saved)
			t.Log("reused completed native/Agent/restart evidence without any write:", mode)
			continue
		}
		// A failed/unknown sample ends the entire real-write run.
		if !t.Run(mode, func(t *testing.T) {
			row := map[string]any{"mode": mode, "status": "failed"}
			rows = append(rows, row)
			audit := &policyRealAudit{Calls: map[string]int{}}
			if previous := previousAudits[mode]; previous != nil {
				row["prior_failed_audit"] = previous
			}
			m := directTestManager(t, filepath.Join(out, mode), &runtime)
			m.maxTurns, m.config.unifiedInputs = 8, true
			server := browserTestServer(t, m, audit.serve(m))
			defer server.Close()
			m.config.endpoint, m.config.cliPath = server.URL, binary
			m.factory = func(dir, id string, config serviceConfig) (*service, error) {
				config.dshTurnTimeout = 3 * time.Minute
				return startService(dir, id, config, func(s *service, state RoomSnapshot) error {
					observePolicyReal(s, audit)
					if err := s.loadMembers(state); err != nil {
						return err
					}
					return s.attachMembers(state)
				})
			}
			var room roomSummary
			operation, receiveID, receiveType := platform.OperationMessageSendSelf, q.ExpectedUserID, "open_id"
			if mode == "default-assistant-group" {
				ensureAssistant(t, m)
				room = assistantSummary(t, m)
				operation, receiveID, receiveType = platform.OperationMessageSend, q.GroupID, "chat_id"
			} else {
				team := testTeamDefinition(t)
				team.Runtime, team.Members[0].Instructions = &runtime, assistantInstructions
				m.teams = map[string]teamDefinition{"sample": team}
				room = createTeamRoom(t, m, "real-image-send")
			}
			svc := eventLiveService(t, &eventHarness{m: m, room: room})
			m.bindPlatformReferences(svc)
			_, snapshot := readManagedRoom(t, m, room)
			member := snapshot.Members[0]
			id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			priorAgent, err := svc.store.LoadAgent(id)
			if errors.Is(err, os.ErrNotExist) {
				priorAgent = AgentSnapshot{Version: formatVersion, MemberIdentity: id}
				err = svc.store.SaveAgent(priorAgent)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.store.initReferenceFollowups(id); err != nil {
				t.Fatal(err)
			}
			priorActions, err := svc.store.queryActions(id, "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			failedCount := 0
			for _, action := range priorActions {
				if retryNotApplied && mode == "default-assistant-group" && action.Request.ID == "real-images-default-assistant-group-caption" && action.Status == "not_applied" && action.Result != nil && action.Result.ReasonCode == "image_upload_1_failed" {
					calls, _ := filepath.Glob(filepath.Join(q.DispatchDir, action.IdempotencyKey+".*"))
					if len(calls) != 0 {
						t.Fatal("do not create another attempt while any native call record exists")
					}
					failedCount++
					row["prior_not_applied_action"] = action
					continue
				}
				if !resume || action.Status != "succeeded" || action.Result == nil || action.Attempts != 1 {
					t.Fatal("resume only permits already verified successful messages; unknown/not_applied require inspection")
				}
			}
			// This preparation service deliberately has no running member. Save
			// disabled scheduling before reopening through the real DSH factory.
			if err := svc.store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: false, Used: priorAgent.TurnNumber}); err != nil {
				t.Fatal(err)
			}
			refs := []lark.ImageRef{}
			for i, data := range images {
				imageID := attachmentTestID(i + 1)
				status, body := attachmentUploadHTTP(t, server, svc.info, imageID, data, false)
				if status != 200 {
					t.Fatal(status, string(body))
				}
				status, body = attachmentJSON(t, server, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: operator, ClientMessageID: imageID, Attachments: []RoomAttachmentInput{{ID: imageID}}})
				var posted PostReceipt
				if status != 200 || json.Unmarshal(body, &posted) != nil {
					t.Fatal("original Room image publication failed", status)
				}
				refs = append(refs, lark.ImageRef{MessageSeq: posted.Seq, AttachmentID: imageID})
			}
			cases := []larkImageRealCase{}
			for i, name := range []string{"single", "multiple", "caption"} {
				input := lark.ImageSendInput{Images: refs}
				hashes := []string{q.Images[0].SHA256, q.Images[1].SHA256}
				if i == 0 {
					input.Images, hashes = refs[:1], hashes[:1]
				}
				if i == 2 {
					input.Body = q.Caption
				}
				actionID := "real-images-" + mode + "-" + name
				if retryNotApplied && mode == "default-assistant-group" && name == "caption" {
					actionID += "-corrected"
				}
				cases = append(cases, larkImageRealCase{ID: actionID, Key: referenceDigest("", id, actionID)[:32], Input: input, Hashes: hashes})
			}
			allowedCases := cases
			if mode == "managed-self" && reused != nil {
				cases[0] = reused.Case
				allowedCases = cases[1:]
				row["reused_single_audit"] = reused.Audit
			}
			wrapperConfig := map[string]any{"binary": native, "profile": q.Profile, "user_id": q.ExpectedUserID, "self_chat_id": q.SelfChatID, "group_id": q.GroupID, "group_name": q.GroupName, "receive_id": receiveID, "receive_id_type": receiveType, "operation": operation, "dispatch_dir": q.DispatchDir, "ledger": svc.store.actionRoot(id), "cases": allowedCases}
			configPath := filepath.Join(out, mode+"-native.json")
			if old, err := os.ReadFile(configPath); err == nil {
				var before, after any
				next, _ := json.Marshal(wrapperConfig)
				if json.Unmarshal(old, &before) != nil || json.Unmarshal(next, &after) != nil {
					t.Fatal("invalid original native configuration")
				}
				if retryNotApplied && mode == "default-assistant-group" && failedCount == 1 && !reflect.DeepEqual(before, after) {
					oldCases := before.(map[string]any)["cases"].([]any)
					newCases := after.(map[string]any)["cases"].([]any)
					if len(oldCases) != 3 || len(newCases) != 3 {
						t.Fatal("retry must preserve the complete original matrix")
					}
					last := oldCases[2].(map[string]any)
					replacement := newCases[2].(map[string]any)
					if last["id"] != "real-images-default-assistant-group-caption" || replacement["id"] != "real-images-default-assistant-group-caption-corrected" {
						t.Fatal("retry can replace only the proven not_applied caption intent")
					}
					last["id"], last["key"] = replacement["id"], replacement["key"]
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("resume changed original account, sources, target or stable action IDs")
				}
			}
			writeRealImageJSON(t, configPath, wrapperConfig)
			script, err := os.ReadFile("testdata/lark_image_send_real.py")
			if err != nil {
				t.Fatal(err)
			}
			quoted, _ := json.Marshal(configPath)
			wrapper := filepath.Join(out, mode+"-lark-cli")
			if err = os.WriteFile(wrapper, []byte(strings.Replace(string(script), `"__CONFIG_PATH__"`, string(quoted), 1)), 0700); err != nil {
				t.Fatal(err)
			}
			p := platform.CLIProfile{Version: 1, Executable: wrapper, Profile: q.Profile, Brand: "feishu", CLIVersion: lark.CLIVersion}
			var c managedPlatform
			file, err := m.platforms.Load()
			if err != nil {
				t.Fatal(err)
			}
			for _, saved := range file.Platforms {
				if saved.ID == strings.Repeat("e", 32) {
					c = managedPlatform{Platform: saved.Platform}
				}
			}
			if c.ID == "" {
				c = platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: operator, ID: strings.Repeat("e", 32), Name: "isolated real image acceptance", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &p})
			}
			c, err = m.verifyPlatform(ctx, managedPlatformRequest{ID: c.ID, Version: c.Version})
			if err != nil || c.Validation.Status != "valid" || c.Validation.Account != independent.Account {
				t.Fatal("fixed account verification failed", err)
			}
			objectType := "user"
			if operation == platform.OperationMessageSend {
				objectType = "chat"
			}
			m.mu.Lock()
			bindings, err := m.readToolBindingsLocked(id.RoomIdentity)
			m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if len(bindings.Bindings) == 0 {
				bound := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: id.RoomIdentity, Caller: operator, ExpectedBindingRevision: new(uint64(0)), Enabled: true, PlatformVersion: c.Version, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", PlatformID: c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: string(operation), TargetKind: "object", ObjectType: objectType}}}}})
				if bound.Code != 200 {
					t.Fatal("exact isolated member grant failed", bound.Code, bound.Body.String())
				}
			}
			closeService(t, svc)
			m.mu.Lock()
			delete(m.runtimes, room.RoomIdentity)
			m.mu.Unlock()
			svc = openRoomOK(t, m, room, "real-images-open-"+openAttempt)
			if resume && len(priorActions) > 0 {
				status, err := svc.target(id.MembershipID).Call[MemberStatus](ctx, InspectMember{id})
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(status.Error, "configuration requires settled Session rotation") {
					opID, err := newOpaqueID()
					if err != nil {
						t.Fatal(err)
					}
					status, err = svc.rotateSession(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, OperationID: opID, FromGeneration: status.SessionGeneration})
					if err != nil || status.Rotation == nil || status.Rotation.Status != "completed" {
						t.Fatal("isolated settled Session rotation failed", err)
					}
					row["isolated_resume_rotation"] = status.Rotation
				}
			}
			if !dispatchReserved {
				if err := os.Mkdir(q.DispatchDir, 0700); err != nil {
					t.Fatal("exclusive native dispatch reservation unavailable", err)
				}
				dispatchReserved = true
			}
			row["cases"], row["account"] = cases, independent.Account
			defer func() {
				audit.mu.Lock()
				row["audit"] = audit
				audit.mu.Unlock()
			}()
			agent, err := svc.store.LoadAgent(id)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			turn := agent.TurnNumber
			started := false
			run := func(name, text string) {
				t.Helper()
				turn++
				posted, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: operator, ClientMessageID: name, Text: text})
				if err != nil {
					t.Fatal(err)
				}
				if !started {
					if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
						t.Fatal(err)
					}
					started = true
				}
				status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
					return !s.Active && (s.Agent.TurnNumber >= turn || s.Paused && s.Error != "")
				})
				if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
					t.Fatal("real Agent turn failed; do not repeat sends", status.Error)
				}
				snapshot, err := svc.store.LoadRoom()
				if err != nil {
					t.Fatal(err)
				}
				for _, message := range snapshot.Messages {
					if message.Seq > posted.Seq && message.Author.Kind == "agent" && message.Author.ID == id.AgentID && strings.TrimSpace(message.Text) != "" {
						return
					}
				}
				t.Fatal("Agent did not report the result to this Room")
			}
			records := []referenceAction{}
			for i, sample := range cases {
				if mode == "managed-self" && reused != nil && i == 0 {
					row[sample.ID] = verifyRealImageMessage(t, ctx, independent, q, sample, reused.Action)
					records = append(records, reused.Action)
					row["actions"] = records
					continue
				}
				saved, err := svc.store.queryActions(id, sample.ID, "", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(saved) == 1 {
					action := saved[0]
					if action.Status != "succeeded" {
						t.Fatal("never resume an unknown message by sending again")
					}
					row[sample.ID] = verifyRealImageMessage(t, ctx, independent, q, sample, action)
					replayRealImageAction(t, svc, id, action, turn)
					records = append(records, action)
					row["actions"] = records
					continue
				}
				selection := "最早的第一张原图，不带说明"
				if i == 1 {
					selection = "用 History 核对最早的两张原图，按原顺序合为一条，不带说明"
				} else if i == 2 {
					selection = "用 History 核对最早两张原图，按原顺序与消息末尾的准确说明合为一条"
				}
				destination := "我的飞书本人会话"
				if operation == platform.OperationMessageSend {
					destination = fmt.Sprintf("飞书群「%s」（准确 chat_id：%s）", q.GroupName, q.GroupID)
				}
				task := fmt.Sprintf("把%s发送到%s。使用当前已绑定的我的用户账号，只发一条；动作 ID 固定为 %s。已有能力直接复用。这个隔离任务只使用 TeamX inbox/history 和 TeamX CLI；不读取本地文件、环境或凭据，不调用原生平台 CLI、网络、MCP resources 或其他发现工具。任何错误或结果不确定就停止，不重试、不拆图、不替换账号或内容。结果发布到本 Room。", selection, destination, sample.ID)
				if i == 2 {
					task += "\n\n准确说明如下，保留空行和字面 Markdown；代码围栏不属于正文：\n```text\n" + q.Caption + "\n```"
				}
				run(sample.ID, task)
				actions, err := svc.store.queryActions(id, "", "", "")
				expectedActions := i + 1 + failedCount
				if mode == "managed-self" && reused != nil {
					expectedActions--
				}
				if err != nil || len(actions) != expectedActions {
					t.Fatal("Agent split/replaced the complete action", err)
				}
				saved, err = svc.store.queryActions(id, sample.ID, "", "")
				if err != nil || len(saved) != 1 {
					t.Fatal("exact receipt missing", err)
				}
				action := saved[0]
				var input lark.ImageSendInput
				if action.Status != "succeeded" || action.Attempts != 1 || action.Result == nil || json.Unmarshal(actionInputJSON(action.Request.Input), &input) != nil || !reflect.DeepEqual(input, sample.Input) || action.Access.Account != independent.Account || action.Inspection.ContentDigest == "" {
					t.Fatal("whole message not verified; preserve original receipt and stop", action.Status)
				}
				if operation == platform.OperationMessageSendSelf && (action.CapabilityBasis == nil || action.Basis != nil || len(action.Target.Containers) != 1 || action.Target.Containers[0].ID.Value != q.SelfChatID) || operation == platform.OperationMessageSend && (action.Basis == nil || action.CapabilityBasis != nil || action.Target.Object.ID.Value != q.GroupID) {
					t.Fatal("target or authority mismatch")
				}
				message := verifyRealImageMessage(t, ctx, independent, q, sample, action)
				row[sample.ID] = message
				records = append(records, action)
				row["actions"] = records
				// Same ID returns the old receipt; it must never reach the wrapper.
				replayRealImageAction(t, svc, id, action, turn)
			}
			if resume && mode == "managed-self" && len(priorActions) == 2 && reused != nil {
				audit.mu.Lock()
				before := audit.Calls["/v1/actions/execute"]
				audit.mu.Unlock()
				posts, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.message"))
				uploads, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.upload-*"))
				caption, _ := json.Marshal(q.Caption)
				run("recover-caption-request-"+openAttempt, "继续此前的保存任务：用 History 核对本 Room 最早两张原图，将它们与这段 JSON 字符串的准确说明作为一条保存到我的飞书："+string(caption)+"。仍使用原动作 ID real-images-managed-self-caption。原任务已成功，请提交同一完整动作恢复原回执，不创建新任务或新的远端消息。仅使用 TeamX 入口，先读取现有飞书 Skill，遵循请求提交方式；不创建、读取或搜索本地文件。结果发到本 Room。")
				audit.mu.Lock()
				after := audit.Calls["/v1/actions/execute"]
				audit.mu.Unlock()
				postsAfter, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.message"))
				uploadsAfter, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.upload-*"))
				if after <= before || len(postsAfter) != len(posts) || len(uploadsAfter) != len(uploads) {
					t.Fatal("real Agent must recover the same complete action without any native write")
				}
				row["agent_complete_request_recovered_without_native_write"] = true
			}
			if resume && mode == "default-assistant-group" && len(records) == 3 {
				posts, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.message"))
				uploads, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.upload-*"))
				run("verify-retained-images-"+openAttempt, "请从保留的 Room 历史读取原消息，核对我最早两张图片的消息序号和附件 ID，并把结果发到本 Room。请读取原消息，不要用动作账本代替历史；不发送飞书消息、不上传图片、不执行动作，也不创建或读取本地文件。")
				postsAfter, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.message"))
				uploadsAfter, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.upload-*"))
				if len(postsAfter) != len(posts) || len(uploadsAfter) != len(uploads) {
					t.Fatal("retained history probe performed a native write")
				}
				row["retained_history_read_only_probe"] = true
			}
			previousEpoch := svc.info.ServiceEpoch
			closeService(t, svc)
			m.mu.Lock()
			delete(m.runtimes, room.RoomIdentity)
			m.mu.Unlock()
			svc = openRoomOK(t, m, room, "real-images-reopen-"+openAttempt)
			if svc.info.ServiceEpoch == previousEpoch {
				t.Fatal("service did not restart")
			}
			audit.mu.Lock()
			readsBeforeRestartQuery := audit.Calls["/v1/actions/read"] + audit.Calls["/v1/actions/list"]
			audit.mu.Unlock()
			ids := []string{}
			for i, action := range records {
				if mode == "managed-self" && reused != nil && i == 0 {
					continue
				}
				ids = append(ids, action.Request.ID)
			}
			run("receipt-query-after-restart-"+openAttempt, "服务已重启。只用 actions read/list 查询这些动作的原回执："+strings.Join(ids, "、")+"，核对原账号、目标和完整图文。不要 execute、解析新目标、换 ID 或补发。将结果报告到本 Room。")
			for i, action := range records {
				if mode == "managed-self" && reused != nil && i == 0 {
					verifyRealImageMessage(t, ctx, independent, q, cases[i], action)
					continue
				}
				restored, err := svc.store.queryActions(id, action.Request.ID, "", "")
				if err != nil || len(restored) != 1 || !reflect.DeepEqual(restored[0], action) {
					t.Fatal("complete original receipt changed across service restart", err)
				}
				replayRealImageAction(t, svc, id, action, turn)
				verifyRealImageMessage(t, ctx, independent, q, cases[i], action)
			}
			for _, sample := range cases {
				files, err := filepath.Glob(filepath.Join(q.DispatchDir, sample.Key+".upload-*"))
				if err != nil || len(files) != 2*len(sample.Hashes) {
					t.Fatal("unexpected upload count", err)
				}
			}
			audit.mu.Lock()
			reads := audit.Calls["/v1/actions/read"] + audit.Calls["/v1/actions/list"]
			turns := append([]policyRealTurn(nil), audit.Turns...)
			audit.mu.Unlock()
			if reads <= readsBeforeRestartQuery {
				t.Fatal("real Agent did not query the original receipts after restart")
			}
			historyReads := 0
			recoveredInputs := map[string]string{}
			for _, turn := range turns {
				for _, call := range turn.Result.Tools {
					if call.Status != "completed" {
						continue
					}
					if strings.Contains(call.Title, "history_read") && call.Status == "completed" {
						historyReads++
					}
					if strings.HasPrefix(call.Title, "mcp__teamx__") || imageAgentStdinFilter(call) {
						continue
					}
					var input struct {
						Command string `json:"command"`
					}
					rawInput := call.Input
					if call.Title == "bash" && call.DetailsTruncated {
						rawInput = realImageJournalToolInput(t, svc.store.dir, call.ID, call.Input)
						recoveredInputs[call.ID] = rawInput
					}
					if call.Title != "bash" || json.Unmarshal([]byte(rawInput), &input) != nil || !strings.Contains(input.Command, "$TEAMX_CLI") || imageAgentForbiddenCommand.MatchString(input.Command) {
						t.Fatal("Agent attempted native fallback or unsupported tool", call.Title)
					}
				}
			}
			row["recovered_tool_inputs"] = recoveredInputs
			if historyReads == 0 {
				t.Fatal("real Agent did not read retained image history")
			}
			row["status"], row["service_restart"], row["messages"], row["uploads"] = "passed", true, 3, 5
		}) {
			return
		}
	}
	report["status"] = "passed"
	report["messages"], report["uploads"] = 6, 10
	if reused != nil {
		posts, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.message"))
		uploadFiles, _ := filepath.Glob(filepath.Join(q.DispatchDir, "*.upload-*"))
		if len(posts) != 7 || len(uploadFiles) != 24 {
			t.Fatal("total native write budget differs from the approved seven messages/twelve uploads")
		}
		report["native_message_posts"], report["native_image_uploads"] = 7, 12
	}
	report["backflow"] = "run TestRealLarkImageSendBackflow against this report; opening still requires that gate"
}

// Diagnostics intentionally bound tool input. Audit the complete arguments from
// the owned DSH journal instead of accepting or rejecting a truncated command.
func realImageJournalToolInput(t *testing.T, roomDir, callID, prefix string) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(roomDir, "agents", "*", "session.json.dsh-*", "home", "sessions", "*", "*", "session.v4.jsonl.zstd"))
	if err != nil || len(paths) == 0 || callID == "" || prefix == "" {
		t.Fatal("owned session journal unavailable for complete tool audit", err)
	}
	var full string
	for _, path := range paths {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		cmd := exec.CommandContext(ctx, "zstd", "-dc", path)
		var output realImageJournalBuffer
		cmd.Stdout = &output
		err := cmd.Run()
		cancel()
		if err != nil {
			t.Fatal("bounded owned journal decode failed", err)
		}
		scan := bufio.NewScanner(bytes.NewReader(output.Bytes()))
		scan.Buffer(make([]byte, 4096), 2<<20)
		for scan.Scan() {
			var record struct {
				Data struct {
					CallID    string `json:"callId"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"data"`
			}
			if err := json.Unmarshal(scan.Bytes(), &record); err != nil {
				t.Fatal("invalid owned journal record", err)
			}
			v := record.Data
			if v.CallID != callID || v.Name != "bash" || v.Arguments == "" {
				continue
			}
			if !json.Valid([]byte(v.Arguments)) || !strings.HasPrefix(v.Arguments, prefix) || full != "" && full != v.Arguments {
				t.Fatal("full tool input does not match bounded diagnostic")
			}
			full = v.Arguments
		}
		if err := scan.Err(); err != nil {
			t.Fatal("owned journal exceeds audit record limit", err)
		}
	}
	if full == "" {
		t.Fatal("complete matching tool call missing from owned session journal")
	}
	return full
}

type realImageJournalBuffer struct{ data bytes.Buffer }

func (b *realImageJournalBuffer) Bytes() []byte { return b.data.Bytes() }

func (b *realImageJournalBuffer) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > 16<<20 {
		return 0, errors.New("owned journal exceeds audit byte limit")
	}
	return b.data.Write(p)
}

func writeRealImageJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(raw, '\n'), 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func replayRealImageAction(t *testing.T, svc *service, id MemberIdentity, action referenceAction, turn uint64) {
	t.Helper()
	q := action.Request
	replay := agentActionRequest{followupCommand: followupCommand{TurnNumber: turn}, ID: q.ID, TargetRef: q.TargetRef, Operation: q.Operation, Input: q.Input, PolicyToken: q.PolicyToken, Authority: agentActionAuthority{Type: "human_reply", RoomSeq: q.Authority.RoomSeq, Reason: q.Authority.Reason}}
	if q.Operation == platform.OperationMessageSendSelf {
		replay.Authority = agentActionAuthority{}
	}
	raw, _ := json.Marshal(replay)
	if svc.access == nil {
		t.Fatal("bound execution access unavailable")
	}
	token, revoke, err := svc.access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	cmd := exec.CommandContext(t.Context(), svc.cliPath, "actions", "execute", "--request", string(raw))
	cmd.Env = append(svc.toolEnv(id), "TEAMX_EXECUTION_TOKEN="+token)
	output, err := cmd.CombinedOutput()
	var receipt referenceActionReceipt
	if err != nil || json.Unmarshal(output, &receipt) != nil || receipt.Status != "succeeded" || receipt.Result == nil || receipt.Result.ResultID != action.Result.ResultID {
		t.Fatal("same-ID original receipt recovery failed", err)
	}
}

// Read again through an independent real pinned client; do not treat the send
// response or a screenshot as evidence of author/content/image ordering.
func verifyRealImageMessage(t *testing.T, ctx context.Context, pin *lark.Pinned, q larkImageSendRealInput, sample larkImageRealCase, action referenceAction) lark.Message {
	t.Helper()
	message, err := pin.MessageReader().Message(ctx, pin.Account, action.Result.ResultID.Value)
	chat := q.SelfChatID
	if action.Request.Operation == platform.OperationMessageSend {
		chat = q.GroupID
	}
	if err != nil || message.ID != action.Result.ResultID.Value || message.ChatID != chat || message.Sender.ID != q.ExpectedUserID || message.Sender.Type != "user" || message.Sender.IDType != "open_id" || message.Sender.TenantKey != pin.Account.Tenant.Value || message.Deleted || message.ParentID != "" || message.RootID != "" || message.ThreadID != "" {
		t.Fatal("independent native author/target verification failed", lark.ErrorCode(err))
	}
	keys := []string{}
	for i := range sample.Hashes {
		var response struct {
			OK       bool   `json:"ok"`
			Identity string `json:"identity"`
			ImageKey string `json:"image_key"`
		}
		path := filepath.Join(q.DispatchDir, fmt.Sprintf("%s.upload-%d.response", sample.Key, i+1))
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &response) != nil || !response.OK || response.Identity != "user" || response.ImageKey == "" {
			t.Fatal("native upload response missing", i+1)
		}
		keys = append(keys, response.ImageKey)
	}
	if !reflect.DeepEqual(message.ImageKeys(), keys) {
		t.Fatal("upload/read image keys differ; verify native resource equivalence before opening, never skip images")
	}
	original := lark.PreparedImageMessage{Body: sample.Input.Body}
	for _, hash := range sample.Hashes {
		found := false
		for _, source := range q.Images {
			if source.SHA256 != hash {
				continue
			}
			data, err := os.ReadFile(filepath.Join(filepath.Dir(os.Getenv("TEAMX_LARK_IMAGE_SEND_INPUT")), source.Path))
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
				t.Fatal("reviewed original changed")
			}
			original.Images = append(original.Images, lark.SendImage{Data: data, SHA256: hash})
			found = true
		}
		if !found {
			t.Fatal("original hash not in reviewed manifest")
		}
	}
	if !original.VerifyContent(message.Type, message.Body.Content, keys) {
		t.Fatal("independent exact caption/blank lines/ordered images verification failed")
	}
	return message
}
