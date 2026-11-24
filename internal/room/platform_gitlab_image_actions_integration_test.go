//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

type gitlabImageFixture struct {
	*managedActionFixture
	server   *httptest.Server
	cli      func([]string, any) error
	refs     []gitlab.ImageRef
	pixels   [][]byte
	requests sync.WaitGroup
	uploads  atomic.Int32
	mode     string
	body     string
	hook     func(*http.Request)
}

func gitlabImageActionFixture(t *testing.T, discussion bool) *gitlabImageFixture {
	t.Helper()
	return configureGitlabImageFixture(t, managedActionTest(t, discussion))
}

func configureGitlabImageFixture(t *testing.T, fixture *managedActionFixture) *gitlabImageFixture {
	t.Helper()
	f := &gitlabImageFixture{managedActionFixture: fixture}
	f.server = httptest.NewServer(f.m)
	t.Cleanup(f.server.Close)
	f.cli = contractCLI(t, f.server.URL, f.svc, f.id)
	for i := range 4 {
		data := attachmentTestPNG(t, i == 0)
		imageID := attachmentTestID(i + 1)
		status, raw := attachmentUploadHTTP(t, f.server, f.svc.info, imageID, data, false)
		if status != 200 {
			t.Fatal(status, string(raw))
		}
		status, raw = attachmentJSON(t, f.server, "/v1/send", apiRequest{ServiceInfo: f.svc.info, Caller: Caller{Author: Author{Kind: "human", ID: humanOperator()}}, ClientMessageID: imageID, Attachments: []RoomAttachmentInput{{ID: imageID}}})
		if status != 200 {
			t.Fatal(status, string(raw))
		}
		var receipt PostReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		f.refs = append(f.refs, gitlab.ImageRef{MessageSeq: receipt.Seq, AttachmentID: imageID})
		f.pixels = append(f.pixels, data)
	}
	posted, err := f.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: f.id.RoomIdentity, Caller: Caller{Author: Author{Kind: "human", ID: humanOperator()}}, ClientMessageID: "image-authority", Text: "把指定原图和说明一起评论到这个 MR；如果目标是讨论，回复该准确讨论"})
	if err != nil {
		t.Fatal(err)
	}
	prior, err := f.svc.store.LoadAgent(f.id)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: f.id, TurnNumber: 1, LastOfferedUnreadSeq: posted.Seq, LastOfferedInboxSeq: prior.LastOfferedInboxSeq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: posted.Seq, OfferedUnreadSeq: posted.Seq, InboxSnapshotSeq: prior.LastOfferedInboxSeq}}); err != nil {
		t.Fatal(err)
	}
	f.action.Authority = referenceActionAuthority{RoomSeq: posted.Seq, Reason: "直接 Room 人类指令要求向准确 MR 或讨论发送选定完整图文"}
	// Bootstrap through the existing saved-followup resolver; this is business
	// target setup, not a new URL/native-ID entry or an execution prerequisite.
	if f.action.TargetRef != "" {
		if _, err = f.m.resolveActionTarget(context.Background(), f.req, actionResolveRequest{Provider: "gitlab", Locator: platform.TargetLocator{TargetRef: f.action.TargetRef}, TurnNumber: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// Unassociated actions exercise the minimal seven-field contract.
	f.action.followupCommand = followupCommand{TurnNumber: 1}
	f.setInput(t, "", f.refs[:2])
	managedReferenceHTTP(t, f.m, func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		defer f.requests.Done()
		if f.hook != nil {
			f.hook(r)
		}
		if r.Context().Err() != nil || (f.mode == "cancel-upload" && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/uploads") && f.uploads.Load() == 1) {
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-platform-secret" {
			t.Error("wrong account credential")
		}

		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/uploads") {
			selected, _ := gitlab.DecodeImageActionInput(actionInputJSON(f.action.Input))
			index := (int(f.uploads.Add(1))-1)%len(selected.Images) + 1
			original := 0
			for i, ref := range f.refs {
				if ref == selected.Images[index-1] {
					original = i
					break
				}
			}
			if err := r.ParseMultipartForm(3 << 20); err != nil {
				t.Error(err)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, h, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if index > len(f.pixels) || !bytes.Equal(data, f.pixels[original]) || h.Filename != fmt.Sprintf("image-%d.png", index) || h.Header.Get("Content-Type") != "image/png" {
				t.Error("original byte or order mismatch")
			}
			if f.mode == "upload-failure" && index == 2 {
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"url": fmt.Sprintf("/uploads/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/image-%d.png", index), "full_path": fmt.Sprintf("/-/project/123/uploads/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/image-%d.png", index), "markdown": "/close"})
			return
		}
		note := func() string {
			var value map[string]any
			_ = json.Unmarshal([]byte(managedActionNote), &value)
			value["body"] = f.body
			value["id"] = 98 + f.posts.Load()
			data, _ := json.Marshal(value)
			return string(data)
		}
		if r.Method == "POST" {
			f.posts.Add(1)
			want := "/api/v4/projects/123/merge_requests/42/notes"
			if f.action.Operation == platform.OperationDiscussionReply {
				want = "/api/v4/projects/123/merge_requests/42/discussions/thread/notes"
			}
			if r.URL.Path != want {
				t.Error("wrong thread", r.URL.Path)
			}
			var input map[string]string
			if json.NewDecoder(r.Body).Decode(&input) != nil || len(input) != 1 {
				t.Error("bad note input")
			}
			f.body = input["body"]
			if f.mode == "post-502" {
				w.WriteHeader(502)
				return
			}
			io.WriteString(w, note())
			return
		}
		if strings.HasSuffix(r.URL.Path, fmt.Sprintf("/notes/%d", 98+f.posts.Load())) {
			if f.mode == "read-failure" {
				w.WriteHeader(503)
				return
			}
			io.WriteString(w, note())
			return
		}
		if f.posts.Load() > 0 && strings.HasSuffix(r.URL.Path, "/discussions/thread") {
			if f.mode == "thread-failure" {
				io.WriteString(w, `{"id":"thread","notes":[]}`)
				return
			}
			io.WriteString(w, `{"id":"thread","individual_note":false,"notes":[`+note()+`]}`)
			return
		}
		f.serve(w, r)
	})
	transport := f.m.platformHTTP
	f.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Error("missing per-request bound")
		}
		return transport.RoundTrip(r)
	})
	return f
}
func (f *gitlabImageFixture) setInput(t *testing.T, body string, refs []gitlab.ImageRef) {
	t.Helper()
	raw, _ := json.Marshal(gitlab.ImageActionInput{Body: body, Images: refs})
	if err := json.Unmarshal(raw, &f.action.Input); err != nil {
		t.Fatal(err)
	}
}
func (f *gitlabImageFixture) executeCLI(t *testing.T, out any) error {
	t.Helper()
	wire := gitlabAgentWire(f.action)
	raw, _ := json.Marshal(wire)
	return f.cli([]string{"actions", "execute", "--request", string(raw)}, out)
}

