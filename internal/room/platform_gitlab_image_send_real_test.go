//go:build e2e && darwin

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// The private manifest freezes the operator-reviewed write scope. Credentials
// are selected separately and never copied into evidence or model instructions.
type gitlabImageSendRealInput struct {
	Origin       string `json:"origin"`
	Username     string `json:"username"`
	UserID       int64  `json:"user_id"`
	Project      string `json:"project"`
	ProjectID    int64  `json:"project_id"`
	MRID         int64  `json:"mr_id"`
	MRIID        int64  `json:"mr_iid"`
	MRURL        string `json:"mr_url"`
	DiscussionID string `json:"discussion_id"`
	Caption      string `json:"caption"`
	Images       []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"images"`
	DispatchDir string `json:"dispatch_dir"`
}

func gitlabImageSendRealSample(t *testing.T) (gitlabImageSendRealInput, [][]byte) {
	t.Helper()
	path := os.Getenv("TEAMX_GITLAB_IMAGE_SEND_INPUT")
	var q gitlabImageSendRealInput
	raw, err := os.ReadFile(path)
	if err != nil || strictToolJSON(raw, &q) != nil || !strings.HasPrefix(q.Origin, "https://") ||
		q.Username == "" || q.UserID <= 0 || !workspaceProjectPath(q.Project) || q.ProjectID <= 0 ||
		q.MRID <= 0 || q.MRIID <= 0 || q.MRURL != fmt.Sprintf("%s/%s/-/merge_requests/%d", q.Origin, q.Project, q.MRIID) ||
		!handlingText(q.Caption) || len(q.Images) != 2 || !filepath.IsAbs(q.DispatchDir) {
		t.Fatal("reviewed exact account, project/MR, caption, two originals and persistent dispatch directory required")
	}
	images := make([][]byte, len(q.Images))
	total := 0
	for i, image := range q.Images {
		if !filepath.IsLocal(image.Path) {
			t.Fatal("image must be local to reviewed manifest")
		}
		images[i], err = os.ReadFile(filepath.Join(filepath.Dir(path), image.Path))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(images[i])) != image.SHA256 {
			t.Fatal("reviewed original missing or changed", i+1)
		}
		if _, err := attachment.ValidateImage(images[i], attachment.ImageLimits{MaxBytes: 2 << 20, MaxPixels: 16 << 20}); err != nil {
			t.Fatal("unsupported original", i+1)
		}
		total += len(images[i])
	}
	if total > 4<<20 || q.Images[0].SHA256 == q.Images[1].SHA256 {
		t.Fatal("two distinct, bounded originals required")
	}
	return q, images
}

func gitlabImageSendPreflight(t *testing.T, ctx context.Context, q gitlabImageSendRealInput) gitlab.ReadAPI {
	t.Helper()
	file, c := selectedRealOAuthSnapshot(t, q.Origin)
	// A copied active access grant may be used without rotating the live grant.
	// Neither this reader nor the acceptance transport permits OAuth POSTs.
	if c.Validation.UserID != q.UserID || c.Validation.Username != q.Username ||
		!file.OAuthCredentials[0].ExpiresAt.After(time.Now().Add(15*time.Minute)) {
		t.Fatal("selected account differs or access grant has less than 15 minutes left; no refresh attempted")
	}
	read := gitlab.NewReadAPI(q.Origin, platform.NewSecret(file.OAuthCredentials[0].AccessToken), nil)
	get := func(path string, out any) {
		r, err := read(ctx, path, nil, nil, 1<<20)
		if err != nil || json.Unmarshal(r.Data, out) != nil {
			t.Fatal("real read-only preflight failed", path)
		}
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	var project struct {
		ID   int64  `json:"id"`
		Path string `json:"path_with_namespace"`
	}
	var mr struct {
		ID        int64  `json:"id"`
		IID       int64  `json:"iid"`
		ProjectID int64  `json:"project_id"`
		URL       string `json:"web_url"`
	}
	get("/user", &user)
	get(fmt.Sprintf("/projects/%d", q.ProjectID), &project)
	get(fmt.Sprintf("/projects/%d/merge_requests/%d", q.ProjectID, q.MRIID), &mr)
	if user.ID != q.UserID || user.Username != q.Username || project.ID != q.ProjectID || project.Path != q.Project ||
		mr.ID != q.MRID || mr.IID != q.MRIID || mr.ProjectID != q.ProjectID || mr.URL != q.MRURL {
		t.Fatal("real account/project/MR mismatch; no writes")
	}
	if q.DiscussionID != "" {
		var d referenceLiveDiscussion
		get(fmt.Sprintf("/projects/%d/merge_requests/%d/discussions/%s", q.ProjectID, q.MRIID, q.DiscussionID), &d)
		if d.ID != q.DiscussionID || d.Individual || len(d.Notes) == 0 {
			t.Fatal("exact native discussion missing; no fallback")
		}
	}
	return read
}

func TestRealGitlabImageSendPreflight(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_IMAGE_SEND_PREFLIGHT") != "1" {
		t.Skip("explicit read-only preflight and reviewed manifest required")
	}
	q, _ := gitlabImageSendRealSample(t)
	gitlabImageSendPreflight(t, t.Context(), q)
	t.Log("exact account/project/MR/thread verified; uploads=0 comments=0")
}

// Real model, ordinary scheduler and the same managed implementation as the
// fixture tests. Two notes / three uploads at most; no native proxy fallback,
// OAuth refresh, retry, or replacement action can bypass the persistent budget.
func TestRealGitlabImageAgentSend(t *testing.T) {
	testRealGitlabImageAgentSend(t, false)
}

func TestRealGitlabActionURLAgentSend(t *testing.T) {
	testRealGitlabImageAgentSend(t, true)
}

func testRealGitlabImageAgentSend(t *testing.T, urlMode bool) {
	optIn := "TEAMX_GITLAB_IMAGE_SEND_REAL"
	if urlMode {
		optIn = "TEAMX_GITLAB_ACTION_URL_SEND_REAL"
	}
	if os.Getenv(optIn) != "1" {
		t.Skip("explicit two-comment/three-upload authorization and reviewed manifest required")
	}
	note := os.Getenv("TEAMX_GITLAB_ACTION_URL_NOTE")
	if urlMode {
		n, err := strconv.ParseInt(note, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != note {
			t.Fatal("explicit canonical note ID for reviewed URL required")
		}
	}
	maximumUploads := 3
	if urlMode {
		maximumUploads = 2
	}
	q, images := gitlabImageSendRealSample(t)
	operator := Caller{Author: Author{Kind: "human", ID: humanOperator()}}
	if q.DiscussionID == "" {
		t.Fatal("reviewed existing native discussion required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	read := gitlabImageSendPreflight(t, ctx, q)
	cfg, entry, credential := inputAcceptanceModel(t)
	out := imageRealOutput(t)
	if err := os.Mkdir(q.DispatchDir, 0700); err != nil {
		t.Fatal("dispatch directory already reserved; reconcile original receipts rather than re-run")
	}
	const key = "TEAMX_GITLAB_IMAGE_SEND_MODEL_KEY"
	t.Setenv(key, credential.Value)
	runtime := *cfg.Runtime
	runtime.Model = entry.materialize()
	runtime.Model.APIKeyEnv = key
	runtime.Model.InputModalities = []string{"text"}
	audit := &policyRealAudit{Calls: map[string]int{}}
	m := directTestManager(t, filepath.Join(out, "rooms"), &runtime)
	m.maxTurns, m.config.unifiedInputs = 8, true
	report := map[string]any{"status": "failed", "platform": "real GitLab", "recorded_at": time.Now().UTC(), "manifest": q, "maximum_note_posts": 2, "maximum_uploads": maximumUploads, "model": runtime.Model.ModelID, "audit": audit, "url_locator": urlMode}
	defer imageRealReport(t, out, report)
	server := browserTestServer(t, m, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tools/run" {
			http.Error(w, "native proxy disabled for managed acceptance", http.StatusForbidden)
			return
		}
		audit.serve(m).ServeHTTP(w, r)
	}))
	defer server.Close()
	m.config.endpoint, m.config.cliPath = server.URL, productionFixtureCLI(t)
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
	ensureAssistant(t, m)
	room := assistantSummary(t, m)
	svc := eventLiveService(t, &eventHarness{m: m, room: room})
	_, snapshot := readManagedRoom(t, m, room)
	member := snapshot.Members[0]
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	report["member"] = id
	connection := realOAuthPlatform(t, m, q.Origin)
	bindCLITool(t, m, room.RoomIdentity, member, connection, "gitlab", 0)
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id}); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	m.bindPlatformReferences(svc)
	var mu sync.Mutex
	uploads, posts := 0, 0
	requests := []map[string]any{}
	report["http"] = &requests
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme+"://"+r.URL.Host != q.Origin || r.Header.Get("Authorization") == "" || r.Header.Get("Cookie") != "" {
			return nil, platform.ErrCredential
		}
		mu.Lock()
		if r.Method == http.MethodPost {
			base := fmt.Sprintf("/api/v4/projects/%d", q.ProjectID)
			kind, n := "", 0
			switch r.URL.Path {
			case base + "/uploads":
				uploads++
				kind, n = "upload", uploads
				if uploads > maximumUploads {
					mu.Unlock()
					return nil, platform.ErrCredential
				}
			case fmt.Sprintf("%s/merge_requests/%d/notes", base, q.MRIID), fmt.Sprintf("%s/merge_requests/%d/discussions/%s/notes", base, q.MRIID, q.DiscussionID):
				posts++
				kind, n = "note", posts
				if posts > 2 {
					mu.Unlock()
					return nil, platform.ErrCredential
				}
			default:
				mu.Unlock()
				return nil, platform.ErrCredential
			}
			if err := gitlabImageReserve(filepath.Join(q.DispatchDir, fmt.Sprintf("%s-%d", kind, n)), []byte(r.URL.Path)); err != nil {
				mu.Unlock()
				return nil, err
			}
		} else if r.Method != http.MethodGet {
			mu.Unlock()
			return nil, platform.ErrCredential
		}
		mu.Unlock()
		res, err := http.DefaultTransport.RoundTrip(r)
		row := map[string]any{"method": r.Method, "path": r.URL.Path}
		if res != nil {
			row["status"] = res.StatusCode
		}
		mu.Lock()
		requests = append(requests, row)
		mu.Unlock()
		return res, err
	})
	refs := []gitlab.ImageRef{}
	for i, data := range images {
		imageID := attachmentTestID(i + 1)
		status, _ := attachmentUploadHTTP(t, server, svc.info, imageID, data, false)
		if status != 200 {
			t.Fatal("source original upload failed", status)
		}
		status, raw := attachmentJSON(t, server, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: operator, ClientMessageID: imageID, Text: fmt.Sprintf("GitLab 图文验收原图 %d", i+1), Attachments: []RoomAttachmentInput{{ID: imageID}}})
		var receipt PostReceipt
		if status != 200 || json.Unmarshal(raw, &receipt) != nil {
			t.Fatal("source publication failed", status)
		}
		refs = append(refs, gitlab.ImageRef{MessageSeq: receipt.Seq, AttachmentID: imageID})
	}
	// Existing-target acceptance setup: use the admitted read resolver and save
	// native identities before Agent tasks. No new URL/native-ID Agent entry.
	targets := []string{"", ""}
	if !urlMode {
		session, err := m.prepareGitlabReferenceAction(ctx, id, platform.AccessContext{Receiver: platform.AccountIdentity{ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(q.UserID)}}})
		if err != nil {
			t.Fatal(err)
		}
		for i, discussion := range []string{"", q.DiscussionID} {
			target := platform.TargetIdentity{IdentityScope: session.Access.Account.IdentityScope, Containers: []platform.NativeIdentity{{Type: "Project", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(q.ProjectID)}}}, Object: platform.NativeIdentity{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(q.MRID)}}}
			if discussion != "" {
				target.Discussion = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: discussion}
			}
			alias := target
			alias.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:iid", Value: fmt.Sprint(q.MRIID)}
			resolution, err := session.Capabilities.Resolve(ctx, platform.TargetLocator{Candidate: &target, VerifiedAliases: []platform.TargetIdentity{alias}}, session.Access)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := svc.store.saveActionTarget(id, resolution, session.Access)
			if err != nil {
				t.Fatal(err)
			}
			targets[i] = ref
		}
		session.Close()
	}
	if err := svc.store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	closeService(t, svc)
	m.mu.Lock()
	delete(m.runtimes, room.RoomIdentity)
	m.mu.Unlock()
	svc = openRoomOK(t, m, room, "real-gitlab-images-open")
	type approvedCase struct {
		ID, Body, Target string
		Operation        platform.Operation
		Refs             []gitlab.ImageRef
		Hashes           []string
	}
	cases := []approvedCase{
		{"real-gitlab-images-comment", q.Caption, targets[0], platform.OperationObjectComment, refs, []string{q.Images[0].SHA256, q.Images[1].SHA256}},
		{"real-gitlab-images-reply", "", targets[1], platform.OperationDiscussionReply, refs[1:], []string{q.Images[1].SHA256}},
	}
	if urlMode {
		cases = []approvedCase{
			{"real-gitlab-url-comment", q.Caption, q.MRURL, platform.OperationObjectComment, refs[:1], []string{q.Images[0].SHA256}},
			{"real-gitlab-url-reply", "", q.MRURL + "#note_" + note, platform.OperationDiscussionReply, refs[1:], []string{q.Images[1].SHA256}},
		}
		report["target_setup"] = "no pre-stored refs, native IDs or notifications; Agent receives full URLs only"
	}
	prepare := m.referenceExecutors["gitlab"]
	m.referenceExecutors["gitlab"] = func(ctx context.Context, member MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		session, err := prepare(ctx, member, old)
		if err != nil {
			return session, err
		}
		for op, cap := range session.Capabilities.Actions {
			send := cap.Execute
			cap.Execute = func(ctx context.Context, request platform.TargetActionRequest, access platform.AccessContext) (platform.TargetActionResult, error) {
				for _, c := range cases {
					input, err := gitlab.DecodeImageActionInput(request.Input)
					if member == id && request.Operation == c.Operation && input.Body == c.Body && reflect.DeepEqual(input.Images, c.Refs) && err == nil &&
						access.Account.ID.Value == fmt.Sprint(q.UserID) && request.Target.Object.ID.Value == fmt.Sprint(q.MRID) &&
						len(request.Target.Containers) == 1 && request.Target.Containers[0].ID.Value == fmt.Sprint(q.ProjectID) &&
						(request.Operation != platform.OperationDiscussionReply || request.Target.Discussion.Value == q.DiscussionID) {
						if err := gitlabImageReserve(filepath.Join(q.DispatchDir, c.ID), request.Input); err != nil {
							return platform.TargetActionResult{}, err
						}
						return send(ctx, request, access)
					}
				}
				return platform.TargetActionResult{}, ErrConflict
			}
			session.Capabilities.Actions[op] = cap
		}
		return session, nil
	}
	rows := []map[string]any{}
	receiptIDs := map[string]string{}
	report["cases"] = &rows
	for i, c := range cases {
		text := fmt.Sprintf("使用 RussellLuo，把上面的两张原图和以下说明一起评论到 %s（已有托管目标 %s），只发一条评论。说明原文：\n%s", q.MRURL, c.Target, c.Body)
		if i == 1 {
			text = fmt.Sprintf("使用 RussellLuo，只把第二张原图回复到已有讨论 %s（MR %s，托管目标 %s），不带说明文字，只发一条回复。", q.DiscussionID, q.MRURL, c.Target)
		}
		if urlMode {
			text = fmt.Sprintf("使用 RussellLuo，把上面的第一张原图和以下说明评论到 %s，只发一条评论。说明原文：\n%s", c.Target, c.Body)
			if i == 1 {
				text = "使用 RussellLuo，只把第二张原图回复到 " + c.Target + "，不带说明文字，只发一条回复。"
			}
		} else {
			text += " 稳定动作 ID 为 " + c.ID + "。"
		}
		posted, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: operator, ClientMessageID: c.ID, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
				t.Fatal(err)
			}
		}
		status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
			return !s.Active && (s.Agent.TurnNumber >= uint64(i+1) || s.Paused && s.Error != "")
		})
		if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
			t.Fatal("real Agent did not complete; retain original receipts", status.Error)
		}
		actions, err := svc.store.queryActions(id, c.ID, "", "")
		if urlMode {
			all, e := svc.store.queryActions(id, "", "", "")
			if e != nil || len(all) != i+1 {
				t.Fatal("unexpected action count", e)
			}
			actions = nil
			err = e
			for _, action := range all {
				if action.Request.Operation == c.Operation {
					actions = append(actions, action)
				}
			}
		}
		if err != nil || len(actions) != 1 {
			t.Fatal("missing complete stable action; do not supplement with manual execute")
		}
		a := actions[0]
		if urlMode {
			receiptIDs[c.ID] = a.Request.ID
			c.ID = a.Request.ID
		}
		row := map[string]any{"id": c.ID, "source_seq": posted.Seq, "action": a}
		rows = append(rows, row)
		if a.Status != "succeeded" || a.Attempts != 1 || a.Result == nil || !a.Result.ResultID.Valid() || !a.Spec.DirectRoomInstructionOnly || a.Spec.Retry.MaxAttempts != 1 || a.Spec.Retry.RetryUnknown {
			t.Fatal("real action not independently verified; stop and read original receipt", a.Status)
		}
		row["native_note"] = verifyRealGitlabImageNote(t, ctx, q, c.Body, c.Hashes, a, read)
		assertActionCallsSettled(t, m)
		assertActionImagePinsReleased(t, svc.store)
	}
	audit.mu.Lock()
	turns := append([]policyRealTurn(nil), audit.Turns...)
	audit.mu.Unlock()
	turns = gitlabImageFullToolTrace(t, svc, id, turns)
	report["costs"] = gitlabImageAgentCosts(t, turns)
	report["full_turns"] = turns
	if uploads != maximumUploads || posts != 2 {
		t.Fatal("unexpected dispatch count", uploads, posts)
	}
	// Close/reopen the actual persisted service. Receipt reads must work even
	// with both source preparation and every network request made unavailable.
	closeService(t, svc)
	m.mu.Lock()
	delete(m.runtimes, room.RoomIdentity)
	m.mu.Unlock()
	m.referenceExecutors["gitlab"] = func(context.Context, MemberIdentity, platform.AccessContext) (referenceActionSession, error) {
		t.Error("receipt query entered preparer")
		return referenceActionSession{}, ErrConflict
	}
	m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		t.Error("receipt query attempted network")
		return nil, ErrConflict
	})
	svc = openRoomOK(t, m, room, "real-gitlab-images-recovery")
	for _, c := range cases {
		if urlMode {
			c.ID = receiptIDs[c.ID]
		}
		detail := gitlabImageReceiptCLI(t, m, svc, id, server.URL, []string{"actions", "read", "--id", c.ID})
		if !bytes.Contains(detail, []byte(`"succeeded"`)) || !bytes.Contains(detail, []byte(c.Refs[0].AttachmentID)) {
			t.Fatal("reopened original receipt unavailable")
		}
	}
	report["recovery"] = "service reopened; original CLI receipts read with preparer/network disabled"
	report["status"] = "passed"
}

func gitlabImageReceiptCLI(t *testing.T, m *roomManager, svc *service, id MemberIdentity, endpoint string, args []string) []byte {
	t.Helper()
	token, revoke, err := m.config.access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	command := exec.CommandContext(t.Context(), productionFixtureCLI(t), args...)
	for _, entry := range os.Environ() {
		if !isRoomToolEnv(entry) {
			command.Env = append(command.Env, entry)
		}
	}
	env := cliToolEnv(endpoint, svc, MemberState{AgentID: id.AgentID, MembershipID: id.MembershipID})
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		command.Env = append(command.Env, key+"="+env(key))
	}
	command.Env = append(command.Env, "TEAMX_EXECUTION_TOKEN="+token)
	data, err := command.CombinedOutput()
	if err != nil || !json.Valid(data) {
		t.Fatal("authenticated receipt CLI failed", err, string(data))
	}
	return data
}

func gitlabImageReserve(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func verifyRealGitlabImageNote(t *testing.T, ctx context.Context, q gitlabImageSendRealInput, body string, hashes []string, a referenceAction, read gitlab.ReadAPI) json.RawMessage {
	t.Helper()
	path := fmt.Sprintf("/projects/%d/merge_requests/%d/notes/%s", q.ProjectID, q.MRIID, a.Result.ResultID.Value)
	r, err := read(ctx, path, nil, nil, 1<<20)
	var note struct {
		ID     int64  `json:"id"`
		Body   string `json:"body"`
		System bool   `json:"system"`
		MRID   int64  `json:"noteable_id"`
		MRIID  int64  `json:"noteable_iid"`
		Type   string `json:"noteable_type"`
		Author struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"author"`
	}
	if err != nil || json.Unmarshal(r.Data, &note) != nil || fmt.Sprint(note.ID) != a.Result.ResultID.Value ||
		note.System || note.MRID != q.MRID || note.MRIID != q.MRIID || note.Type != "MergeRequest" || note.Author.ID != q.UserID || note.Author.Username != q.Username {
		t.Fatal("independent real note/account/object verification failed")
	}
	source := platform.SourceRef{Provider: "gitlab", Instance: q.Origin, Project: q.Project, ProjectID: q.ProjectID}
	refs := gitlab.FindNotificationImages(note.Body, source, 4)
	if len(refs.Images) != len(hashes) || !strings.HasPrefix(note.Body, body) {
		t.Fatal("real complete body/image count differs")
	}
	parts := []string{}
	if body != "" {
		parts = append(parts, body)
	}
	for i, image := range refs.Images {
		if !image.Supported {
			t.Fatal("real image reference unsupported")
		}
		data, _, err := gitlab.DownloadUpload(ctx, q.Origin, platform.NewSecret(selectedRealAccessToken(t, q.Origin)), image.Upload, nil)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != hashes[i] {
			t.Fatal("downloaded original SHA256/order differs", i+1)
		}
		parts = append(parts, fmt.Sprintf("![image-%d](%s)", i+1, image.Path))
	}
	if note.Body != strings.Join(parts, "\n\n") {
		t.Fatal("real Markdown/body was normalized or changed")
	}
	if a.Request.Operation == platform.OperationDiscussionReply {
		r, err := read(ctx, fmt.Sprintf("/projects/%d/merge_requests/%d/discussions/%s", q.ProjectID, q.MRIID, q.DiscussionID), nil, nil, 1<<20)
		var d referenceLiveDiscussion
		found := false
		if err == nil && json.Unmarshal(r.Data, &d) == nil && d.ID == q.DiscussionID && !d.Individual {
			for _, n := range d.Notes {
				found = found || n.ID == note.ID && n.Body == note.Body && n.Author.ID == q.UserID
			}
		}
		if !found {
			t.Fatal("real reply not in exact thread")
		}
	}
	return r.Data
}

// Keep the reservation's durability contract exercised without any opt-in,
// credentials, model or network. A crash/restart cannot reuse a consumed slot.
func TestGitlabImageDispatchReservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note-1")
	if err := gitlabImageReserve(path, []byte("intent")); err != nil {
		t.Fatal(err)
	}
	if err := gitlabImageReserve(path, []byte("replacement")); err == nil {
		t.Fatal("consumed native write slot reused")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "intent" {
		t.Fatal("original intent replaced", err)
	}
}
