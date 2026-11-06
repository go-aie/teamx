package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func readViewJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func removeReadStorageHeader(v map[string]any) {
	for _, key := range []string{"version", "room_id", "room_instance", "agent_id", "membership_id"} {
		delete(v, key)
	}
}

func assertReadViewEqual(t *testing.T, original, expected, actual any) {
	t.Helper()
	if !reflect.DeepEqual(expected, readViewJSON(t, actual)) {
		t.Fatalf("unexpected business difference:\nwant %s\ngot %s", mustContractJSON(expected), mustContractJSON(actual))
	}
	before, after := len(mustContractJSON(original)), len(mustContractJSON(actual))
	t.Logf("JSON bytes: %d -> %d (saved %d)", before, after, before-after)
}

func TestAgentFollowupReadProjection(t *testing.T) {
	h, svc, units, _ := conversationHarness(t, 1)
	u := units[0]
	h.store.mu.Lock()
	target, err := h.store.loadTargetLocked(h.id, u.ObjectRef, u.TargetRef)
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_, access, _ := gitlabReferenceFixture()
	access.RoomIdentity, access.AgentID, access.MembershipID = h.id.RoomIdentity, h.id.AgentID, h.id.MembershipID
	now := time.Now().UTC()
	human := RoomMessage{Seq: 19, Author: Author{Kind: "human", ID: "operator"}, Text: "撤销上一条，只回复此讨论", ReplyTo: 18, CreatedAt: now}
	basis := followupReplyBasis{QuestionRef: "question-saved", TargetRef: u.TargetRef, MessageRefs: []string{"note-17"},
		Intent: followupIntent{Operation: platform.OperationDiscussionReply, Body: "准确正文", Constraints: "仅此讨论"}, Source: human, Basis: "真实用户答复"}
	for _, rich := range []bool{false, true} {
		t.Run(fmt.Sprint(rich), func(t *testing.T) {
			unit := u
			if rich {
				unit.Summary, unit.Next, unit.Evidence = "处理进展", "核实未知结果", "完整证据"
				unit.Questions = []followupQuestion{{Ref: basis.QuestionRef, TargetRef: u.TargetRef, Text: "可以回复吗？", MessageRefs: basis.MessageRefs,
					Proposed: &basis.Intent, ClientMessageID: "stable-send", Seq: 18, Answers: []followupAnswer{{Message: human, Basis: "明确答复"}}}}
				unit.Instructions = []followupInstruction{{Ref: "instruction-saved", Version: 7, followupReplyBasis: basis,
					Status: "revoked", ExpiresAt: &now, Evidence: "真实撤销", Revocation: &human, UpdatedAt: now}}
				for _, status := range []string{"succeeded", "unknown", "not_applied", "processing"} {
					unit.Actions = append(unit.Actions, referenceAction{Request: referenceActionRequest{followupCommand: conversationCommand(unit, 1),
						ID: "saved-" + status, TargetRef: u.TargetRef, PolicyToken: strings.Repeat("a", 64), Input: CommentInput{Body: "完整评论正文"},
						Authority: referenceActionAuthority{RuleID: "rule", RuleVersion: 3, Reason: "准确授权依据"}},
						Target: target.Target, Access: access, Basis: &basis, Rule: &PolicyRevision{ID: "rule", Version: 3, Text: "完整规则", ExpiresAt: &now},
						Status: status, Attempts: 2, PossibleDuplicate: true, UpdatedAt: now,
						Result: &platform.TargetActionResult{Status: status, Account: access.Account, Target: target.Target, ReasonCode: "retained", Message: "完整结果", URL: "https://example.test/result"}})
				}
			}
			original := followupReadResult{unit, target, []platform.ActionSpec{{Operation: platform.OperationObjectComment}}}
			view, err := projectAgentFollowupRead(h.id, original)
			if err != nil {
				t.Fatal(err)
			}
			expected := readViewJSON(t, original)
			removeReadStorageHeader(expected["unit"].(map[string]any))
			removeReadStorageHeader(expected["target"].(map[string]any))
			for k, v := range readViewJSON(t, h.id) {
				expected[k] = v
			}
			eu := expected["unit"].(map[string]any)
			eu["unit_version"] = eu["revision"]
			delete(eu, "revision")
			et := expected["target"].(map[string]any)
			et["target_ref"] = et["ref"]
			delete(et, "ref")
			// Check every retained action field against original evidence independently.
			expectedActions := []any{}
			for _, a := range unit.Actions {
				m := map[string]any{"id": a.Request.ID, "authority": readViewJSON(t, projectActionAuthority(a.Request.Authority)), "retry": readViewJSON(t, a.Spec.Retry), "target_ref": a.Request.TargetRef, "target": readViewJSON(t, a.Target), "account": readViewJSON(t, a.Access.Account), "operation": string(a.Request.Operation), "input": readViewJSON(t, a.Request.Input), "status": a.Status, "attempts": float64(a.Attempts), "updated_at": a.UpdatedAt.Format(time.RFC3339Nano)}
				if a.Status != "succeeded" && a.Basis != nil {
					m["basis"] = readViewJSON(t, a.Basis)
				}
				if a.PossibleDuplicate {
					m["possible_duplicate"] = true
				}
				if a.Result != nil {
					m["result"] = readViewJSON(t, a.Result)
				}
				expectedActions = append(expectedActions, m)
			}
			eu["actions"] = expectedActions
			expected["complete"] = true
			expected["details"] = map[string]any{"object_ref": u.ObjectRef, "unit_ref": u.Ref, "turn_number": float64(u.TurnNumber), "omitted": []any{"action execution snapshots"}}
			assertReadViewEqual(t, original, expected, view)
		})
	}
	// Exercise the actual service boundary: human detail keeps both headers.
	for _, agent := range []bool{false, true} {
		req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: h.id.AgentID, MembershipID: h.id.MembershipID,
			Followup: &followupRequest{followupCommand: conversationCommand(u, 1)}}
		if agent {
			req.Caller, req.AgentID, req.MembershipID = agentCallerIdentity(h.id), "", ""
		}
		result, err := svc.memberFollowups(context.Background(), req, "/v1/matters/read")
		if err != nil {
			t.Fatal(err)
		}
		_, header := readViewJSON(t, result)["unit"].(map[string]any)["version"]
		if header == agent {
			t.Fatal("wrong caller projection", agent, result)
		}
	}
	for _, field := range []string{"unit", "target", "schema"} {
		original := followupReadResult{Unit: u, Target: target}
		want := ErrIdentity
		switch field {
		case "unit":
			original.Unit.MembershipID = "other"
		case "target":
			original.Target.RoomInstance = "other"
		case "schema":
			original.Unit.Version++
			want = ErrVersion
		}
		if _, err := projectAgentFollowupRead(h.id, original); !errors.Is(err, want) {
			t.Fatal(field, err)
		}
	}
}

