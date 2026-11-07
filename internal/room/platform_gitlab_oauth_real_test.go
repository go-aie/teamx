//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// This is an operator-driven acceptance test, never part of normal tests. Its
// retained private directory is a separate installation, not a copy of live
// Rooms. Application setup and login use the real UI.
// Later invocations reopen the same Store to test actual process restarts.
// No test changes expiry timestamps, issues comments, or writes repository data.
func TestRealGitLabOAuthG5(t *testing.T) {
	phase := os.Getenv("TEAMX_G5_PHASE")
	if phase == "" {
		t.Skip("set TEAMX_G5_PHASE and TEAMX_G5_ROOT; see docs/gitlab-oauth-g5-acceptance.zh.md")
	}
	if !strings.Contains("|browser|read|refresh|boundary|revoked|", "|"+phase+"|") {
		t.Fatal("unknown G5 phase")
	}
	root := os.Getenv("TEAMX_G5_ROOT")
	if !filepath.IsAbs(root) || filepath.Clean(root) == "/" {
		t.Fatal("dedicated absolute TEAMX_G5_ROOT required")
	}
	marker := filepath.Join(root, "g5-installation")
	if phase == "browser" {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			if os.Mkdir(root, 0700) != nil || os.WriteFile(marker, []byte("isolated GitLab OAuth G5 acceptance\n"), 0600) != nil {
				t.Fatal("cannot create isolated acceptance directory")
			}
		}
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "isolated GitLab OAuth G5 acceptance\n" {
		t.Fatal("refusing an unmarked directory; never use the live service data directory")
	}
	endpoint := "http://127.0.0.1:18089"
	origin := "https://git-devops.opencsg.com"
	m := testManager(t, filepath.Join(root, "data"), nil)
	m.config.endpoint = endpoint
	reportPath := filepath.Join(root, phase+"-report.json")
	report := map[string]any{"phase": phase, "status": "running", "started_at": time.Now().UTC(), "origin": origin, "endpoint": endpoint, "business_writes": 0, "dsh": "not exercised"}
	saveReport := func() {
		t.Helper()
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil || os.WriteFile(reportPath+".tmp", append(data, '\n'), 0600) != nil || os.Rename(reportPath+".tmp", reportPath) != nil {
			t.Fatal("cannot save safe G5 report")
		}
	}
	var auditMu sync.Mutex
	type requestResult struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Status int    `json:"status"`
	}
	requests := []requestResult{}
	transport := workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		// No arbitrary business write can escape this acceptance process.
		allowed := r.Method == "GET" || r.Method == "POST" && (r.URL.Path == "/oauth/token" || r.URL.Path == "/oauth/revoke")
		if r.URL.Scheme+"://"+r.URL.Host != origin || !allowed {
			return nil, platform.ErrCredential
		}
		res, err := http.DefaultTransport.RoundTrip(r)
		row := requestResult{Method: r.Method, Path: r.URL.EscapedPath()}
		if res != nil {
			row.Status = res.StatusCode
		}
		auditMu.Lock()
		requests = append(requests, row)
		auditMu.Unlock()
		return res, err
	})
	m.platformHTTP = transport
	defer func() {
		if t.Failed() {
			report["status"] = "failed"
		}
		report["finished_at"] = time.Now().UTC()
		auditMu.Lock()
		report["requests"] = append([]requestResult{}, requests...)
		auditMu.Unlock()
		saveReport()
		t.Log("safe evidence:", reportPath)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	load := func() connection.Snapshot {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		file, err := m.connections.Load()
		if err != nil {
			t.Fatal("cannot load private G5 snapshot")
		}
		return file
	}
	if phase == "browser" {
		if err := os.Remove(filepath.Join(root, "stop-browser")); err != nil && !os.IsNotExist(err) {
			t.Fatal("cannot clear the previous browser stop marker")
		}
		listener, err := net.Listen("tcp", "127.0.0.1:18089")
		if err != nil {
			t.Fatal("dedicated callback port unavailable")
		}
		server := &http.Server{Handler: m, ReadHeaderTimeout: 5 * time.Second}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		defer func() {
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(stop)
			<-done
		}()
		report["status"] = "waiting_for_browser"
		saveReport()
		t.Log("G5_BROWSER_READY", endpoint, "use Settings / G5 isolated GitLab; stop with", filepath.Join(root, "stop-browser"))
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				report["status"] = "pending_browser_evidence"
				return
			case <-ticker.C:
				file := load()
				for _, c := range file.Connections {
					report["connection"] = map[string]any{"id": c.ID, "kind": c.CredentialKind, "user_id": c.Validation.UserID, "username": c.Validation.Username}
				}
				m.mu.Lock()
				sessions := []map[string]string{}
				for _, session := range m.gitlabAuthSessions {
					sessions = append(sessions, map[string]string{"status": session.Status, "error_code": session.ErrorCode, "cleanup_status": session.Cleanup})
				}
				m.mu.Unlock()
				report["auth_sessions"] = sessions
				saveReport()
				if _, err := os.Stat(filepath.Join(root, "stop-browser")); err == nil {
					report["status"] = "stopped_inspect_connection_and_browser_evidence"
					return
				}
			}
		}
	}
	file := load()
	var record connection.Record
	for _, c := range file.Connections {
		if c.Provider == "gitlab" && c.URL == origin && c.CredentialKind == "oauth" {
			record = c
			break
		}
	}
	if record.ID == "" {
		t.Fatal("real browser login has not saved an OAuth connection")
	}
	if phase == "revoked" {
		cred, _, err := connection.OAuthRecord(file, record.CredentialID)
		if err != nil || cred.State != "logged_out" {
			t.Fatal("perform explicit UI logout first")
		}
		for _, c := range file.Connections {
			if c.CredentialID == record.CredentialID {
				if _, err := connection.ResolveAuthorization(file, c); err == nil {
					t.Fatal("shared logged-out authorization remains readable")
				}
			}
		}
		for _, c := range file.OAuthCredentials {
			if c.ID == record.CredentialID && (c.AccessToken != "" || c.RefreshToken != "") {
				t.Fatal("logged-out business material retained")
			}
		}
		report["status"] = "local_block_passed_remote_effect_unverified"
		return
	}
	account, err := connection.ResolveAuthorization(file, record)
	if err != nil || account.UserID != 13 {
		t.Fatal("OAuth grant is unavailable or belongs to another user")
	}
	cred, _, err := connection.OAuthRecord(file, record.CredentialID)
	if err != nil {
		t.Fatal("OAuth material unavailable")
	}
	report["initial_expiry"] = cred.ExpiresAt
	report["initial_token_revision"] = cred.TokenRevision
	progressBefore := g5Progress(t, m)
	if phase == "refresh" {
		// Exclusive Store ownership and no admitted calls make this a real,
		// explicit protocol-refresh check. It does not stand in for boundary.
		if err := m.refreshGitLabToken(ctx, account, nil); err != nil {
			t.Fatal("explicit real refresh failed; inspect durable outcome before retry")
		}
		report["explicit_refresh"] = true
		status := g5AccessStatus(t, ctx, origin, cred.AccessToken, transport)
		if status != http.StatusUnauthorized {
			t.Fatal("old access token invalidation not confirmed")
		}
		report["previous_access_http_status"] = status
	}
	if phase == "boundary" {
		// Wait until the real token has expired, rather than editing a timestamp or
		// relying on the early refresh window. Start this phase before expiry.
		if !cred.ExpiresAt.After(time.Now()) {
			t.Fatal("start boundary observation before the actual expiry")
		}
		report["status"] = "observing_real_expiry"
		saveReport()
		t.Log("G5_REAL_EXPIRY", cred.ExpiresAt.UTC().Format(time.RFC3339))
		timer := time.NewTimer(time.Until(cred.ExpiresAt.Add(time.Second)))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			t.Fatal("real expiry exceeds observation limit")
		case <-timer.C:
		}
		report["observed_after_real_expiry_at"] = time.Now().UTC()
	}
	// Two durable, inactive Rooms exercise per-Room token leases without starting
	// any Agent/DSH process. They share this explicitly selected OAuth connection.
	if len(m.index.Rooms) == 0 {
		profiles := managerTestProfiles(t)
		m.teams = map[string]teamDefinition{"takeout-dinner": {ID: "takeout-dinner", Name: "G5 read-only", OpeningMessage: "Isolated read-only OAuth acceptance; do not start collaboration.", Runtime: &teamRuntime{Model: profiles["runtime"].Model}, Members: []teamMember{{ID: "reader", Name: "reader", Instructions: "Read-only acceptance"}}}}
		team := m.teams["takeout-dinner"]
		team.Runtime.DSH.BinaryPath = "/usr/bin/false"
		m.teams["takeout-dinner"] = team
		managerCreate(t, m, "g5-reader-a")
		managerCreate(t, m, "g5-reader-b")
	}
	if len(m.index.Rooms) != 2 {
		t.Fatal("expected two isolated reader Rooms")
	}
	start := make(chan struct{})
	errors := make(chan error, 2)
	for _, room := range m.index.Rooms {
		go func(owner RoomIdentity) {
			<-start
			res, err := m.gitlabAccountRead(owner, account, platform.Secret{})(ctx, "/user", nil, nil, 1<<20)
			if err == nil {
				var user struct {
					ID int64 `json:"id"`
				}
				if json.Unmarshal(res.Data, &user) != nil || user.ID != 13 {
					err = platform.ErrCredential
				}
			}
			errors <- err
		}(room.RoomIdentity)
	}
	close(start)
	for range 2 {
		if <-errors != nil {
			t.Fatal("real concurrent Room read failed")
		}
	}
	after := load()
	current, err := connection.ResolveAuthorization(after, after.Connections[0])
	if err != nil || current != account {
		t.Fatal("normal refresh changed authorization evidence")
	}
	rotated, _, err := connection.OAuthRecord(after, record.CredentialID)
	if err != nil {
		t.Fatal("cannot load rotated credential")
	}
	if (phase == "boundary" || phase == "refresh") && rotated.TokenRevision != cred.TokenRevision+1 {
		t.Fatal("expiry boundary did not yield exactly one rotation")
	}
	report["concurrent_rooms"] = 2
	report["final_token_revision"] = rotated.TokenRevision
	report["authorization_unchanged"] = true
	if phase == "refresh" || phase == "boundary" {
		if !reflect.DeepEqual(progressBefore, g5Progress(t, m)) {
			t.Fatal("refresh changed notification progress, Inbox or receipts")
		}
		report["refresh_progress_inbox_receipts_preserved"] = true
		auditMu.Lock()
		refreshRequests := 0
		for _, req := range requests {
			if req.Method == "POST" && req.Path == "/oauth/token" {
				refreshRequests++
			}
		}
		auditMu.Unlock()
		if refreshRequests != 1 {
			t.Fatal("expected exactly one real refresh POST")
		}
		report["refresh_posts"] = refreshRequests
	}
	owner := m.index.Rooms[0].RoomIdentity
	read := m.gitlabAccountRead(owner, account, platform.Secret{})
	project := os.Getenv("TEAMX_G5_PROJECT")
	if project == "" {
		t.Fatal("explicit read-only TEAMX_G5_PROJECT required")
	}
	res, err := read(ctx, "/projects/"+url.PathEscape(project), nil, nil, 1<<20)
	var p struct {
		ID   int64  `json:"id"`
		Path string `json:"path_with_namespace"`
	}
	if err != nil || json.Unmarshal(res.Data, &p) != nil || p.ID <= 0 || p.Path != project {
		t.Fatal("selected real project read failed")
	}
	for _, path := range []string{"/projects/" + strconv.FormatInt(p.ID, 10) + "/events", "/todos"} {
		res, err := read(ctx, path, url.Values{"per_page": {"10"}, "page": {"1"}}, nil, 1<<20)
		var rows []json.RawMessage
		if err != nil || json.Unmarshal(res.Data, &rows) != nil || rows == nil {
			t.Fatal("real notification API read failed")
		}
		report[path] = map[string]any{"http_status": res.Status, "items": len(rows), "delivery": "not exercised"}
	}
	if bin := os.Getenv("TEAMX_GITLAB_BIN"); filepath.IsAbs(bin) {
		lease, err := m.acquireGitLabToken(ctx, owner, account, maxGitLabTokenUse)
		if err != nil {
			t.Fatal("cannot acquire external tool lease")
		}
		var output, diagnostic bytes.Buffer
		process := tool.Process{Definition: tool.Catalog()[0], Output: &output, Error: &diagnostic, Values: map[string]string{"instance": origin, "token": lease.credential.Token.Reveal()}}
		code := process.Run(lease.ctx, []string{"api", "user"})
		finishErr := m.finishGitLabToken(lease)
		report["glab_exit_code"] = code
		report["glab_output_is_json"] = json.Valid(output.Bytes())
		var user struct {
			ID int64 `json:"id"`
		}
		if code != 0 || finishErr != nil || json.Unmarshal(output.Bytes(), &user) != nil || user.ID != 13 {
			t.Fatal("real glab identity read failed; diagnostics omitted")
		}
		report["glab_1_127_0"] = "real_user_read_passed"
	} else {
		report["glab_1_127_0"] = "pending_executable"
	}
	report["reference_image_git_delivery"] = "pending_explicit_fixture_and_acceptance"
	if path := os.Getenv("TEAMX_G5_SOURCE"); path != "" {
		var fixture struct {
			Source platform.SourceRef `json:"source"`
			Text   string             `json:"text"`
		}
		if !filepath.IsAbs(path) || readSnapshot(path, &fixture, 1<<20) != nil || fixture.Source.Instance != origin || fixture.Source.Provider != "gitlab" {
			t.Fatal("invalid explicit source fixture")
		}
		room := m.index.Rooms[0].roomSummary
		svc := eventLiveService(t, &eventHarness{m: m, room: room})
		snapshot, err := svc.store.LoadRoom()
		if err != nil {
			t.Fatal("cannot open isolated reader Room")
		}
		member := snapshot.Members[0]
		bindings, err := m.readToolBindingsLocked(room.RoomIdentity)
		if err != nil {
			t.Fatal("cannot read isolated tool bindings")
		}
		if len(bindings.Bindings) == 0 {
			bindCLITool(t, m, room.RoomIdentity, member, platformConnection{Connection: record.Connection}, "gitlab", bindings.Revision)
		}
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		resolution, _, err := m.resolveGitlabFollowup(ctx, id, fixture.Source)
		if err != nil || !resolution.ObjectVerified {
			t.Fatal("real saved-source resolution failed")
		}
		report["source_resolution"] = map[string]any{"status": resolution.Status, "reason_code": resolution.ReasonCode, "object_verified": resolution.ObjectVerified, "complete": resolution.Complete}
		images := gitlab.FindNotificationImages(fixture.Text, fixture.Source, 1)
		if len(images.Images) > 0 {
			if fixture.Source.Channel != "todos" {
				t.Fatal("saved-source image gate currently requires a real Todos source")
			}
			m.mu.Lock()
			state, err := m.currentTodosLocked(room.RoomIdentity)
			m.mu.Unlock()
			if err != nil {
				t.Fatal("cannot load isolated subscription")
			}
			if state == nil {
				created, err := m.configureTodosSubscription(ctx, notification.Key{Owner: room.RoomIdentity, Source: gitlab.TodosSourceID}, 0, gitlab.TodosConfig{Enabled: true, ConnectionID: record.ID, Target: inboxTarget(id)})
				if err != nil {
					t.Fatal("cannot create isolated real-account subscription")
				}
				state = &created
			}
			source := fixture.Source
			source.SubscriptionID = state.SubscriptionID
			item := notification.Item{Key: source.ItemKey, DeliveryID: notification.DeliveryID(state.Metadata().Key, source.ItemKey), Text: fixture.Text, Source: source, CreatedAt: time.Now().UTC(), Order: "00000000000000000001"}
			var saved InboxEntry
			path := filepath.Join(svc.store.dir, "inboxes", id.MembershipID, "00000000000000000001.json")
			if _, err := os.Stat(path); err == nil {
				if readSnapshot(path, &saved, maxPrivateFileBytes) != nil || saved.Item.Source != source || saved.Item.Text != fixture.Text {
					t.Fatal("saved isolated notice differs from selected fixture")
				}
				item = saved.Item
			}
			if err := svc.receiveInbox(ctx, inboxTarget(id), item); err != nil {
				t.Fatal("cannot seed saved real notice in isolated Inbox")
			}
			receipt, err := svc.room.Call[PostReceipt](ctx, postIntegrationMessage{RoomIdentity: room.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source})
			if err != nil {
				t.Fatal("cannot deliver saved notice to isolated Room")
			}
			for _, inbox := range []bool{false, true} {
				req := attachmentRequest{ServiceInfo: svc.info, Seq: receipt.Seq, Path: images.Images[0].Path}
				if inbox {
					req.AgentID, req.MembershipID, req.Seq = id.AgentID, id.MembershipID, 1
				}
				data, mime, _, err := m.downloadAttachment(ctx, req, nil, gitlab.UploadLimits{MaxBytes: 8 << 20, MaxPixels: 24000000})
				if err != nil {
					t.Fatal("real saved-source attachment path failed")
				}
				kind := "Room"
				if inbox {
					kind = "Inbox"
				}
				report["image_"+kind] = map[string]any{"bytes": len(data), "mime": mime, "browser_visual_and_model": "not exercised"}
			}
			report["notice_delivery"] = "saved real notice seeded locally; not a fresh remote poll"
		}
	}
	report["status"] = "read_checks_passed_remaining_gates_pending"
	if phase == "boundary" && os.Getenv("TEAMX_G5_LOGOUT_AFTER_BOUNDARY") == "1" {
		g5Logout(t, ctx, m, report)
	}
}