func TestGitlabImagesManagedCLI(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		for _, mode := range []string{"images", "caption", "4096", "upload-failure", "post-502", "read-failure", "thread-failure"} {
			t.Run(fmt.Sprintf("%t/%s", discussion, mode), func(t *testing.T) {
				f := gitlabImageActionFixture(t, discussion)
				f.mode = mode
				body := ""
				if mode == "caption" {
					body = "中文\r\n**问题截图**"
				}
				if mode == "4096" {
					body = strings.Repeat("x", 4096)
				}
				f.setInput(t, body, f.refs[:2])
				// Formal discovery and execution use one implementation; discovery never uploads.
				var target agentGitlabTargetView
				resolve := fmt.Sprintf(`{"provider":"gitlab","locator":{"target_ref":%q},"turn_number":1}`, f.action.TargetRef)
				if err := f.cli([]string{"actions", "resolve", "--request", resolve}, &target); err != nil {
					t.Fatal(err)
				}
				if target.Resolution.Target == nil || target.Resolution.Target.MRIID.Value != "42" || f.uploads.Load() != 0 || target.TargetRef != f.action.TargetRef {
					t.Fatal(target)
				}
				for _, cap := range target.Actions {
					if !strings.Contains(string(cap.InputSchema), `"images"`) || !strings.Contains(cap.Authority, "human_reply") {
						t.Fatal(cap)
					}
				}
				t.Setenv("TEAMX_GITLAB_BIN", filepath.Join(t.TempDir(), "missing"))
				t.Setenv("TEAMX_GLAB_BIN", "/missing/glab")
				beforeState := historyTree(t, f.svc.store.dir)
				var got agentGitlabReceipt
				if err := f.executeCLI(t, &got); err != nil {
					t.Fatal(err)
				}
				want := "succeeded"
				posts := int32(1)
				switch mode {
				case "upload-failure":
					want = "not_applied"
					posts = 0
				case "read-failure", "post-502":
					want = "unknown"
				case "thread-failure":
					if discussion {
						want = "unknown"
					}
				}
				if got.Status != want || got.Attempts != 1 || got.Result.Verified != (want == "succeeded") || got.Result.Account.ID.Value != "101" || f.posts.Load() != posts || got.Input != f.action.Input {
					t.Fatal(got, f.posts.Load())
				}
				if (mode == "read-failure" || mode == "thread-failure") && got.Result.ResultID.Value != "99" {
					t.Fatal("lost note ID", got)
				}
				actions, err := f.svc.store.queryActions(f.id, f.action.ID, "", "")
				if err != nil || len(actions) != 1 {
					t.Fatal(actions, err)
				}
				a := actions[0]
				if !a.Spec.DirectRoomInstructionOnly || a.Spec.Retry.MaxAttempts != 1 || a.Spec.Retry.RetryUnknown || a.Inspection.ContentDigest == "" || a.Request.Input != f.action.Input {
					t.Fatal(a.Spec, a.Inspection)
				}
				before := f.uploads.Load()
				if err := f.executeCLI(t, &got); err != nil || f.uploads.Load() != before || f.posts.Load() != posts {
					t.Fatal("replay wrote", err)
				}
				var detail actionQueryRecord
				if err := f.cli([]string{"actions", "read", "--id", f.action.ID}, &detail); err != nil || detail.Detail == nil || detail.Detail.Request.Input != f.action.Input || f.uploads.Load() != before {
					t.Fatal(detail, err)
				}
				afterState := historyTree(t, f.svc.store.dir)
				for path, hash := range beforeState {
					if !strings.Contains(path, "/actions/") && afterState[path] != hash {
						t.Errorf("action changed Room/input/History state: %s", path)
					}
				}
				for path := range afterState {
					if _, ok := beforeState[path]; !ok && !strings.Contains(path, "/actions/") {
						t.Errorf("new non-action state: %s", path)
					}
				}
				if mode == "caption" {
					if err := os.Remove(filepath.Join(f.svc.store.attachmentPath(f.refs[0].AttachmentID), "data")); err != nil {
						t.Fatal(err)
					}
					if err := f.executeCLI(t, &got); err != nil || got.Status != "succeeded" {
						t.Fatal("saved receipt depended on original lifetime", got, err)
					}
					originalID := f.action.ID
					f.action.ID = "missing-original"
					if err := f.executeCLI(t, &got); err == nil || f.uploads.Load() != before {
						t.Fatal("fresh action used missing original", err)
					}
					f.action.ID = originalID
				}
				f.setInput(t, "changed", f.refs[:2])
				if err := f.executeCLI(t, &got); err == nil || f.uploads.Load() != before {
					t.Fatal("changed input accepted", err)
				}
				assertActionCallsSettled(t, f.m)
				assertActionImagePinsReleased(t, f.svc.store)
			})
		}
	}
}

