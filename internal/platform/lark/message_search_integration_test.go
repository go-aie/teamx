//go:build integration || e2e

package lark

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func messageSearchClient(t *testing.T) (*Client, string, platform.AccountIdentity) {
	t.Helper()
	c, dir, account := testClient(t)
	script := strings.Replace(fixture, "path=a[2]", "path=a[2]\n"+messageSearchFixture, 1)
	if err := os.WriteFile(c.profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return c, dir, account
}

func TestMessageSearchSinglePageContract(t *testing.T) {
	parallelClientTest(t)
	c, dir, a := messageSearchClient(t)
	r := searchRequest()
	r.ChatIDs, r.FromID, r.IsAtMe = []string{"oc_chat"}, "ou_sender", true
	// Input offsets are normalized only for the search wire DTO.
	r.Start = r.Start.In(time.FixedZone("offset", 8*3600))
	page, err := c.SearchMessages(context.Background(), a, r)
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.PageToken != "second" {
		t.Fatal(page, err)
	}
	r.PageToken = page.PageToken
	page, err = c.SearchMessages(context.Background(), a, r)
	if err != nil || len(page.Items) != 0 || page.HasMore {
		t.Fatal(page, err)
	}
	r.PageToken, r.ChatType = "", "p2p"
	if _, err := c.SearchMessages(context.Background(), a, r); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"empty_more", "unordered"} {
		setMode(t, dir, mode)
		page, err = c.SearchMessages(context.Background(), a, r)
		if err != nil || !page.HasMore || mode == "empty_more" && len(page.Items) != 0 || mode == "unordered" && len(page.Items) != 2 {
			t.Fatal(mode, page, err)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(calls), "/open-apis/im/v1/messages/search") != 5 || strings.Contains(string(calls), "--page-all") || strings.Contains(string(calls), "/mget") {
		t.Fatal("implicit page/detail expansion")
	}
}

func TestMessageSearchRejectsIncompleteAndUnverifiedPages(t *testing.T) {
	parallelClientTest(t)
	for mode, code := range map[string]string{
		"loop": "invalid_response", "bad_token": "invalid_response", "missing_token": "invalid_response", "missing_more": "invalid_response",
		"missing_items": "invalid_response", "null_items": "invalid_response", "duplicate": "invalid_response", "oversize": "invalid_response",
		"bad_id": "invalid_response", "wrong_chat": "invalid_response", "wrong_type": "invalid_response", "missing_type": "invalid_response",
		"wrong_sender": "invalid_response", "bad_time": "invalid_response", "outside_window": "invalid_response", "response_bot": "invalid_response",
		"notice": "search_incomplete", "switch": "identity_mismatch", "permission": "permission_denied", "rate": "rate_limited",
		"cursor": "cursor_expired", "network": "network_error", "parameter_error": "invalid_request", "unknown_error": "query_failed",
		"malformed": "invalid_response", "large": "output_limit",
	} {
		t.Run(mode, func(t *testing.T) {
			c, dir, a := messageSearchClient(t)
			setMode(t, dir, mode)
			r := searchRequest()
			r.ChatIDs, r.FromID = []string{"oc_chat"}, "ou_sender"
			page, err := c.SearchMessages(context.Background(), a, r)
			if ErrorCode(err) != code || page.Items != nil || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal(mode, page, err)
			}
			if mode == "notice" {
				var notice *SearchNoticeError
				if !errors.As(err, &notice) || !strings.Contains(notice.Notice, "incomplete") {
					t.Fatal("notice evidence lost")
				}
			}
		})
	}
}

