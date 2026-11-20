//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/go-aie/teamx/internal/testutil"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

func mcpPixelChallenge(t *testing.T) ([]byte, string) {
	t.Helper()
	value, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("%08d", value)
	small := image.NewRGBA(image.Rect(0, 0, 160, 50))
	draw.Draw(small, small.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	drawer := font.Drawer{Dst: small, Src: image.NewUniform(color.Black), Face: basicfont.Face7x13, Dot: fixed.P(50, 28)}
	drawer.DrawString(code)
	large := image.NewRGBA(image.Rect(0, 0, 960, 300))
	xdraw.NearestNeighbor.Scale(large, large.Bounds(), small, small.Bounds(), draw.Src, nil)
	var data bytes.Buffer
	if err := png.Encode(&data, large); err != nil {
		t.Fatal(err)
	}
	return data.Bytes(), code
}

// Actual DSH + actual remote model, with independently generated pixel-only
// answers. Platform download acceptance is recorded separately: these saved
// GitLab-format notifications use a local attachment seam and never write remote
// comments or Todos. No challenge file is placed in the Agent workspace.
func TestRealDSHMCPInboxMultiImage(t *testing.T) {
	if os.Getenv("TEAMX_MCP_INBOX_REAL") != "1" {
		t.Skip("explicit real DSH/model and fresh evidence directory required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "recorded_at": time.Now().UTC(), "real_model": true, "remote_business_writes": 0, "platform_downloads": "local attachment seam; separate provider acceptance"}
	defer imageRealReport(t, out, report)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	installation, err := testutil.InstalledDSH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report["installation"] = installation
	var config struct {
		Model dsh.ModelConfig `json:"model"`
	}
	data, err := os.ReadFile(os.Getenv("TEAMX_MCP_MODEL_INPUT"))
	if err != nil || json.Unmarshal(data, &config) != nil {
		t.Fatal("TEAMX_MCP_MODEL_INPUT must contain explicit model config")
	}
	imageRealCredential(t, config.Model.APIKeyEnv)
	target, err := url.Parse(config.Model.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	target.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(target)
	var imagesSeen atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, "local inspection failed", 500)
			return
		}
		// Observe real DSH's model projection without logging authorization or body.
		imagesSeen.Store(max(imagesSeen.Load(), int32(bytes.Count(body, []byte(`"type":"image_url"`)))))
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Host = target.Host
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	remote, _ := url.Parse(config.Model.BaseURL)
	config.Model.BaseURL = gateway.URL + remote.Path
	report["model_id"] = config.Model.ModelID
	h := newInboxHarnessWithTimeout(t, InboxLimits{}, 5*time.Minute)
	svc := referenceTestService(h)
	svc.info.ServiceEpoch = "isolated-real-mcp"
	svc.referenceResolver = referenceTestResolver
	initReferenceTest(t, h)
	a, _, _ := accessFixture(t)
	var calls atomic.Int32
	accessHandler := a.wrap(http.NotFoundHandler())
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"name":"inbox_read"`)) {
				calls.Add(1)
			}
		}
		accessHandler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	a.internal, a.public = endpoint.URL, endpoint.URL
	first, code1 := mcpPixelChallenge(t)
	second, code2 := mcpPixelChallenge(t)
	for code1 == code2 {
		second, code2 = mcpPixelChallenge(t)
	}
	if err := os.WriteFile(filepath.Join(out, "pixel-first.png"), first, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "pixel-second.png"), second, 0600); err != nil {
		t.Fatal(err)
	}
	expected := [][]string{{code1, code2}}
	report["expected_pixel_codes"] = expected
	var downloads atomic.Int32
	svc.materialAttachments = &materialAttachments{download: func(_ context.Context, req attachmentRequest, _ *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		downloads.Add(1)
		payload := first
		if strings.HasSuffix(req.Path, "second.png") {
			payload = second
		}
		return payload, "image/png", &attachmentAccess{}, nil
	}, check: func(context.Context, ServiceInfo, attachmentReader, TurnRecord, []materialNotice, []*attachmentAccess) error {
		return nil
	}}
	receiveReference(t, h, "plain", referenceTestSource("plain"))
	q := h.receive("pixels")
	q.Item.Source = referenceTestSource("pixels")
	q.Item.Text = "Read both attached images in their displayed order. ![first](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/first.png) ![second](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/second.png) ![duplicate](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/first.png)"
	inboxCall[InboxReceipt](t, h, q)
	receiveTestTaskEvent(t, h, taskEventFixture("mcp-real-event"))
	inboxRunning(t, h, 1, 3)
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	svc.access, svc.dshTurnTimeout = a, 2*time.Minute
	svc.dshProfiles = map[string]dshProfile{"acceptance": {BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs, Workspace: workspace, Model: config.Model,
		Instructions: `Isolated image acceptance: your only task is to call the discovered inbox_read MCP tool exactly once with turn_number=current_turn and limit=25, then privately output JSON {"codes":["first image eight-digit code","second image eight-digit code"]}. Read the codes from image pixels in order. Do not use shell, files, delegation, other tools, Room messages or external actions. Do not repeat a code from memory: read this turn's image result. No business work is requested.`}}
	b := svc.memberBackend(RunnerConfig{Profile: "acceptance"})
	if b.inboxMCP != svc {
		t.Fatal("production default did not select MCP")
	}
	report["entry_selection"] = "production default"
	opts := adapterOptions(h.store, h.id)
	opts.CLIEnv = []string{"TEAMX_ENDPOINT=" + endpoint.URL, "TEAMX_ROOM_ID=" + h.id.RoomID, "TEAMX_ROOM_INSTANCE=" + h.id.RoomInstance, "TEAMX_SERVICE_EPOCH=" + svc.info.ServiceEpoch, "TEAMX_AGENT_ID=" + h.id.AgentID, "TEAMX_MEMBERSHIP_ID=" + h.id.MembershipID}
	var answers []any
	for _, resume := range []bool{false, true} {
		turn, end, start := uint64(1), uint64(3), uint64(0)
		if resume {
			h.store.finishMCPTurn(h.id, 1)
			first, code1 = mcpPixelChallenge(t)
			second, code2 = mcpPixelChallenge(t)
			expected = append(expected, []string{code1, code2})
			report["expected_pixel_codes"] = expected
			if err := os.WriteFile(filepath.Join(out, "pixel-resumed-first.png"), first, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "pixel-resumed-second.png"), second, 0600); err != nil {
				t.Fatal(err)
			}
			q.Item.DeliveryID = "resumed-pixels"
			q.Item.Key = "resumed-pixels"
			q.Item.Source.ItemKey = "resumed-pixels"
			inboxCall[InboxReceipt](t, h, q)
			turn, end, start = 2, 4, 3
			referenceRunning(t, h, turn, start, end)
			b.recover = true
		}
		session, err := b.OpenSession(ctx, opts)
		if err != nil {
			if session != nil {
				session.Close(context.Background())
			}
			t.Fatal("real backend open", err)
		}
		input := materialInput(h.id, 0, end)
		input.Inbox.Through = start
		runErr := session.Run(ctx, input)
		closeErr := session.Close(context.Background())
		var diagnostic backendDiagnostic
		if err := readSnapshot(filepath.Join(filepath.Dir(opts.StatePath), "dsh-turn.json"), &diagnostic, maxPrivateFileBytes); err != nil {
			t.Fatal(err)
		}
		answers = append(answers, diagnostic.Result.Text)
		report["answers"] = answers
		if runErr != nil || closeErr != nil {
			t.Fatal("real Inbox run", runErr, closeErr)
		}
		if !strings.Contains(diagnostic.Result.Text, code1) || !strings.Contains(diagnostic.Result.Text, code2) {
			t.Fatal("pixel-only answers not demonstrated", diagnostic.Result.Text)
		}
		if got := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); got.ConsumedThrough != end {
			t.Fatal("formal read not confirmed", got)
		}
	}
	report["mcp_calls"], report["max_model_image_blocks"], report["downloads"] = calls.Load(), imagesSeen.Load(), downloads.Load()
	if calls.Load() != 2 || imagesSeen.Load() < 2 || downloads.Load() != 4 {
		t.Fatal("one-call multi-image evidence incomplete", calls.Load(), imagesSeen.Load(), downloads.Load())
	}
	report["new_and_resume"] = true
	report["status"] = "passed"
}
