//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/testutil"
)

// Existing saved notification -> isolated Room/Inbox -> production preparation
// and adapter -> installed DSH. Only GET requests reach GitLab. Delivery and the
// fixed running turn are explicitly seeded; this is not a scheduler/poller test.
func TestRealGitLabImageMaterials(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_IMAGE_REAL") != "1" {
		t.Skip("explicit saved notice, credentials and fresh output required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "recorded_at": time.Now().UTC(), "scope": "seeded delivery and fixed turn; real GitLab, production material/ACP adapters", "driver_remote_writes": 0, "scheduler": "not exercised", "recovery": "not requested unless TEAMX_GITLAB_IMAGE_RESUME=1; see per-channel resume_status", "browser": "separate TestRealGitLabImagePreview"}
	defer imageRealReport(t, output, report)
	rows := map[string]any{}
	report["channels"] = rows
	model := os.Getenv("TEAMX_GITLAB_IMAGE_MODEL") == "1"
	var installation testutil.DSHInstallation
	var profile dshProfile
	var expected []string
	if model {
		if json.Unmarshal([]byte(os.Getenv("TEAMX_GITLAB_IMAGE_EXPECT")), &expected) != nil || len(expected) == 0 {
			t.Fatal("TEAMX_GITLAB_IMAGE_EXPECT must be a JSON array of pixel-only text to verify; never added to prompt")
		}
		for _, text := range expected {
			if strings.TrimSpace(text) == "" {
				t.Fatal("empty pixel expectation")
			}
		}
		var err error
		installation, err = testutil.InstalledDSH(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		keyEnv := os.Getenv("TEAMX_DSH_API_KEY_ENV")
		if keyEnv == "" {
			keyEnv = "OPENAI_API_KEY"
		}
		imageRealCredential(t, keyEnv)
		profile = dshProfile{BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs, Workspace: t.TempDir(),
			Instructions: `This is a read-only image acceptance turn. Do not use tools, read files, delegate, post messages or perform business actions. Using only the supplied third-party material, return JSON with fields channel, visible_error, error_region, marked_region, coverage. Keep every value under 60 words. channel must be exactly Room or Inbox according to the Third-party material label, not the GitLab source channel. Transcribe the actual visible error in the screenshot and describe where it appears and where any colored annotation appears. List any unread images or truncated text. If no error is visible, say so; never guess missing material. Ordinary output is the requested private result.`,
			Model:        dsh.ModelConfig{Protocol: "openai-completions", BaseURL: os.Getenv("MODEL_BASE_URL"), ModelID: os.Getenv("MODEL_ID"), APIKeyEnv: keyEnv, MaxTokens: 8192, InputModalities: []string{"text", "image"}}}
		if profile.Model.BaseURL == "" || profile.Model.ModelID == "" {
			t.Fatal("MODEL_BASE_URL and MODEL_ID required")
		}
		report["installation"], report["model"] = installation, profile.Model
	}
	for _, channel := range []string{"Room", "Inbox"} {
		t.Run(channel, func(t *testing.T) {
			row := map[string]any{"status": "failed", "model_requested": model}
			rows[channel] = row
			m, svc, id, item := imageRealFixture(t)
			variant := os.Getenv("TEAMX_GITLAB_IMAGE_VARIANT")
			if variant != "" && variant != "partial" {
				t.Fatal("TEAMX_GITLAB_IMAGE_VARIANT accepts partial only")
			}
			if variant == "partial" {
				discovered := gitlab.FindNotificationImages(item.Text, item.Source, gitlab.MaxImageReferences)
				if len(discovered.Images) != 1 || !discovered.Images[0].Supported {
					t.Fatal("partial model variant requires one real image")
				}
				item.Text += "\n\n" + notification.QuoteExcerpt(strings.Repeat("local truncated variant ", 200), 2048, false)
				item.Text += "\n\n" + notification.QuoteExcerpt("![local absent image](/uploads/"+discovered.Images[0].Upload.Secret+"/teamx-i4-absent-image.png)", 2048, false)
				row["variant"] = "local truncated text and absent image; original remote comment unchanged"
			}
			var requests []map[string]any
			row["requests"] = &requests
			m.platformHTTP = workspaceHTTPFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodGet || req.Header.Get("Cookie") != "" || req.Header.Get("PRIVATE-TOKEN") == "" {
					return nil, fmt.Errorf("acceptance requires token-only GET download")
				}
				if len(requests) == 0 {
					probeCtx, stop := context.WithTimeout(req.Context(), 30*time.Second)
					defer stop()
					anonymous, err := http.NewRequestWithContext(probeCtx, http.MethodGet, req.URL.String(), nil)
					if err != nil {
						return nil, err
					}
					res, err := http.DefaultTransport.RoundTrip(anonymous)
					if err != nil {
						return nil, fmt.Errorf("unauthenticated comparison unavailable")
					}
					res.Body.Close()
					row["unauthenticated_status"] = res.StatusCode
					if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusNotFound {
						return nil, fmt.Errorf("unauthenticated status does not demonstrate a private attachment")
					}
				}
				started := time.Now()
				res, err := http.DefaultTransport.RoundTrip(req)
				event := map[string]any{"path": req.URL.EscapedPath(), "elapsed_ms": time.Since(started).Milliseconds(), "cookie_sent": false}
				if res != nil {
					event["status"] = res.StatusCode
				}
				requests = append(requests, event)
				return res, err
			})
			defer func() {
				if err := m.close(); err != nil {
					t.Error(err)
				}
				if t.Failed() {
					row["status"] = "failed"
				}
			}()
			var roomEnd, inboxEnd uint64
			if channel == "Room" {
				receipt, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: id.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source})
				if err != nil {
					t.Fatal(err)
				}
				roomEnd = receipt.Seq
			} else {
				if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
					t.Fatal(err)
				}
				inboxEnd = 1
			}
			saveMaterialTurn(t, svc, id, roomEnd, inboxEnd)
			input := materialInput(id, roomEnd, inboxEnd)
			beforeRoom, _ := svc.store.LoadRoom()
			beforeAgent, _ := svc.store.LoadAgent(id)
			beforeInbox, _ := svc.memberInboxStatus(context.Background(), id)
			checkState := func() {
				afterRoom, _ := svc.store.LoadRoom()
				afterAgent, _ := svc.store.LoadAgent(id)
				afterInbox, _ := svc.memberInboxStatus(context.Background(), id)
				unchanged := reflect.DeepEqual(beforeRoom, afterRoom) && reflect.DeepEqual(beforeAgent, afterAgent) && reflect.DeepEqual(beforeInbox, afterInbox)
				row["protocol_state_unchanged"] = unchanged
				_, err := os.Stat(filepath.Join(svc.store.dir, "agents", id.MembershipID, "followups"))
				row["matters_not_created"] = os.IsNotExist(err)
				if !unchanged || !os.IsNotExist(err) {
					t.Error("material preparation changed business protocol state")
				}
			}
			defer checkState()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			prepared, err := svc.prepareDSHMaterials(ctx, input, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepared.check(ctx); err != nil {
				t.Fatal(err)
			}
			blocks := prepared.blocks("acceptance preflight")
			row["download_materials"] = imageRealMetadata(blocks)
			row["material_status_text"] = materialText(blocks)
			if materialImageCount(blocks) == 0 {
				t.Fatal("no real image survived production preparation")
			}
			row["download_status"] = "passed"
			if variant == "partial" && (!strings.Contains(materialText(blocks), "正文已截断，内容可能不完整") || !strings.Contains(materialText(blocks), "attachment unavailable")) {
				t.Fatal("partial variant failed to report missing coverage")
			}
			// Pure-text mode must not fetch or include images, and must explain loss.
			requestsBeforeText := len(requests)
			textOnly, err := svc.prepareDSHMaterials(ctx, input, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != requestsBeforeText || materialImageCount(textOnly.blocks("")) != 0 || !strings.Contains(materialText(textOnly.blocks("")), "text-only") {
				t.Fatal("text-only coverage missing")
			}
			row["text_only_status"] = "passed"
			if !model {
				row["status"] = "passed"
				row["model_status"] = "not requested"
				return
			}
			// The Open seam only observes; all serialization and execution use the
			// installed component, without substituting HTTP responses or model output.
			backend := dshBackend{store: svc.store, prepare: svc.prepareDSHMaterials, profile: profile, turnTimeout: 2 * time.Minute}
			var componentOpts dsh.Options
			backend.open = func(c context.Context, opts dsh.Options) (dshComponent, error) {
				opts.StateDir = filepath.Join(output, channel, "dsh") // private acceptance evidence, outside disposable Room/credential store
				componentOpts = opts
				session, err := dsh.Open(c, opts)
				if session == nil {
					return nil, err
				}
				row["promptCapabilities.image"] = session.SupportsImages()
				return &imageRealComponent{Session: session, row: row}, err
			}
			session, err := backend.OpenSession(ctx, adapterOptions(svc.store, id))
			if session != nil {
				defer func() {
					err := session.Close(context.Background())
					row["close_succeeded"] = err == nil
					if err != nil {
						t.Error(err)
					}
				}()
			}
			if err != nil {
				row["model_status"] = "open failed"
				row["error"] = err.Error()
				t.Fatal(err)
			}
			err = session.Run(ctx, input)
			if err != nil {
				row["model_status"] = "failed"
				row["error"] = err.Error()
				t.Fatal(err)
			}
			result := row["result"].(dsh.TurnResult)
			if len(result.Tools) != 0 {
				t.Fatal("acceptance model used tools")
			}
			lower := strings.ToLower(result.Text)
			for _, text := range expected {
				if !strings.Contains(lower, strings.ToLower(text)) {
					t.Fatal("pixel-only expectation missing from actual model answer")
				}
			}
			var answer struct {
				Channel      string `json:"channel"`
				VisibleError string `json:"visible_error"`
				ErrorRegion  string `json:"error_region"`
				MarkedRegion string `json:"marked_region"`
				Coverage     string `json:"coverage"`
			}
			raw := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(result.Text), "```json"), "```"), "```"))
			if json.Unmarshal([]byte(raw), &answer) != nil || !strings.EqualFold(answer.Channel, channel) || answer.VisibleError == "" || answer.ErrorRegion == "" || answer.MarkedRegion == "" || answer.Coverage == "" {
				t.Fatal("missing structured source/region/coverage answer; manual pixel review still required")
			}
			if variant == "partial" {
				coverage := strings.ToLower(answer.Coverage)
				if !(strings.Contains(coverage, "truncat") || strings.Contains(coverage, "截断")) || !(strings.Contains(coverage, "unread") || strings.Contains(coverage, "not read") || strings.Contains(coverage, "not supplied") || strings.Contains(coverage, "unavailable") || strings.Contains(coverage, "未读")) {
					t.Fatal("model failed to acknowledge truncated text and unread image")
				}
			}
			row["model_status"], row["status"] = "passed; review spatial description against pixels", "passed"
			if os.Getenv("TEAMX_GITLAB_IMAGE_RESUME") == "1" {
				row["resume_status"] = "failed"
				if err := session.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				var oldRef map[string]any
				if readSnapshot(filepath.Join(componentOpts.StateDir, "backend.json"), &oldRef, maxPrivateFileBytes) != nil || oldRef["sessionId"] == nil || oldRef["sessionId"] == "" {
					t.Fatal("missing closed backend identity")
				}
				resumed, err := dsh.Resume(ctx, componentOpts)
				if resumed != nil {
					defer func() {
						err := resumed.Close(context.Background())
						row["resumed_close_succeeded"] = err == nil
						if err != nil {
							t.Error(err)
						}
					}()
				}
				if err != nil {
					row["resume_error"] = err.Error()
					t.Fatal(err)
				}
				var newRef map[string]any
				if readSnapshot(filepath.Join(componentOpts.StateDir, "backend.json"), &newRef, maxPrivateFileBytes) != nil || newRef["sessionId"] != oldRef["sessionId"] {
					t.Fatal("recovery did not preserve original Session ID")
				}
				row["resumed_same_session"] = true
				recalled, err := resumed.Run(ctx, "Without tools, repeat the exact English error visible in the preceding screenshot, then name the color and location of its annotation rectangle. Do not fetch or read another image. Answer in fewer than 100 words.")
				row["after_resume"] = recalled
				if err != nil {
					row["resume_error"] = err.Error()
					t.Fatal(err)
				}
				if len(recalled.Tools) != 0 {
					t.Fatal("recovery used tools")
				}
				for _, text := range expected {
					if !strings.Contains(strings.ToLower(recalled.Text), strings.ToLower(text)) {
						t.Fatal("recovered context missing verified image error")
					}
				}
				if !strings.Contains(strings.ToLower(recalled.Text), "red") {
					t.Fatal("recovered annotation description missing")
				}
				row["resume_status"] = "passed"
			}

		})
	}
	if !t.Failed() {
		report["status"] = "passed"
	}
}

