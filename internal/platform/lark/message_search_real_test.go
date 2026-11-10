//go:build e2e

package lark

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// This opt-in test reuses S1's private input, performs only bounded reads and
// logs counts/categories. It never opens subscription state or creates a Room.
func TestRealLarkMessageSearchReads(t *testing.T) {
	path := os.Getenv("TEAMX_LARK_SEARCH_TEST_INPUT")
	if path == "" {
		t.Skip("set TEAMX_LARK_SEARCH_TEST_INPUT to the private S1 input")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		t.Fatal("unreadable or oversized private input")
	}
	var input struct {
		Executable string    `json:"executable"`
		Profile    string    `json:"profile"`
		Brand      string    `json:"brand"`
		AppID      string    `json:"app_id"`
		OpenID     string    `json:"open_id"`
		Start      time.Time `json:"start"`
		End        time.Time `json:"end"`
	}
	if json.Unmarshal(raw, &input) != nil || !input.End.After(input.Start) || input.End.Sub(input.Start) > 24*time.Hour || input.End.After(time.Now()) {
		t.Fatal("invalid fixed read window")
	}
	if input.Executable == "" {
		input.Executable, err = exec.LookPath("lark-cli")
		if err != nil {
			t.Fatal("lark-cli unavailable")
		}
	}
	c, err := New(platform.CLIProfile{Version: 1, Executable: input.Executable, Profile: input.Profile, Brand: input.Brand, CLIVersion: CLIVersion})
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	v, err := c.Verify(ctx)
	if err != nil {
		t.Fatal(ErrorCode(err))
	}
	if v.Account.ID.Value != input.OpenID || v.Account.ID.Namespace != "lark:open_id:"+input.AppID {
		t.Fatal("private input account mismatch")
	}
	r := MessageSearchRequest{ChatType: "group", Start: input.Start, End: input.End, PageSize: 50}
	first, err := c.SearchMessages(ctx, v.Account, r)
	if err != nil {
		t.Fatal("group search:", ErrorCode(err))
	}
	if len(first.Items) == 0 || !first.HasMore {
		t.Fatal("fixed sample needs nonempty multi-page group results")
	}
	r.PageToken = first.PageToken
	second, err := c.SearchMessages(ctx, v.Account, r)
	if err != nil {
		t.Fatal("second page:", ErrorCode(err))
	}
	ids := make([]string, len(first.Items))
	for i, item := range first.Items {
		ids[i] = item.ID
	}
	before := time.Now()
	batch, err := c.BatchMessages(ctx, v.Account, ids)
	if err != nil || len(batch.MissingIDs) != 0 || len(batch.Items) != len(ids) {
		t.Fatal("batch details:", ErrorCode(err))
	}
	batchDuration := time.Since(before)
	var mentions, replies, posts int
	for _, m := range batch.Items {
		if len(m.Mentions) > 0 {
			mentions++
		}
		if m.ParentID != "" {
			replies++
		}
		if m.Type == "post" {
			posts++
		}
	}
	single, err := c.Message(ctx, v.Account, ids[0])
	if err != nil {
		t.Fatal("single detail:", ErrorCode(err))
	}
	fromBatch := batch.Items[0]
	fromBatch.Sender.Name, single.Sender.Name = "", ""
	if !reflect.DeepEqual(fromBatch, single) {
		t.Fatal("batch and single native message fields differ")
	}
	// Exercise private, mention, own-context and specified-scope wire parameters.
	counts := []int{}
	for _, q := range []MessageSearchRequest{
		{ChatType: "p2p", Start: input.Start, End: input.End, PageSize: 2},
		{ChatType: "group", IsAtMe: true, Start: input.Start, End: input.End, PageSize: 2},
		{ChatType: "group", FromID: input.OpenID, Start: input.Start, End: input.End, PageSize: 2},
		{ChatType: "group", ChatIDs: []string{first.Items[0].MetaData.ChatID}, Start: input.Start, End: input.End, PageSize: 2},
	} {
		page, err := c.SearchMessages(ctx, v.Account, q)
		if err != nil {
			t.Fatal("filter branch:", ErrorCode(err))
		}
		counts = append(counts, len(page.Items))
	}
	r.PageToken = "teamx-invalid-cursor"
	if _, err := c.SearchMessages(ctx, v.Account, r); ErrorCode(err) != "cursor_expired" {
		t.Fatal("invalid cursor:", ErrorCode(err))
	}
	if _, err := c.BatchMessages(ctx, v.Account, []string{ids[0], "om_00000000000000000000000000000000"}); ErrorCode(err) != "details_unavailable" {
		t.Fatal("unreadable batch:", ErrorCode(err))
	}
	t.Logf("bounded_group_pages=2 counts=%d/%d batch=%d batch_duration=%s mentions=%d replies=%d rich_posts=%d single_equivalent=true filter_counts=%v cursor_expired=true details_unavailable=true (coverage not exhausted)", len(first.Items), len(second.Items), len(batch.Items), batchDuration.Round(time.Millisecond), mentions, replies, posts, counts)
}