// Server-side logout checks the real production lifecycle and remote access
// invalidation. Browser logout presentation is a separate recorded checkpoint.
func g5Logout(t *testing.T, ctx context.Context, m *roomManager, report map[string]any) {
	t.Helper()
	progress := g5Progress(t, m)
	m.mu.Lock()
	file, err := m.connections.Load()
	if err != nil || len(file.Connections) == 0 {
		m.mu.Unlock()
		t.Fatal("cannot capture logout evidence")
	}
	record := file.Connections[0]
	cred, app, err := connection.OAuthRecord(file, record.CredentialID)
	if err != nil {
		m.mu.Unlock()
		t.Fatal("cannot capture active grant")
	}
	records, err := m.gitlabAuthImpactLocked(file, record.ID, app)
	if err != nil {
		m.mu.Unlock()
		t.Fatal("cannot capture shared logout impact")
	}
	affected, _, err := m.gitlabAuthReferencesLocked(records)
	m.mu.Unlock()
	if err != nil {
		t.Fatal("cannot capture logout references")
	}
	w := gitlabAuthHTTP(m, "logout", gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: Caller{Kind: "human", ID: humanOperator()}, ID: record.ID, Version: record.Version, ApplicationID: app.ID, Affected: affected}, nil)
	var view gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatal("logout not started; do not replay blindly")
	}
	m.mu.Lock()
	session := m.gitlabAuthSessions[view.SessionID]
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		t.Fatal("logout settlement timeout")
	case <-session.done:
	}
	m.mu.Lock()
	status := session.Status
	after, err := m.connections.Load()
	m.mu.Unlock()
	if err != nil || status != "logged_out" {
		t.Fatal("remote logout receipt not confirmed")
	}
	for _, record := range after.Connections {
		if record.CredentialID == cred.ID {
			if _, err := connection.ResolveAuthorization(after, record); err == nil {
				t.Fatal("shared authorization remains readable after logout")
			}
		}
	}
	statusCode := g5AccessStatus(t, ctx, app.Origin, cred.AccessToken, m.platformHTTP)
	if statusCode != http.StatusUnauthorized {
		t.Fatal("remote token invalidation not confirmed")
	}
	report["logout"] = map[string]any{"status": status, "shared_connections": len(records), "old_access_http_status": statusCode, "application_retained": len(after.OAuthApplications) == len(file.OAuthApplications)}
	if !reflect.DeepEqual(progress, g5Progress(t, m)) {
		t.Fatal("logout changed notification progress, Inbox or receipts")
	}
	report["logout_progress_inbox_receipts_preserved"] = true
	report["status"] = "boundary_and_backend_logout_passed_relogin_pending"
}