type imageRealComponent struct {
	*dsh.Session
	row map[string]any
}

func (c *imageRealComponent) RunPrompt(ctx context.Context, blocks []dsh.ContentBlock) (dsh.TurnResult, error) {
	c.row["submitted_materials"] = imageRealMetadata(blocks)
	result, err := c.Session.RunPrompt(ctx, blocks)
	c.row["result"] = result
	return result, err
}
func imageRealMetadata(blocks []dsh.ContentBlock) []map[string]any {
	var rows []map[string]any
	for i, b := range blocks {
		if b.Type == "image" {
			sum := sha256.Sum256(b.Data)
			rows = append(rows, map[string]any{"block_index": i, "bytes": len(b.Data), "mime": b.MIMEType, "sha256": hex.EncodeToString(sum[:])})
		}
	}
	return rows
}
func imageRealOutput(t *testing.T) string {
	t.Helper()
	out := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(out) {
		t.Fatal("fresh absolute TEAMX_DSH_OUTPUT required")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	return out
}
func imageRealReport(t *testing.T, out string, report map[string]any) {
	t.Helper()
	if t.Failed() {
		report["status"] = "failed"
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(out, "report.json"), append(data, '\n'), 0600)
	}
	if err != nil {
		t.Error(err)
	}
	t.Log("private report:", filepath.Join(out, "report.json"))
}
func imageRealCredential(t *testing.T, key string) {
	t.Helper()
	if os.Getenv(key) != "" {
		return
	}
	file := os.Getenv("TEAMX_DSH_ENV_FILE")
	if !filepath.IsAbs(file) {
		t.Fatal("credential or absolute TEAMX_DSH_ENV_FILE required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "node", "--env-file="+file, "-e", "process.stdout.write(process.env[process.argv[1]] || '')", key).Output()
	if err != nil || len(data) == 0 {
		t.Fatal("cannot load required credential")
	}
	t.Setenv(key, string(data))
}
func imageRealFixture(t *testing.T) (*roomManager, *service, MemberIdentity, notification.Item) {
	t.Helper()
	file := os.Getenv("TEAMX_GITLAB_IMAGE_NOTICE")
	if !filepath.IsAbs(file) {
		t.Fatal("absolute TEAMX_GITLAB_IMAGE_NOTICE saved InboxEntry required")
	}
	var entry InboxEntry
	if readSnapshot(file, &entry, maxPrivateFileBytes) != nil || entry.Item.Source.Provider != "gitlab" || entry.Item.Source.Channel != "events" {
		t.Fatal("existing GitLab Events InboxEntry required")
	}
	source := entry.Item.Source
	if len(gitlab.FindNotificationImages(entry.Item.Text, source, gitlab.MaxImageReferences).Images) == 0 {
		t.Fatal("selected saved notice has no image")
	}
	imageRealCredential(t, "GITLAB_PAT")
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "image-i4")
	req := platformTestRequest(m)
	req.URL, req.PAT = source.Instance, os.Getenv("GITLAB_PAT")
	c := platformTestSave(t, m, req)
	c, err := m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal("real GitLab account verification failed")
	}
	// A new subscription has a real verified account and project, but no poller
	// runs and no new external event is requested or written.
	project := source.Project
	if project == "" {
		project, _, _ = strings.Cut(strings.TrimPrefix(source.URL, source.Instance+"/"), "/-/")
	}
	state, err := m.configureEventsSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.EventsSourceID}, 0, gitlab.EventsConfig{Enabled: true, ConnectionID: c.ID, Project: source.Instance + "/" + project})
	if err != nil || state.Binding.ProjectID != source.ProjectID {
		t.Fatal("real project verification failed")
	}
	svc := eventLiveService(t, &eventHarness{m: m, room: room})
	m.bindPlatformReferences(svc)
	snapshot, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	member := snapshot.Members[0]
	id := MemberIdentity{RoomIdentity: snapshot.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	item := entry.Item
	item.Source.SubscriptionID = state.SubscriptionID
	item.Source.Project = state.Binding.Project
	item.Route = notification.Route{}
	return m, svc, id, item
}

