//go:build e2e

package lark

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLarkSearchProbeMessageContract replays private native API payloads through
// the existing Message contract. It performs no network calls or state writes.
// A Python shape check alone does not establish compatibility with Go readers.
func TestLarkSearchProbeMessageContract(t *testing.T) {
	dir := os.Getenv("TEAMX_LARK_SEARCH_PROBE_OUTPUT")
	if dir == "" {
		t.Skip("set TEAMX_LARK_SEARCH_PROBE_OUTPUT to a completed read-only probe output")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	count, mentions, replies, posts := 0, 0, 0, 0
	for _, path := range files {
		if strings.HasSuffix(path, "summary.json") || strings.Contains(path, "comparison") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) > 4<<20 {
			t.Fatal("unreadable or oversized probe artifact")
		}
		var raw struct {
			Data struct {
				Items []json.RawMessage `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal("invalid probe artifact")
		}
		for _, item := range raw.Data.Items {
			var fields map[string]json.RawMessage
			if json.Unmarshal(item, &fields) != nil {
				t.Fatal("invalid item")
			}
			if _, ok := fields["body"]; !ok {
				continue // search IDs and membership evidence are not Message DTOs.
			}
			var m Message
			if json.Unmarshal(item, &m) != nil || !m.valid() {
				t.Fatal("native batch/history payload violates existing Message contract")
			}
			count++
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
	}
	if count == 0 {
		t.Fatal("no native messages to validate")
	}
	t.Logf("message samples=%d mentions=%d replies=%d rich_posts=%d (includes repeat reads)", count, mentions, replies, posts)
}
