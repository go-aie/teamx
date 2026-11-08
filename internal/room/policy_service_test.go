package room

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPolicyHTTPRejectsRemovedFields(t *testing.T) {
	h, id, svc := policyTestService(t)
	source := roomCall[PostReceipt](t, h, h.post(humanCaller, "http-source", "记住以后先给结论"))
	before, err := h.store.readPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []bool{false, true} {
		for _, field := range []string{"kind", "scope", "scope_from", "actions", "conditions", "exceptions"} {
			for _, value := range []any{nil, "rule", map[string]any{}} {
				req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID}
				key, path := "policy", "policy."+field
				body := map[string]any{"id": "http-rule", "status": "active", "text": "先给结论", "source_text": "以后先给结论"}
				if agent {
					req.Caller, req.AgentID, req.MembershipID = agentCallerIdentity(id), "", ""
					key, path = "conversation_policy", "body."+field
					body = map[string]any{"type": "save", "id": "http-rule", "source_seq": source.Seq, "turn_number": 1, "basis": "明确长期要求", "body": map[string]any{"text": "先给结论"}}
					if field == "scope_from" {
						body[field] = value
						path = field
					} else {
						body["body"].(map[string]any)[field] = value
					}
				} else {
					body[field] = value
				}
				encoded, _ := json.Marshal(req)
				var wire map[string]any
				if err := json.Unmarshal(encoded, &wire); err != nil {
					t.Fatal(err)
				}
				wire[key] = body
				encoded, _ = json.Marshal(wire)
				w := httptest.NewRecorder()
				svc.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/policy/update", bytes.NewReader(encoded)))
				var failure apiError
				if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != http.StatusBadRequest || failure.Field != path {
					t.Fatal(agent, field, value, w.Code, w.Body.String(), err)
				}
				assertPolicyUnchanged(t, h.store, id, before)
			}
		}
	}
}

func TestPolicyHTTPInvalidSourceDiagnostic(t *testing.T) {
	h, id, svc := policyTestService(t)
	before, err := h.store.readPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	q := agentPolicyChange{Type: "save", ID: "style", SourceSeq: 999, TurnNumber: 1,
		Basis: "真实来源", Body: &agentPolicyBody{Text: "简短中文"}}
	status, failure := diagnosticHTTP(t, svc, apiRequest{ServiceInfo: svc.info,
		Caller: agentCallerIdentity(id), ConversationPolicy: &q}, "/v1/policy/update")
	if status != http.StatusForbidden || failure.Code != "identity" || failure.Field != "source_seq" ||
		strings.Contains(failure.Hint, "member binding") {
		t.Fatal(status, failure)
	}
	assertPolicyUnchanged(t, h.store, id, before)
}

