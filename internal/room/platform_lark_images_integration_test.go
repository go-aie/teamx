//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestLarkInboxImagePreviewAndModelPrompt(t *testing.T) {
	parallelFixtureTest(t)
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "legacy"}[legacy], func(t *testing.T) {
			h, id, query, data := larkImageHarness(t, legacy)
			before, _ := h.svc.memberInboxStatus(context.Background(), id)
			beforeRoom, _ := h.svc.store.LoadRoom()
			w := attachmentHTTP(h.m, query)
			if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), data) || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(w.Code, w.Body.String())
			}
			saveMaterialTurn(t, h.svc, id, 0, 1)
			input := materialInput(id, 0, 1)
			// Text-only sessions must not download, and must acknowledge the image.
			logBefore, _ := os.ReadFile(filepath.Join(h.dir, "downloads"))
			materials, err := h.svc.prepareDSHMaterials(context.Background(), input, 1, false)
			if err != nil || !strings.Contains(materialText(materials.blocks("base")), "text-only") {
				t.Fatal("text-only image status missing", err)
			}
			logAfter, _ := os.ReadFile(filepath.Join(h.dir, "downloads"))
			if !bytes.Equal(logBefore, logAfter) {
				t.Fatal("text-only session downloaded")
			}
			var prompt []dsh.ContentBlock
			backend := h.svc.newMember(id, RunnerConfig{}, nil).(*MemberAgent).Backend.(dshBackend)
			session := &dshSession{backend: backend, opts: adapterOptions(h.svc.store, id), component: &adapterComponent{images: true, runPrompt: func(_ context.Context, blocks []dsh.ContentBlock) (dsh.TurnResult, error) {
				prompt = blocks
				return dsh.TurnResult{}, nil
			}}}
			for range 2 {
				if err := session.Run(context.Background(), input); err != nil {
					t.Fatal(err)
				}
				if materialImageCount(prompt) != 1 || !bytes.Equal(prompt[4].Data, data) || prompt[4].MIMEType != "image/png" {
					t.Fatal("Lark image did not reach RunPrompt", prompt)
				}
				for _, want := range []string{"Inbox #1", `provider="lark"`, "请分析图中的错误", "image positions [1]", "read: image block supplied"} {
					if !strings.Contains(materialText(prompt), want) {
						t.Fatal("missing prompt evidence", want)
					}
				}
			}
			after, _ := h.svc.memberInboxStatus(context.Background(), id)
			afterRoom, _ := h.svc.store.LoadRoom()
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeRoom, afterRoom) {
				t.Fatal("preview/material preparation consumed or changed business state")
			}
			materials, err = h.svc.prepareDSHMaterials(context.Background(), input, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			// Revoke the subscription after preparation: no stale image submission.
			s := h.state()
			retired, err := lark.Retire(s)
			if err != nil {
				t.Fatal(err)
			}
			h.m.mu.Lock()
			err = h.m.notifications.SaveLocked(retired)
			h.m.mu.Unlock()
			if err != nil || materials.check(context.Background()) == nil {
				t.Fatal("retired source retained prompt authority", err)
			}
		})
	}
}

func TestLarkImageSourceScopeAndBudgets(t *testing.T) {
	parallelFixtureTest(t)
	h, id, query, _ := larkImageHarness(t, false)
	for _, change := range []struct{ key, value string }{{"path", "lark-image:img_other"}, {"path", "https://evil.invalid/image.png"}, {"seq", "2"}, {"membership_id", strings.Repeat("f", 32)}, {"room_instance", "other"}, {"service_epoch", "old"}} {
		q := maps.Clone(query)
		q.Set(change.key, change.value)
		if w := attachmentHTTP(h.m, q); w.Code == 200 {
			t.Fatal("cross-source image accepted", change)
		}
	}
	if _, err := os.Stat(filepath.Join(h.dir, "downloads")); !os.IsNotExist(err) {
		t.Fatal("invalid request invoked downloader")
	}
	saveMaterialTurn(t, h.svc, id, 0, 1)
	input := materialInput(id, 0, 1)
	h.svc.imageMaterials = imageMaterialConfig{ImageBytes: 1}
	materials, err := h.svc.prepareDSHMaterials(context.Background(), input, 1, true)
	if err != nil || materialImageCount(materials.blocks("base")) != 0 || !strings.Contains(materialText(materials.blocks("base")), "byte/pixel") {
		t.Fatal("missing byte budget failure", err)
	}
	// A reader from a different running turn cannot borrow page authority.
	req := attachmentRequest{ServiceInfo: h.svc.info, AgentID: id.AgentID, MembershipID: id.MembershipID, Seq: 1, Path: "lark-image:img_fixture"}
	if _, _, _, err := h.m.downloadAttachment(context.Background(), req, &attachmentReader{MemberIdentity: id, TurnNumber: 2}, gitlab.UploadLimits{MaxBytes: 1024, MaxPixels: 16}); err == nil {
		t.Fatal("stale turn accepted")
	}
}

func TestLarkImageLifecycleCancellation(t *testing.T) {
	parallelFixtureTest(t)
	h, _, query, _ := larkImageHarness(t, false)
	if err := os.WriteFile(filepath.Join(h.dir, "block-download"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- attachmentHTTP(h.m, query).Code }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.dir, "download-started")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("download did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	h.m.mu.Lock()
	waits := h.m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.owner == h.room.RoomIdentity })
	h.m.mu.Unlock()
	if len(waits) != 1 {
		t.Fatal("download not registered", len(waits))
	}
	select {
	case code := <-done:
		if code == 200 {
			t.Fatal("canceled image returned")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("download cancellation did not settle")
	}
	select {
	case <-waits[0]:
	default:
		t.Fatal("lifecycle still held")
	}
}