func TestAgentInboxReadProjection(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	for _, key := range []string{"one", "two"} {
		receiveReference(t, h, key, referenceTestSource(key))
	}
	inboxRunning(t, h, 1, 2)
	svc := referenceTestService(h)
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			original := page
			if empty {
				original.Entries = []InboxEntry{}
			}
			view, err := projectAgentInboxRead(h.id, original)
			if err != nil {
				t.Fatal(err)
			}
			expected := readViewJSON(t, original)
			for _, e := range expected["entries"].([]any) {
				removeReadStorageHeader(e.(map[string]any))
			}
			sources := expected["sources"].([]any)
			for _, entry := range expected["entries"].([]any) {
				e := entry.(map[string]any)
				for _, source := range sources {
					if source.(map[string]any)["seq"] == e["seq"] {
						e["source"] = source
					}
				}
			}
			delete(expected, "sources")
			for _, summary := range expected["followups"].([]any) {
				v := summary.(map[string]any)
				v["unit_ref"] = v["ref"]
				delete(v, "ref")
				v["unit_version"] = v["revision"]
				delete(v, "revision")
			}
			assertReadViewEqual(t, original, expected, view)
			view.Batch.Receipt = ""
			delete(expected["batch"].(map[string]any), "receipt")
			assertReadViewEqual(t, original, expected, view)
		})
	}
	page.Entries[1].MembershipID = "other"
	if _, err := projectAgentInboxRead(h.id, page); !errors.Is(err, ErrIdentity) {
		t.Fatal("mismatched entry hidden", err)
	}
}