func TestGitlabImagesPreflightAndPolicy(t *testing.T) {
	for _, mode := range []string{"policy", "rule", "instruction", "question", "message-refs", "code", "html", "quick", "unknown-field", "bad-id", "fraction", "duplicate", "old-text-limit"} {
		t.Run(mode, func(t *testing.T) {
			f := gitlabImageActionFixture(t, false)
			switch mode {
			case "policy":
				f.action.PolicyToken = strings.Repeat("0", 64)
			case "rule":
				f.action.Authority = referenceActionAuthority{RuleID: policyRule().ID, RuleVersion: 1, Reason: "rule"}
			case "instruction":
				f.action.Authority.InstructionRef = "instruction_missing"
				f.action.Authority.InstructionVersion = 1
			case "question":
				f.action.Authority.QuestionRef = "question_missing"
			case "message-refs":
				f.action.Authority.MessageRefs = []string{"room:1"}
			case "code":
				f.setInput(t, "```\nopen", f.refs)
			case "html":
				f.setInput(t, "<script>\nopen", f.refs)
			case "quick":
				f.setInput(t, "\u3000/close\r\n", f.refs)
			case "unknown-field":
				_ = json.Unmarshal([]byte(`{"images":[{"message_seq":1,"attachment_id":"img_0123456789abcdef0123456789abcdef","extra":true}]}`), &f.action.Input)
			case "bad-id":
				f.setInput(t, "", []gitlab.ImageRef{{MessageSeq: 1, AttachmentID: "/tmp/image.png"}})
			case "fraction":
				_ = json.Unmarshal([]byte(`{"images":[{"message_seq":1.5,"attachment_id":"img_0123456789abcdef0123456789abcdef"}]}`), &f.action.Input)
			case "duplicate":
				f.setInput(t, "", []gitlab.ImageRef{f.refs[0], f.refs[0]})
			case "old-text-limit":
				f.setInput(t, strings.Repeat("x", 4097), nil)
			}
			got, err := f.run()
			if mode == "policy" {
				if err != nil || got.Policy == nil || got.Status != "policy_changed" {
					t.Fatal(got, err)
				}
				if !reflect.DeepEqual(projectActionReceipt(got, f.action), got) {
					t.Fatal("policy projected away")
				}
			} else if err == nil {
				t.Fatal("invalid action accepted", got)
			}
			if f.uploads.Load() != 0 || f.posts.Load() != 0 {
				t.Fatal("preflight wrote")
			}
			assertActionCallsSettled(t, f.m)
			assertActionImagePinsReleased(t, f.svc.store)
		})
	}
}

