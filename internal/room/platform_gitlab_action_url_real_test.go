//go:build e2e && darwin

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Reconcile the original two successful notes even if a later Agent cost audit
// failed. This test cannot dispatch: every preparer and provider call is closed.
func TestRealGitlabActionURLReceiptRecovery(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_ACTION_URL_RECOVERY_REAL") != "1" {
		t.Skip("explicit access-only original report required")
	}
	q, _ := gitlabImageSendRealSample(t)
	path := os.Getenv("TEAMX_GITLAB_IMAGE_SEND_REPORT")
	var original struct {
		Manifest gitlabImageSendRealInput `json:"manifest"`
		Member   MemberIdentity           `json:"member"`
		URL      bool                     `json:"url_locator"`
		Cases    []struct {
			ID     string          `json:"id"`
			Action referenceAction `json:"action"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(path)
	if err != nil || !filepath.IsAbs(path) || json.Unmarshal(data, &original) != nil || !original.URL || len(original.Cases) != 2 || !bytes.Equal(mustContractJSON(original.Manifest), mustContractJSON(q)) {
		t.Fatal("unchanged URL sample and two original actions required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "original_report": path, "remote_writes": 0, "scope": "independent process; original URL targets and same-ID CLI recovery; independent note/download verification"}
	defer imageRealReport(t, out, report)
	read := gitlabImageSendPreflight(t, t.Context(), q)
	m := directTestManager(t, filepath.Join(filepath.Dir(path), "rooms"), nil)
	server := browserTestServer(t, m)
	defer server.Close()
	m.config.endpoint, m.config.cliPath = server.URL, productionFixtureCLI(t)
	room := assistantSummary(t, m)
	if room.RoomIdentity != original.Member.RoomIdentity {
		t.Fatal("different original Room")
	}
	svc := eventLiveService(t, &eventHarness{m: m, room: room})
	m.bindPlatformReferences(svc)
	m.referenceExecutors["gitlab"] = func(context.Context, MemberIdentity, platform.AccessContext) (referenceActionSession, error) {
		t.Error("receipt recovery entered preparer")
		return referenceActionSession{}, ErrConflict
	}
	m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		t.Error("receipt recovery attempted provider request")
		return nil, ErrConflict
	})
	proof := []map[string]any{}
	for i, c := range original.Cases {
		a := c.Action
		if a.Request.ID != c.ID || a.Status != "succeeded" || a.Attempts != 1 || a.Result == nil {
			t.Fatal("original action not verified")
		}
		body, hashes := q.Caption, []string{q.Images[0].SHA256}
		if i == 1 {
			body, hashes = "", []string{q.Images[1].SHA256}
		}
		native := verifyRealGitlabImageNote(t, t.Context(), q, body, hashes, a, read)
		detail := gitlabImageReceiptCLI(t, m, svc, original.Member, server.URL, []string{"actions", "read", "--id", c.ID})
		wire := string(mustContractJSON(gitlabAgentWire(a.Request)))
		for range 2 {
			receipt := gitlabImageReceiptCLI(t, m, svc, original.Member, server.URL, []string{"actions", "execute", "--request", wire})
			if !bytes.Contains(receipt, []byte(`"succeeded"`)) || !bytes.Contains(receipt, []byte(a.Result.ResultID.Value)) {
				t.Fatal("same-ID receipt changed")
			}
		}
		svc.store.mu.Lock()
		target, e := svc.store.loadActionTargetLocked(original.Member, a.Request.TargetRef)
		svc.store.mu.Unlock()
		if e != nil || !sameNativeTarget(target.Target, a.Target) {
			t.Fatal("URL ref lost on restart", e)
		}
		proof = append(proof, map[string]any{"id": c.ID, "native_note": native, "receipt": json.RawMessage(detail), "same_id_rechecks": 2, "image_sha256": hashes, "target_ref": a.Request.TargetRef})
	}
	report["status"], report["receipts"], report["recovery_provider_requests"] = "passed", proof, 0
}

// Access-only acceptance: a copied active grant, a fresh member and the formal
// CLI/manager resolver. The transport rejects all writes, refresh and redirects.
func TestRealGitlabActionURLReadOnly(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_ACTION_URL_READ_REAL") != "1" {
		t.Skip("opt-in access-only real URL resolution")
	}
	q, _ := gitlabImageSendRealSample(t)
	note := os.Getenv("TEAMX_GITLAB_ACTION_URL_NOTE")
	n, err := strconv.ParseInt(note, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != note {
		t.Fatal("explicit canonical note ID for read-only URL sample required")
	}
	file, _ := selectedRealOAuthSnapshot(t, q.Origin)
	if !file.OAuthCredentials[0].ExpiresAt.After(time.Now().Add(time.Minute)) {
		t.Fatal("current access grant expired or expires within a minute; no refresh attempted")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "remote_writes": 0, "scope": "fresh member; formal CLI/HTTP/manager; copied access grant, GET only"}
	defer imageRealReport(t, out, report)
	m, room, svc, _, members := cliToolFixture(t, "exit 99\n")
	connection := realOAuthPlatform(t, m, q.Origin)
	if connection.Validation.UserID != q.UserID || connection.Validation.Username != q.Username {
		t.Fatal("selected account differs from reviewed sample")
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], connection, "gitlab", 0)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: members[0].AgentID, MembershipID: members[0].MembershipID}
	m.bindPlatformReferences(svc)
	posted, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: room.RoomIdentity, Caller: humanCaller, ClientMessageID: "url-read-only", Text: "只核实这些 GitLab 目标，不发送评论或上传图片。"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: posted.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: posted.Seq, OfferedUnreadSeq: posted.Seq}}); err != nil {
		t.Fatal(err)
	}
	requests := []map[string]any{}
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Scheme+"://"+r.URL.Host != q.Origin || !strings.HasPrefix(r.URL.Path, "/api/v4/") {
			return nil, platform.ErrCredential
		}
		res, err := http.DefaultTransport.RoundTrip(r)
		row := map[string]any{"method": r.Method, "path": r.URL.EscapedPath()}
		if res != nil {
			row["status"] = res.StatusCode
		}
		requests = append(requests, row)
		return res, err
	})
	server := httptest.NewServer(m)
	defer server.Close()
	f := &gitlabImageFixture{managedActionFixture: &managedActionFixture{m: m, room: room, svc: svc, connection: connection, member: members[0], id: id}, cli: contractCLI(t, server.URL, svc, id)}
	results := []agentGitlabTargetView{}
	for _, raw := range []string{q.MRURL, q.MRURL + "#note_" + note, q.MRURL + "#discussion_" + q.DiscussionID} {
		view, err := resolveGitlabURLCLI(t, f, raw)
		if err != nil || view.TargetRef == "" || view.Resolution.Status != platform.ResolutionResolved || view.Resolution.Target == nil || view.Resolution.Target.Object.ID.Value != fmt.Sprint(q.MRID) || view.Resolution.Target.IID.Value != fmt.Sprint(q.MRIID) || view.Resolution.Account.ID.Value != fmt.Sprint(q.UserID) {
			t.Fatal(view, err)
		}
		if strings.Contains(raw, "#") && view.Resolution.Target.Discussion.Value != q.DiscussionID {
			t.Fatal("wrong native thread", view)
		}
		results = append(results, view)
	}
	if results[1].TargetRef == results[2].TargetRef || results[1].Resolution.Target.Message.Value != note || results[2].Resolution.Target.Message.Valid() || results[1].Resolution.Target.Discussion != results[2].Resolution.Target.Discussion {
		t.Fatal("note focus was lost or mapped to a different discussion")
	}
	if err := m.waitPlatformCalls(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	report["status"], report["requests"], report["results"] = "passed", requests, results
}
