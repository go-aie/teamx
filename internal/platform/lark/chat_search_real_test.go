//go:build e2e

package lark

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Explicit opt-in, metadata reads only. Do not print names, IDs or credentials.
func TestRealLarkChatSearch(t *testing.T) {
	query := os.Getenv("TEAMX_LARK_SEARCH_QUERY")
	if os.Getenv("TEAMX_LARK_SEARCH_REAL") != "1" || query == "" {
		t.Skip("requires explicit search opt-in, query and CLI profile")
	}
	c, err := New(platform.CLIProfile{Version: 1, Executable: os.Getenv("TEAMX_LARK_TEST_BIN"), Profile: os.Getenv("TEAMX_LARK_TEST_PROFILE"), Brand: os.Getenv("TEAMX_LARK_TEST_BRAND"), CLIVersion: CLIVersion})
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	v, err := c.Verify(ctx)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	page, err := c.SearchChats(ctx, v.Account, ChatSearchRequest{Query: query, Type: "all"})
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	groups, private := 0, 0
	for _, chat := range page.Chats {
		if !chat.Valid() {
			t.Fatal("invalid search result")
		}
		if chat.Mode == "p2p" {
			private++
		} else {
			groups++
		}
	}
	t.Logf("groups=%d private=%d more_groups=%t warnings=%v", groups, private, page.HasMore, page.SearchWarnings)
	if groups+private == 0 {
		t.Fatal("no matches for acceptance query")
	}
}