func TestGitlabImagesFinalDispatchAndDeadline(t *testing.T) {
	for _, mode := range []string{"dispatch", "cancel-upload", "deadline", "digest-equivalent"} {
		t.Run(mode, func(t *testing.T) {
			f := gitlabImageActionFixture(t, false)
			prepare := f.m.referenceExecutors["gitlab"]
			f.m.referenceExecutors["gitlab"] = func(ctx context.Context, id MemberIdentity, a platform.AccessContext) (referenceActionSession, error) {
				s, err := prepare(ctx, id, a)
				if err != nil {
					return s, err
				}
				cap := s.Capabilities.Actions[f.action.Operation]
				execute := cap.Execute

				cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
					final, ok := ctx.Value(actionDispatchCheckKey{}).(actionDispatchCheck)
					if !ok {
						t.Fatal("missing final dispatch")
					}
					ctx = context.WithValue(ctx, actionDispatchCheckKey{}, actionDispatchCheck(func(ctx context.Context, actual platform.TargetActionRequest, digest string) error {
						if !bytes.Equal(actual.Input, actionInputJSON(f.action.Input)) || digest == "" {
							t.Error("generated body replaced original input")
						}
						if mode == "dispatch" {
							return ErrConflict
						}
						return final(ctx, actual, digest)
					}))
					return execute(ctx, q, a)
				}
				s.Capabilities.Actions[f.action.Operation] = cap
				return s, nil
			}
			base := f.m.platformHTTP
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				// Each HTTP deadline is 30s, bounded by one earlier session deadline.
				if mode == "deadline" {
					want, _ := ctx.Deadline()
					if d, ok := r.Context().Deadline(); !ok || !d.Equal(want) {
						t.Error("reset original absolute deadline", d, want)
					}
				}
				if mode == "cancel-upload" && strings.HasSuffix(r.URL.Path, "/uploads") {
					cancel()
					return nil, context.Canceled
				}
				return base.RoundTrip(r)
			})
			if mode == "deadline" {
				var c context.CancelFunc
				ctx, c = context.WithTimeout(ctx, time.Second)
				defer c()
			}
			got, err := f.m.executeReferenceAction(ctx, f.req, f.action)
			want := "succeeded"
			if mode == "dispatch" || mode == "cancel-upload" {
				want = "not_applied"
			}
			if err != nil || got.Status != want || mode == "dispatch" && f.posts.Load() != 0 {
				t.Fatal(got, err)
			}
			if mode == "digest-equivalent" {
				f.mode = "read-failure"
				f.action.ID = "uncertain"
				f.setInput(t, "different", f.refs[:2])
				got, err = f.run()
				if err != nil || got.Status != "unknown" {
					t.Fatal(got, err)
				}
				// Same bytes from different source references and a new action ID
				// must not escape the original uncertain-content guard.
				f.setInput(t, "different", []gitlab.ImageRef{f.refs[0], f.refs[2]})
				f.action.ID = "do-not-resend"
				before := f.uploads.Load()
				if _, err = f.run(); err == nil || f.uploads.Load() != before {
					t.Fatal("uncertain content duplicated", err)
				}
			}
			assertActionCallsSettled(t, f.m)
			assertActionImagePinsReleased(t, f.svc.store)
		})
	}
}