func TestAgentInboxCLIOutputAndConfirmation(t *testing.T) {
	for _, mode := range []string{"complete", "peek", "short", "broken", "confirm-failed", "confirm-lost"} {
		t.Run(mode, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			for i := range 3 {
				key := fmt.Sprint(i)
				receiveReference(t, h, key, referenceTestSource(key))
			}
			inboxRunning(t, h, 1, 3)
			svc := referenceTestService(h)
			svc.requests, svc.info.ServiceEpoch = make(chan struct{}, 32), "a3-test"
			svc.referenceResolver = referenceTestResolver
			confirms := 0
			var receipt string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/inbox/confirm" {
					confirms++
					var req apiRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					receipt = req.ReadReceipt
					b, _ := json.Marshal(req)
					r.Body = io.NopCloser(bytes.NewReader(b))
					if mode == "confirm-failed" {
						writeAPIError(w, ErrConflict)
						return
					}
					if mode == "confirm-lost" {
						svc.ServeHTTP(httptest.NewRecorder(), r)
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
				}
				svc.ServeHTTP(w, r)
			}))
			defer server.Close()
			env := cliToolEnv(server.URL, svc, MemberState{AgentID: h.id.AgentID, MembershipID: h.id.MembershipID})
			args := []string{"inbox", "read", "--limit", "1"}
			if mode == "peek" {
				args = append(args, "--peek")
			}
			var stdout, stderr bytes.Buffer
			var err error
			if mode == "short" || mode == "broken" {
				err = runCLI(h.ctx, args, failedOutput{short: mode == "short"}, &stderr, env)
			} else {
				err = runCLI(h.ctx, args, &stdout, &stderr, env)
			}
			status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
			wantThrough, wantConfirms := uint64(0), 0
			switch mode {
			case "complete":
				wantThrough, wantConfirms = 3, 3
			case "confirm-lost":
				wantThrough, wantConfirms = 1, 1
			case "confirm-failed":
				wantConfirms = 1
			}
			if status.ConsumedThrough != wantThrough || confirms != wantConfirms {
				t.Fatal(status, confirms, err)
			}
			if mode == "complete" || mode == "peek" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("failure was hidden")
			}
			if strings.HasPrefix(mode, "confirm-") && (!strings.Contains(err.Error(), "confirmation failed or uncertain") || receipt == "" || !strings.Contains(err.Error(), receipt) || !strings.Contains(err.Error(), "turn=1")) {
				t.Fatal("missing recovery details", err)
			}
			decoder := json.NewDecoder(&stdout)
			pages := 0
			for decoder.More() {
				var page map[string]any
				if err := decoder.Decode(&page); err != nil {
					t.Fatal(err)
				}
				pages++
				if _, exists := page["batch"].(map[string]any)["receipt"]; exists {
					t.Fatal("CLI exposed internal receipt")
				}
				entry := page["entries"].([]any)[0].(map[string]any)
				if _, exists := entry["membership_id"]; exists || entry["item"] == nil {
					t.Fatal("wrong entry projection", entry)
				}
			}
			if mode == "complete" && pages != 3 {
				t.Fatal("pagination changed", pages)
			}
		})
	}
}

func TestAgentRoomReadHidesOnlyCLIReceipt(t *testing.T) {
	page := ReadPage{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, MembershipID: "membership",
		Revision: 7, SnapshotSeq: 19, FromExclusive: 17, ScannedThrough: 19, NextAfter: 19, Remaining: 2, UnreadCount: 2,
		ReadReceipt: "private-receipt", ExpiresAt: time.Now().UTC(), Messages: []RoomMessage{{Seq: 19, Author: Author{Kind: "human", ID: "operator"}, Text: "正文", ReplyTo: 18}}}
	for _, agent := range []bool{false, true} {
		t.Run(fmt.Sprint(agent), func(t *testing.T) {
			confirms := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/read/confirm" {
					var req apiRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ReadReceipt != page.ReadReceipt {
						t.Error("confirmation lost receipt", req, err)
					}
					confirms++
					_ = json.NewEncoder(w).Encode(ReadConfirmation{})
					return
				}
				_ = json.NewEncoder(w).Encode(page)
			}))
			defer server.Close()
			req := apiRequest{Caller: humanCaller}
			if agent {
				req.Caller = agentCaller("A", "membership")
			}
			var stdout bytes.Buffer
			if err := readCLI(context.Background(), cliClient{endpoint: server.URL, http: server.Client()}, req, &stdout, true); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := readViewJSON(t, page)
			if agent {
				delete(want, "revision")
				delete(want, "read_receipt")
				delete(want, "expires_at")
			}
			assertReadViewEqual(t, page, want, got)
			if confirms != 1 {
				t.Fatal("confirmation skipped", confirms)
			}
		})
	}
}