func TestPolicyServiceReplayReturnsHistoricalRecordWithCurrentContext(t *testing.T) {
	h, id, svc := policyTestService(t)
	source := roomCall[PostReceipt](t, h, h.post(humanCaller, "suggest", "这次请用简短中文回复"))
	q := agentPolicyChange{Type: "suggest", ID: "language", SourceSeq: source.Seq, TurnNumber: 1,
		Body: &agentPolicyBody{Text: "简短中文回复\n保留必要限制"}, Basis: "本次偏好只支持建议"}
	update := func(q agentPolicyChange) agentPolicyUpdateResult {
		t.Helper()
		got, err := svc.changeAgentPolicy(h.ctx, id, agentCallerIdentity(id), q)
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := h.store.readPolicy(id)
		if err != nil {
			t.Fatal(err)
		}
		var saved PolicyRevision
		for _, r := range persisted.History {
			if r.ID == q.ID && r.Version == q.Version+1 {
				saved = r
			}
		}
		if saved.Version == 0 || !reflect.DeepEqual(got.Record, saved) {
			t.Fatalf("receipt must contain the exact persisted revision: %+v / %+v", got.Record, saved)
		}
		if got.Revision != persisted.Revision || !reflect.DeepEqual(got.Effective, persisted.Effective) || got.PolicyToken == "" {
			t.Fatal("receipt does not describe the current effective policy", got, persisted)
		}
		return got
	}
	suggested := update(q)
	if len(suggested.Effective) != 0 || suggested.Record.Status != "suggested" || suggested.Record.ID != q.ID || suggested.Record.Version != 1 || suggested.Record.Text != q.Body.Text || suggested.Record.Source.Text != "这次请用简短中文回复" {
		t.Fatal(suggested)
	}
	if retry := update(q); !reflect.DeepEqual(retry, suggested) {
		t.Fatal("identical save was not reused", retry)
	}
	receipt, err := svc.publishPolicySuggestion(h.ctx, id, 1, suggested.Record.ID, suggested.Record.Version)
	if err != nil {
		t.Fatal(err)
	}
	answer := roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "confirm", Text: "以后就这样", ReplyTo: receipt.Seq})
	confirm := q
	confirm.Version, confirm.Revision, confirm.SourceSeq = 1, 1, answer.Seq
	confirm.Type, confirm.Body, confirm.ProposalSeq, confirm.Basis = "confirm", nil, receipt.Seq, "确认准确的已发布建议"
	active := update(confirm)
	if active.Record.Status != "active" || active.Record.Version != 2 || len(active.Effective) != 1 {
		t.Fatal(active)
	}
	revoke := confirm
	revoke.Version, revoke.Revision = 2, 2
	revoke.Type, revoke.ProposalSeq, revoke.Basis = "revoke", 0, "明确撤销"
	revoke.SourceSeq = roomCall[PostReceipt](t, h, h.post(humanCaller, "revoke", "撤销这条偏好")).Seq
	revoked := update(revoke)
	if revoked.Record.Status != "revoked" || revoked.Record.Version != 3 || len(revoked.Effective) != 0 {
		t.Fatal(revoked)
	}
	// Response recovery after history clear/restart returns the OLD request's
	// saved record, alongside the CURRENT policy revision/token/effective set.
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: revoke.SourceSeq})
	h.reload(t, true)
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id,
		TurnNumber: 2, LastOfferedUnreadSeq: revoke.SourceSeq,
		Turn: &TurnRecord{Number: 2, Status: "running", SnapshotSeq: revoke.SourceSeq, OfferedUnreadSeq: revoke.SourceSeq}}); err != nil {
		t.Fatal(err)
	}
	svc.store, svc.room = h.store, h.addr
	q.TurnNumber = 2
	reused := update(q)
	if !reflect.DeepEqual(reused.Record, suggested.Record) || reused.Revision != 3 || reused.CurrentTurn != 2 || reused.PolicyToken != revoked.PolicyToken || len(reused.Effective) != 0 {
		t.Fatal("replay fabricated current record or reactivated policy", reused)
	}
	confirm.TurnNumber = 2
	if reused := update(confirm); !reflect.DeepEqual(reused.Record, active.Record) || len(reused.Effective) != 0 {
		t.Fatal(reused)
	}
}

func TestPolicyHTTPMemberSelection(t *testing.T) {
	_, id, svc := policyTestService(t)
	change := policyRule()
	req := apiRequest{ServiceInfo: svc.info, Caller: humanCaller,
		AgentID: id.AgentID, MembershipID: id.MembershipID, Policy: &change}
	call := func() *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/policy/update", bytes.NewReader(data)))
		return w
	}
	w := call()
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body)
	}
	req.Policy = nil
	req.Caller = agentCallerIdentity(id)
	status, failure := diagnosticHTTP(t, svc, req, "/v1/policy/read")
	if status != http.StatusForbidden || failure.Code != "identity" {
		t.Fatal(status, failure)
	}
	req.AgentID, req.MembershipID = "", ""
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/policy/read", bytes.NewReader(data)))
	var view agentPolicyView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != http.StatusOK ||
		len(view.Effective) != 1 || view.Effective[0].ID != change.ID {
		t.Fatal(w.Code, w.Body, err)
	}
}