func TestMessageBatchNativeDetailsAndOmissions(t *testing.T) {
	parallelClientTest(t)
	for _, mode := range []string{"ok", "partial", "empty", "deleted", "name_permission"} {
		t.Run(mode, func(t *testing.T) {
			c, dir, a := messageSearchClient(t)
			setMode(t, dir, mode)
			batch, err := c.BatchMessages(context.Background(), a, []string{"om_one", "om_two"})
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if mode == "partial" {
				want = 1
			}
			if mode == "empty" {
				want = 0
			}
			if len(batch.Items) != want || len(batch.MissingIDs) != 2-want {
				t.Fatal("lost unresolved detail IDs")
			}
			if mode == "partial" && (batch.MissingIDs[0] != "om_one" || batch.Items[0].ID != "om_two") {
				t.Fatal("wrong missing item")
			}
			for _, m := range batch.Items {
				if !m.valid() || m.RootID != "om_root" || m.ParentID != "om_parent" || m.ThreadID != "omt_thread" || m.Sender.TenantKey != "external" || len(m.Mentions) != 1 || !m.IsUpdated || m.Updated != "100001" || !strings.Contains(m.Body.Content, "all") {
					t.Fatal("native details lost")
				}
			}
			if mode == "ok" && batch.Items[0].ID != "om_one" {
				t.Fatal("batch order depends on server")
			}
			if mode == "deleted" && !batch.Items[1].Deleted {
				t.Fatal("native deletion evidence lost")
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Count(string(calls), "/messages/mget") != 1 || strings.Contains(string(calls), "/messages/om_") {
				t.Fatal("unbounded fallback")
			}
		})
	}
}

func TestMessageBatchFailuresDiscardCandidates(t *testing.T) {
	parallelClientTest(t)
	for mode, code := range map[string]string{
		"missing_items": "invalid_response", "null_items": "invalid_response", "duplicate": "invalid_response", "unrequested": "invalid_response",
		"missing_body": "invalid_response", "missing_content": "invalid_response", "missing_sender": "invalid_response",
		"bad_time": "invalid_response", "bad_reply": "invalid_response", "response_bot": "invalid_response", "switch": "identity_mismatch",
		"detail_error": "details_unavailable", "parameter_error": "invalid_request", "permission": "permission_denied", "rate": "rate_limited",
		"network": "network_error", "malformed": "invalid_response", "unknown_error": "query_failed", "large": "output_limit",
	} {
		t.Run(mode, func(t *testing.T) {
			c, dir, a := messageSearchClient(t)
			setMode(t, dir, mode)
			batch, err := c.BatchMessages(context.Background(), a, []string{"om_one", "om_two"})
			if ErrorCode(err) != code || batch.Items != nil || batch.MissingIDs != nil || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal(mode, batch, err)
			}
		})
	}
}

func TestMessageSearchReadsCancelAndShareProfileGate(t *testing.T) {
	parallelClientTest(t)
	for _, batch := range []bool{false, true} {
		c, dir, a := messageSearchClient(t)
		setMode(t, dir, "block")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			if batch {
				_, err := c.BatchMessages(ctx, a, []string{"om_one"})
				done <- err
			} else {
				_, err := c.SearchMessages(ctx, a, searchRequest())
				done <- err
			}
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(dir, "read")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("read did not start")
			}
			time.Sleep(10 * time.Millisecond)
		}
		wait, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
		if _, err := c.Verify(wait); !errors.Is(err, context.DeadlineExceeded) {
			cancel()
			t.Fatal("profile gate bypassed", err)
		}
		stop()
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("cancel did not settle")
		}
		setMode(t, dir, "ok")
		if _, err := c.Verify(context.Background()); err != nil {
			t.Fatal("gate not released", err)
		}
	}
}

func TestMessageBatchUsesSingleBoundedBatch(t *testing.T) {
	parallelClientTest(t)
	c, dir, a := messageSearchClient(t)
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = "om_" + strings.Repeat("a", i+1)
	}
	batch, err := c.BatchMessages(context.Background(), a, ids)
	if err != nil || len(batch.Items) != 50 || len(batch.MissingIDs) != 0 {
		t.Fatal("full batch", err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(calls), "/messages/mget") != 1 {
		t.Fatal("batch expanded")
	}
}