func TestGitlabImageReceiptPolicyPreservation(t *testing.T) {
	f := gitlabImageActionFixture(t, true)
	got, err := f.run()
	if err != nil {
		t.Fatal(err)
	}
	view, _ := f.svc.store.readAgentPolicy(f.id)
	for _, status := range []string{"not_applied", "policy_changed", "processing"} {
		got.Status = status
		got.Policy = &view
		if !reflect.DeepEqual(projectActionReceipt(got, f.action), got) {
			t.Fatal("lost policy with result")
		}
	}
	// Parsing full legacy receipts remains supported via actions read.
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "test-platform-secret") {
		t.Fatal("credential leak")
	}
}

func TestGitlabImagesHelpAndDiagnostics(t *testing.T) {
	f := gitlabImageActionFixture(t, false)
	var out, stderr bytes.Buffer
	if err := runCLI(context.Background(), []string{"actions", "execute", "--help"}, &out, &stderr, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"object.comment", "discussion.reply", "--request-file", "room_seq", "when declared by input_schema", "context:"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("help missing", text)
		}
	}
	_ = json.Unmarshal([]byte(`{"images":[{"message_seq":1,"attachment_id":23}]}`), &f.action.Input)
	wire := gitlabAgentWire(f.action)
	raw, _ := json.Marshal(wire)
	err := runCLI(context.Background(), []string{"actions", "execute", "--request", string(raw)}, &bytes.Buffer{}, &stderr, cliToolEnv(f.server.URL, f.svc, f.member))
	if err == nil || !strings.Contains(err.Error()+stderr.String(), "input.images[0].attachment_id") {
		t.Fatal(err, stderr.String())
	}
	// File input is read at the CLI, using the same managed endpoint and contract.
	f.setInput(t, "caption", f.refs[:2])
	wire = gitlabAgentWire(f.action)
	raw, _ = json.Marshal(wire)
	path := filepath.Join(t.TempDir(), "action.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var got agentGitlabReceipt
	if err = f.cli([]string{"actions", "execute", "--request-file", path}, &got); err != nil || !got.Result.Verified {
		t.Fatal(got, err)
	}
}