// Optional browser gate: expose the actual page and attachment handlers on
// loopback. The browser driver must write browser-done only after checking the
// rendered image; test success alone proves HTTP bytes, not visual correctness.
func TestRealGitLabImagePreview(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_IMAGE_BROWSER") != "1" {
		t.Skip("explicit browser opt-in required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "actual browser preview; no model", "driver_remote_writes": 0}
	defer imageRealReport(t, output, report)
	m, svc, id, item := imageRealFixture(t)
	defer func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	}()
	receipt, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: id.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(m)
	defer server.Close()
	images := gitlab.FindNotificationImages(item.Text, item.Source, gitlab.MaxImageReferences)
	var path string
	for _, im := range images.Images {
		if im.Supported {
			path = im.Path
			break
		}
	}
	if path == "" {
		t.Fatal("supported upload required")
	}
	// Use the public handler with the same saved-source query as the UI.
	query := fmt.Sprintf("room_id=%s&room_instance=%s&service_epoch=%s&caller_id=operator&seq=%d", id.RoomID, id.RoomInstance, svc.info.ServiceEpoch, receipt.Seq)
	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/platform/attachments?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := req.URL.Query()
	q.Set("path", path)
	req.URL.RawQuery = q.Encode()
	ready := map[string]any{"page": server.URL, "room_image": req.URL.String(), "member": id, "output": output}
	q.Set("agent_id", id.AgentID)
	q.Set("membership_id", id.MembershipID)
	req.URL.RawQuery = q.Encode()
	ready["inbox_image"] = req.URL.String()
	data, _ := json.MarshalIndent(ready, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "browser-ready.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("browser ready:", filepath.Join(output, "browser-ready.json"))
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser check not acknowledged; not a visual pass")
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(output, "browser-done")); err == nil {
				report["status"] = "browser driver acknowledged; inspect retained screenshots"
				return
			}
		}
	}
}

