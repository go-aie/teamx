//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Separate from model understanding: download existing saved images and return
// them through the formal MCP tool. Never posts comments, updates Todos, refreshes
// a shared OAuth grant or changes an existing Room/profile.
func TestRealMCPInboxSavedPlatformImages(t *testing.T) {
	provider := os.Getenv("TEAMX_MCP_PLATFORM_REAL")
	if provider == "" {
		t.Skip("explicit gitlab or lark saved-source acceptance required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "provider": provider, "recorded_at": time.Now().UTC(), "remote_business_writes": 0, "real_model": false}
	defer imageRealReport(t, out, report)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var svc *service
	var id MemberIdentity
	switch provider {
	case "lark":
		var input struct {
			Profile platform.CLIProfile `json:"profile"`
			Item    notification.Item   `json:"item"`
		}
		b, err := os.ReadFile(os.Getenv("TEAMX_LARK_IMAGE_INPUT"))
		if err != nil || json.Unmarshal(b, &input) != nil {
			t.Fatal("explicit Lark saved image/profile input required")
		}
		h := newLarkHarness(t)
		h.m.larkClients = nil
		c := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "isolated MCP image acceptance", Provider: "lark", URL: lark.Origin(input.Profile.Brand), CLIProfile: &input.Profile})
		c, err = h.m.verifyPlatform(ctx, managedPlatformRequest{ID: c.ID, Version: c.Version})
		if err != nil || c.Validation.Status != "valid" {
			t.Fatal("Lark profile verification failed", err)
		}
		h.c = c
		member := h.members[0]
		id = MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		cfg := realLarkScopeConfig(t, c, []string{input.Item.Source.Reference.ChatID.Value}, inboxTarget(id))
		cfg.Groups.MessageMode, cfg.Groups.AtMe, cfg.Groups.AtAll = "all", false, false
		state := h.configure(cfg)
		item := input.Item
		item.Source.SubscriptionID = state.SubscriptionID
		item.Route = notification.Route{Target: inboxTarget(id), Version: state.SubscriptionVersion}
		svc = h.svc
		h.m.bindPlatformReferences(svc)
		if err := svc.receiveInbox(ctx, inboxTarget(id), item); err != nil {
			t.Fatal(err)
		}
		saveMaterialTurn(t, svc, id, 0, 1)
		report["managed_connection_path"] = true
	case "gitlab":
		var saved InboxEntry
		if err := readSnapshot(os.Getenv("TEAMX_GITLAB_IMAGE_NOTICE"), &saved, maxPrivateFileBytes); err != nil || saved.Item.Source.Provider != "gitlab" {
			t.Fatal("explicit saved GitLab image entry required")
		}
		imageRealCredential(t, "GITLAB_PAT")
		credential := platform.AccessCredential{Kind: "oauth", Token: platform.NewSecret(os.Getenv("GITLAB_PAT"))}
		// Reuse the standard bearer-only download client with an explicit read-only
		// test credential; the managed OAuth registry lifecycle is not exercised here.
		transport := workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" || r.URL.Scheme+"://"+r.URL.Host != saved.Item.Source.Instance {
				return nil, platform.ErrCredential
			}
			return http.DefaultTransport.RoundTrip(r)
		})
		identity := gitlab.VerifyIdentityWithCredential(ctx, saved.Item.Source.Instance, credential, transport)
		if identity.Status != "valid" {
			t.Fatal("explicit test account verification failed", identity.ErrorCode)
		}
		if recipient := saved.Item.Source.Reference.RecipientID; recipient.Valid() && recipient.Value != strconv.FormatInt(identity.UserID, 10) {
			t.Fatal("test account differs from saved notification recipient")
		}
		h := newInboxHarnessWithTimeout(t, InboxLimits{}, 2*time.Minute)
		initReferenceTest(t, h)
		svc = referenceTestService(h)
		svc.info.ServiceEpoch = "isolated-platform-acceptance"
		id = h.id
		q := h.receive("real-saved-image")
		q.Item = saved.Item
		inboxCall[InboxReceipt](t, h, q)
		inboxRunning(t, h, 1, 1)
		var downloads int
		svc.materialAttachments = &materialAttachments{download: func(callCtx context.Context, request attachmentRequest, _ *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
			// Resolve only complete references in this exact immutable saved body.
			upload, found := gitlab.FindUpload(saved.Item.Text, saved.Item.Source, request.Path)
			if !found || request.RoomIdentity != id.RoomIdentity || request.AgentID != id.AgentID || request.MembershipID != id.MembershipID {
				return nil, "", nil, ErrIdentity
			}
			data, mime, err := gitlab.DownloadUploadWithCredential(callCtx, saved.Item.Source.Instance, credential, upload, transport, limits)
			if err != nil {
				return nil, "", nil, err
			}
			downloads++
			return data, mime, &attachmentAccess{}, nil
		}, check: func(context.Context, ServiceInfo, attachmentReader, TurnRecord, []materialNotice, []*attachmentAccess) error {
			return nil
		}}
		report["downloads"] = &downloads
		report["managed_connection_path"] = false
		report["credential_scope"] = "explicit GET-only test bearer; no OAuth refresh or registry lifecycle"
	default:
		t.Fatal("TEAMX_MCP_PLATFORM_REAL accepts gitlab or lark")
	}
	a, _, _ := accessFixture(t)
	token, _ := issueInboxCredential(t, a, svc, id, true)
	result, view := mustInboxCall(t, a, token, 1, "", 25)
	supplied := 0
	for _, image := range view.Images {
		if image.Status == "supplied" {
			supplied++
		}
	}
	if supplied == 0 || len(result.Content) < 3 {
		t.Fatal("no actual platform image supplied", view)
	}
	status, err := svc.memberInboxStatus(ctx, id)
	if err != nil || status.ConsumedThrough != 1 {
		t.Fatal("formal output not consumed", status, err)
	}
	report["image_coverage"], report["content_blocks"], report["consumed_through"] = view.Images, len(result.Content), status.ConsumedThrough
	report["status"] = "passed"
	if err := os.WriteFile(filepath.Join(out, "coverage.json"), mcpCoverageJSON(t, view), 0600); err != nil {
		t.Fatal(err)
	}
}

func mcpCoverageJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}
