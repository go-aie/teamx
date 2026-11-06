//go:build e2e

package lark

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Opt-in, read-only acceptance. IDs/profile live in process environment only;
// logs contain counts and error categories, never private IDs or message text.
func TestRealLarkSpecifiedChats(t *testing.T) {
	profile := os.Getenv("TEAMX_LARK_TEST_PROFILE")
	chats := strings.Split(os.Getenv("TEAMX_LARK_TEST_CHATS"), ",")
	if profile == "" || len(chats) != 2 {
		t.Skip("set TEAMX_LARK_TEST_PROFILE and exactly two TEAMX_LARK_TEST_CHATS")
	}
	start, e1 := strconv.ParseInt(os.Getenv("TEAMX_LARK_TEST_START"), 10, 64)
	end, e2 := strconv.ParseInt(os.Getenv("TEAMX_LARK_TEST_END"), 10, 64)
	if e1 != nil || e2 != nil || start <= 0 || end < start {
		t.Fatal("set bounded Unix-second test window")
	}
	c, err := New(platform.CLIProfile{Version: 1, Executable: os.Getenv("TEAMX_LARK_TEST_BIN"), Profile: profile, Brand: os.Getenv("TEAMX_LARK_TEST_BRAND"), CLIVersion: CLIVersion})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	v, err := c.Verify(ctx)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	for i, chat := range chats {
		req := MessagesRequest{ChatID: chat, Start: start, End: end, PageSize: 2}
		seen := map[string]bool{}
		pages := 0
		var firstID string
		for {
			page, err := c.Messages(ctx, v.Account, req)
			if err != nil {
				t.Fatalf("chat %d page %d: %s", i+1, pages+1, ErrorCode(err))
			}
			pages++
			for _, m := range page.Items {
				if seen[m.ID] {
					t.Fatalf("chat %d duplicate message", i+1)
				}
				seen[m.ID] = true
				if firstID == "" {
					firstID = m.ID
				}
			}
			if !page.HasMore {
				break
			}
			if pages >= 20 {
				t.Fatalf("chat %d scan incomplete at page budget", i+1)
			}
			req.PageToken = page.PageToken
		}
		if pages < 2 || len(seen) < 2 {
			t.Fatalf("chat %d lacks pagination sample", i+1)
		}
		message, err := c.Message(ctx, v.Account, firstID)
		if err != nil || message.ChatID != chat {
			t.Fatalf("chat %d message verification: %s", i+1, ErrorCode(err))
		}
		t.Logf("chat %d: pages=%d unique_messages=%d complete=true message_get=true", i+1, pages, len(seen))
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err := c.Messages(cancelled, v.Account, MessagesRequest{ChatID: chats[0], Start: start, End: end, PageSize: 2}); err == nil {
		t.Fatal("cancelled read accepted")
	}
}