// Controlled failure variants change only the disposable local notice. They
// keep the real project's attachment authority; never edit the GitLab comment.
func TestRealGitLabImageCoverage(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_IMAGE_COVERAGE") != "1" {
		t.Skip("select a saved multi-image notice for real coverage checks")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "real downloads; locally constructed failure/truncation variants; no model", "driver_remote_writes": 0}
	defer imageRealReport(t, output, report)
	m, svc, id, item := imageRealFixture(t)
	defer func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	}()
	images := gitlab.FindNotificationImages(item.Text, item.Source, gitlab.MaxImageReferences)
	if len(images.Images) != 2 || !images.Images[0].Supported || !images.Images[1].Supported {
		t.Fatal("exactly two supported real images required")
	}
	// This variant intentionally exceeds the normal excerpt limit and appends an
	// absent filename under one existing upload. The formatter supplies the real
	// truncation marker, while both original image references remain untouched.
	item.Text += "\n\n" + notification.QuoteExcerpt(strings.Repeat("local coverage variant ", 200), 2048, false)
	item.Text += "\n\n" + notification.QuoteExcerpt("![local absent image](/uploads/"+images.Images[0].Upload.Secret+"/teamx-i4-absent-image.png)", 2048, false)
	receipt, err := svc.room.Call[PostReceipt](context.Background(), postIntegrationMessage{RoomIdentity: id.RoomIdentity, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source})
	if err != nil {
		t.Fatal(err)
	}
	saveMaterialTurn(t, svc, id, receipt.Seq, 0)
	input := materialInput(id, receipt.Seq, 0)
	beforeRoom, _ := svc.store.LoadRoom()
	beforeAgent, _ := svc.store.LoadAgent(id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cases := []struct {
		name   string
		config imageMaterialConfig
		count  int
		status string
	}{
		{"partial", imageMaterialConfig{}, 2, "attachment unavailable"},
		{"count_budget", imageMaterialConfig{MaxImages: 1}, 1, "count budget exceeded"},
		{"byte_budget", imageMaterialConfig{ImageBytes: 1}, 0, "byte/pixel"},
	}
	rows := map[string]any{}
	report["cases"] = rows
	for _, c := range cases {
		svc.imageMaterials = c.config
		materials, err := svc.prepareDSHMaterials(ctx, input, 1, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := materials.check(ctx); err != nil {
			t.Fatal(err)
		}
		blocks := materials.blocks("coverage acceptance")
		text := materialText(blocks)
		rows[c.name] = map[string]any{"images": imageRealMetadata(blocks), "status_text": text}
		if materialImageCount(blocks) != c.count || !strings.Contains(text, c.status) || !strings.Contains(text, "正文已截断，内容可能不完整") {
			t.Fatalf("%s missing expected coverage", c.name)
		}
	}
	afterRoom, _ := svc.store.LoadRoom()
	afterAgent, _ := svc.store.LoadAgent(id)
	unchanged := reflect.DeepEqual(beforeRoom, afterRoom) && reflect.DeepEqual(beforeAgent, afterAgent)
	report["protocol_state_unchanged"] = unchanged
	if !unchanged {
		t.Fatal("coverage preparation changed protocol state")
	}
	report["status"] = "passed"
}
