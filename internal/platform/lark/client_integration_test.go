//go:build integration || e2e

package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUserIdentityAndSinglePageReads(t *testing.T) {
	parallelClientTest(t)
	c, dir, account := testClient(t)
	v, err := c.Verify(context.Background())
	if err != nil || v.Account != account || v.Username != "中文显示名" || v.UserID != 0 {
		t.Fatal(v, err)
	}
	req := MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2}
	page, err := c.Messages(context.Background(), account, req)
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.PageToken != "second" {
		t.Fatal(page, err)
	}
	if page.Items[0].Text() != "private-message" || page.Items[0].Sender.TenantKey == account.Tenant.Value {
		t.Fatal("lost message or distinct target tenant")
	}
	req.PageToken = page.PageToken
	page, err = c.Messages(context.Background(), account, req)
	if err != nil || page.HasMore || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	message, err := c.Message(context.Background(), account, "om_one")
	if err != nil || message.ID != "om_one" || message.Sender.Name != "同名用户" {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	remotePages := 0
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		var args []string
		if json.Unmarshal([]byte(line), &args) != nil {
			t.Fatal("invalid fixture log")
		}
		if strings.Contains(line, "--page-all") || strings.Contains(line, "search") {
			t.Fatal("hidden extra query")
		}
		if len(args) > 4 && args[4] == "/open-apis/im/v1/messages" {
			remotePages++
		}
	}
	if remotePages != 2 {
		t.Fatal("expected one remote page per read", remotePages)
	}
	setMode(t, dir, "same_time")
	req.PageToken = ""
	page, err = c.Messages(context.Background(), account, req)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("dropped equal timestamp", err)
	}
}

func TestReadFailuresDiscardCandidate(t *testing.T) {
	parallelClientTest(t)
	cases := map[string]string{"switch": "identity_mismatch", "bot": "credential_unavailable", "logout": "credential_unavailable", "version": "cli_version_mismatch", "auth": "credential_rejected", "permission": "permission_denied", "rate": "rate_limited", "cursor": "cursor_expired", "network": "network_error", "exit": "query_failed", "malformed": "invalid_response", "wrong_chat": "invalid_response", "duplicate": "invalid_response", "bad_time": "invalid_response", "incomplete": "invalid_response", "null_items": "invalid_response", "response_bot": "invalid_response", "large": "output_limit", "loop": "invalid_response"}
	for mode, code := range cases {
		t.Run(mode, func(t *testing.T) {
			c, dir, a := testClient(t)
			setMode(t, dir, mode)
			req := MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2}
			if mode == "loop" {
				req.PageToken = "second"
			}
			page, err := c.Messages(context.Background(), a, req)
			if ErrorCode(err) != code || page.Items != nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("%s: page escaped or error category: %v", mode, err)
			}
		})
	}
}

func TestCancellationAndProfileGate(t *testing.T) {
	parallelClientTest(t)
	c, dir, a := testClient(t)
	setMode(t, dir, "block")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Messages(ctx, a, MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "read")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("read did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	other := *c
	wait, cancelWait := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelWait()
	if _, err := other.Verify(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("profile gate not cancellable", err)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process did not settle")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancel too slow")
	}
	setMode(t, dir, "ok")
	if _, err := c.Verify(context.Background()); err != nil {
		t.Fatal("profile gate not released", err)
	}
}

func TestSenderNameLookupBoundedAndOptional(t *testing.T) {
	parallelClientTest(t)
	for _, mode := range []string{"ok", "name_permission", "name_malformed", "name_partial", "name_wrong", "name_duplicate", "name_invalid", "name_timeout"} {
		t.Run(mode, func(t *testing.T) {
			c, dir, account := testClient(t)
			setMode(t, dir, mode)
			messages := []Message{sampleMessage("om_one", "oc_chat", "one"), sampleMessage("om_two", "oc_chat", "two"), sampleMessage("om_three", "oc_chat", "three")}
			messages[1].Sender.ID = "ou_other"
			raw, _ := json.Marshal(messages)
			if err := os.WriteFile(filepath.Join(dir, "sender-messages.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			page, err := c.Messages(context.Background(), account, MessagesRequest{ChatID: "oc_chat", Start: 1000, End: 1010, PageSize: 50})
			if err != nil || len(page.Items) != len(messages) {
				t.Fatal("optional lookup blocked message read", page, err)
			}
			for i, m := range page.Items {
				wantName := ""
				if mode == "ok" || mode == "name_partial" && i == 1 || mode == "name_invalid" && i != 1 {
					wantName = "同名用户"
				}
				if m.Sender.ID != messages[i].Sender.ID || m.Sender.Name != wantName {
					t.Fatal("name assigned to wrong sender or native identity lost", m.Sender)
				}
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Count(string(calls), "/open-apis/contact/v3/users/basic_batch") != 1 {
				t.Fatal("expected exactly one deduplicated name request")
			}
		})
	}
}

func TestSenderLookupCannotEscapeAccountGuard(t *testing.T) {
	parallelClientTest(t)
	c, dir, account := testClient(t)
	setMode(t, dir, "name_switch")
	m, err := c.Message(context.Background(), account, "om_one")
	if ErrorCode(err) != "identity_mismatch" || m.ID != "" {
		t.Fatal("account switch during name lookup released candidate", m, err)
	}
}

func TestSelfSenderUsesVerifiedNameWithoutContactPermission(t *testing.T) {
	parallelClientTest(t)
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint(mixed), func(t *testing.T) {
			c, dir, account := testClient(t)
			setMode(t, dir, "name_permission")
			self := sampleMessage("om_self", "oc_chat", "好的")
			self.Sender.ID = account.ID.Value
			messages := []Message{self}
			if mixed {
				messages = append(messages, sampleMessage("om_other", "oc_chat", "其他人的消息"))
			}
			raw, _ := json.Marshal(messages)
			if err := os.WriteFile(filepath.Join(dir, "sender-messages.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			page, err := c.Messages(context.Background(), account, MessagesRequest{ChatID: "oc_chat", Start: 1000, End: 1010, PageSize: 50})
			if err != nil || len(page.Items) != len(messages) || page.Items[0].Sender.Name != "中文显示名" {
				t.Fatal("verified self name lost", page, err)
			}
			if mixed && page.Items[1].Sender.Name != "" {
				t.Fatal("self name attributed to another sender")
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			lookups := strings.Count(string(calls), "/open-apis/contact/v3/users/basic_batch")
			if !mixed && lookups != 0 || mixed && lookups != 1 {
				t.Fatal("self should not require contact lookup", lookups)
			}
		})
	}
}

func TestSenderLookupSplitsAtNativeBatchLimit(t *testing.T) {
	parallelClientTest(t)
	c, dir, account := testClient(t)
	messages := []Message{}
	for i := range 25 {
		m := sampleMessage(fmt.Sprintf("om_%02d", i), "oc_chat", "正文")
		m.Sender.ID = fmt.Sprintf("ou_%02d", i)
		messages = append(messages, m)
	}
	raw, _ := json.Marshal(messages)
	if err := os.WriteFile(filepath.Join(dir, "sender-messages.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	page, err := c.Messages(context.Background(), account, MessagesRequest{ChatID: "oc_chat", Start: 1000, End: 1010, PageSize: 50})
	if err != nil || len(page.Items) != 25 {
		t.Fatal(page, err)
	}
	for _, m := range page.Items {
		if m.Sender.Name != "同名用户" {
			t.Fatal("later name batch lost", m.Sender)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(calls), "/open-apis/contact/v3/users/basic_batch") != 3 {
		t.Fatal("expected 10+10+5 name batches")
	}
}