// Evidence records actual managed DTOs, projections and byte sizes, separately
// from later real Agent/platform runs. Opt-in only writes the requested local file.
func TestGitlabImageAgentContractEvidence(t *testing.T) {
	f := gitlabImageActionFixture(t, true)
	sample := func(value any) map[string]any {
		raw, _ := json.Marshal(value)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		return map[string]any{"utf8_bytes": len(raw), "top_level_fields": len(fields), "value": value}
	}
	evidence := map[string]any{"old_image_baseline": nil, "remote_business_writes": 0, "runtime": "actual manager/service/CLI with local HTTPS fixture", "known_target_business_calls": []string{"actions execute"}, "first_contract_business_calls": []string{"actions resolve", "actions execute"}, "necessary_preconditions": "current turn/policy_token and real instruction; image publication and existing managed target setup are counted separately from action calls"}
	f.m.referenceExecutors["gitlab"] = f.m.prepareGitlabReferenceAction
	textTarget, err := f.m.resolveActionTarget(context.Background(), f.req, actionResolveRequest{Provider: "gitlab", Locator: platform.TargetLocator{TargetRef: f.action.TargetRef}, TurnNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.m.toolCatalog[0].Skill = defaultPlatformTools()[0].Skill
	var discovered any
	if err := f.cli([]string{"tools", "list"}, &discovered); err != nil {
		t.Fatal(err)
	}
	evidence["tool_discovery_projection"] = sample(discovered)
	evidence["text_target_original_dto"] = sample(textTarget)
	evidence["text_target_projection"] = sample(projectActionTarget(textTarget))
	f.setInput(t, "exact reply", nil)
	f.action.ID = "text-baseline"
	textReceipt, err := f.run()
	if err != nil || textReceipt.Status != "succeeded" {
		t.Fatal(textReceipt, err)
	}
	evidence["text_receipt_original_dto"] = sample(textReceipt)
	evidence["text_receipt_projection"] = sample(projectActionReceipt(textReceipt, f.action))
	// Reset only local fixture counters; records and action IDs remain durable.
	f.posts.Store(0)
	f.action.ID = "image-contract"
	f.setInput(t, "完整说明\n两张原图", f.refs[:2])
	target, err := f.m.resolveActionTarget(context.Background(), f.req, actionResolveRequest{Provider: "gitlab", Locator: platform.TargetLocator{TargetRef: f.action.TargetRef}, TurnNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	evidence["image_target_internal_dto"] = sample(target)
	evidence["image_target_projection"] = sample(projectActionTarget(target))
	wire := gitlabAgentWire(f.action)
	evidence["image_execute_request"] = sample(wire)
	var receipt agentGitlabReceipt
	if err = f.executeCLI(t, &receipt); err != nil || !receipt.Result.Verified {
		t.Fatal(receipt, err)
	}
	evidence["image_receipt_projection"] = sample(receipt)
	records, err := f.svc.store.queryActions(f.id, f.action.ID, "", "")
	if err != nil || len(records) != 1 {
		t.Fatal(err)
	}
	internal, err := f.svc.store.existingReferenceAction(f.id, f.action)
	if err != nil {
		t.Fatal(err)
	}
	evidence["image_receipt_internal_dto"] = sample(internal)
	evidence["image_action_frozen_detail"] = sample(records[0])
	evidence["execute_help"] = map[string]any{"utf8_bytes": len(referenceHelpText("actions", "execute")), "text": referenceHelpText("actions", "execute")}
	evidence["resolve_help"] = map[string]any{"utf8_bytes": len(referenceHelpText("actions", "resolve")), "text": referenceHelpText("actions", "resolve")}
	skill, err := os.ReadFile("../platform/tool/skills/gitlab/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	evidence["gitlab_skill"] = map[string]any{"utf8_bytes": len(skill)}
	raw, _ := json.Marshal(wire)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if len(fields) != 7 {
		t.Fatal("minimal request changed", string(raw))
	}
	if path := os.Getenv("TEAMX_GITLAB_IMAGE_EVIDENCE"); path != "" {
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitlabImagesRevocationStopsRemainingRequests(t *testing.T) {
	for _, mode := range []string{"admission-cancel", "turn", "session"} {
		t.Run(mode, func(t *testing.T) {
			f := gitlabImageActionFixture(t, false)
			base := f.m.platformHTTP
			f.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				response, err := base.RoundTrip(r)
				if err == nil && strings.HasSuffix(r.URL.Path, "/uploads") {
					switch mode {
					case "admission-cancel":
						f.m.mu.Lock()
						f.m.cancelPlatformCallsLocked(func(call *platformCall) bool { return call.platformID == f.connection.ID })
						f.m.mu.Unlock()
					case "turn":
						agent, e := f.svc.store.LoadAgent(f.id)
						if e != nil {
							t.Error(e)
						} else {
							agent.TurnNumber = 2
							agent.Turn.Number = 2
							if e = f.svc.store.SaveAgent(agent); e != nil {
								t.Error(e)
							}
						}
					case "session":
						path := filepath.Join(f.svc.store.dir, "agents", f.id.MembershipID, "session.json")
						ref := sessionReference{Version: formatVersion, MemberIdentity: f.id, Directory: filepath.Base(dshStateDir(path)) + "-" + strings.Repeat("b", 32)}
						if err := os.WriteFile(path, mustJSON(t, ref), 0600); err != nil {
							t.Error(err)
						}
					}
				}
				return response, err
			})
			got, err := f.run()
			if err != nil || got.Status != "not_applied" || f.uploads.Load() != 1 || f.posts.Load() != 0 {
				t.Fatal(got, err, f.uploads.Load(), f.posts.Load())
			}
			assertActionCallsSettled(t, f.m)
			assertActionImagePinsReleased(t, f.svc.store)
		})
	}
}

func gitlabAgentWire(q referenceActionRequest) map[string]any {
	return map[string]any{"target_ref": q.TargetRef, "turn_number": q.TurnNumber, "id": q.ID, "operation": q.Operation, "input": q.Input, "policy_token": q.PolicyToken, "authority": projectActionAuthority(q.Authority)}
}

func TestGitlabImagesPolicyChangeBeforeAttempt(t *testing.T) {
	f := gitlabImageActionFixture(t, false)
	prepare := f.m.referenceExecutors["gitlab"]
	f.m.referenceExecutors["gitlab"] = func(ctx context.Context, id MemberIdentity, access platform.AccessContext) (referenceActionSession, error) {
		session, err := prepare(ctx, id, access)
		if err != nil {
			return session, err
		}
		check := session.Check
		checks := 0
		session.Check = func(ctx context.Context) error {
			checks++
			if checks == 3 {
				rule := policyRule()
				rule.Version = 1
				rule.Text = "已经更新的业务规则"
				if _, err := f.svc.store.changePolicy(f.id, humanCaller, rule); err != nil {
					return err
				}
			}
			return check(ctx)
		}
		return session, nil
	}
	var raw map[string]json.RawMessage
	if err := f.executeCLI(t, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["policy"] == nil || raw["result"] == nil || raw["input"] != nil || f.uploads.Load() != 0 || f.posts.Load() != 0 {
		t.Fatal(string(mustContractJSON(raw)), f.uploads.Load(), f.posts.Load())
	}
	var receipt referenceActionReceipt
	if err := json.Unmarshal(mustContractJSON(raw), &receipt); err != nil || receipt.Policy == nil || receipt.Result == nil || receipt.Status != "not_applied" {
		t.Fatal(receipt, err)
	}
	assertActionImagePinsReleased(t, f.svc.store)
}

func TestGitlabImagesSelectionsCLI(t *testing.T) {
	for _, selection := range []string{"first", "second", "reverse", "four"} {
		t.Run(selection, func(t *testing.T) {
			f := gitlabImageActionFixture(t, false)
			refs := f.refs[:1]
			switch selection {
			case "second":
				refs = f.refs[1:2]
			case "reverse":
				refs = []gitlab.ImageRef{f.refs[1], f.refs[0]}
			case "four":
				refs = f.refs
			}
			f.setInput(t, "", refs)
			var got agentGitlabReceipt
			if err := f.executeCLI(t, &got); err != nil || !got.Result.Verified || f.uploads.Load() != int32(len(refs)) || f.posts.Load() != 1 {
				t.Fatal(got, err, f.uploads.Load(), f.posts.Load())
			}
			var input gitlab.ImageActionInput
			if err := json.Unmarshal(actionInputJSON(got.Input), &input); err != nil || !reflect.DeepEqual(input.Images, refs) {
				t.Fatal(input, err)
			}
			assertActionImagePinsReleased(t, f.svc.store)
		})
	}
}

func TestGitlabImagesExistingMatterContext(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		t.Run(fmt.Sprint(discussion), func(t *testing.T) {
			f := gitlabImageActionFixture(t, discussion)
			f.svc.store.mu.Lock()
			units, err := f.svc.store.followupUnitsLocked(f.id, "")
			f.svc.store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			var unit followupUnit
			for _, u := range units {
				if u.TargetRef == f.action.TargetRef {
					unit = u
					break
				}
			}
			if unit.Ref == "" {
				t.Fatal("missing original matter")
			}
			wire := gitlabAgentWire(f.action)
			delete(wire, "target_ref")
			wire["context"] = &actionBusinessContext{unit.ObjectRef, unit.Ref, unit.Revision}
			var got agentGitlabReceipt
			if err := f.cli([]string{"actions", "execute", "--request", string(mustContractJSON(wire))}, &got); err != nil || !got.Result.Verified || got.TargetRef != f.action.TargetRef || got.UnitVersion <= unit.Revision {
				t.Fatal(got, err)
			}
		})
	}
}