func g5Progress(t *testing.T, m *roomManager) map[string]string {
	t.Helper()
	result := map[string]string{}
	m.mu.Lock()
	defer m.mu.Unlock()
	digest := func(key string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal("cannot capture progress evidence")
		}
		result[key] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	for _, room := range m.index.Rooms {
		for _, raw := range m.notifications.StatesLocked(room.RoomIdentity) {
			switch s := raw.(type) {
			case gitlab.EventsState:
				digest(s.SubscriptionID, []any{s.Baseline, s.Watermark, s.Inbox, s.Observed})
			case gitlab.TodosState:
				digest(s.SubscriptionID, []any{s.Baseline, s.Inbox, s.Observed})
			}
		}
		var snapshot RoomSnapshot
		if readSnapshot(filepath.Join(m.roomDir(room.RoomID), "room.json"), &snapshot, maxRoomFileBytes) != nil {
			t.Fatal("cannot read receipt evidence")
		}
		digest(room.RoomID+"/receipts", []any{snapshot.LatestSeq, snapshot.IntegrationReceipts})
		paths, err := filepath.Glob(filepath.Join(m.roomDir(room.RoomID), "inboxes", "*", "*.json"))
		if err != nil {
			t.Fatal("cannot list Inbox evidence")
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("cannot read Inbox evidence")
			}
			result[path] = fmt.Sprintf("%x", sha256.Sum256(data))
		}
	}
	return result
}

func g5AccessStatus(t *testing.T, ctx context.Context, origin, token string, transport http.RoundTripper) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", origin+"/api/v4/user", nil)
	if err != nil {
		t.Fatal("invalid negative probe")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal("negative probe transport unconfirmed")
	}
	res.Body.Close()
	return res.StatusCode
}
