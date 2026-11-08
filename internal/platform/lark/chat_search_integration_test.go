//go:build integration || e2e

package lark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatSearchNativeContracts(t *testing.T) {
	parallelClientTest(t)
	c, dir, a := testClient(t)
	script := strings.Replace(fixture, "assert a[:2]==['api','GET'] or a[:2]==['api','POST']", chatSearchFixture+"\nassert a[:2]==['api','GET'] or a[:2]==['api','POST']", 1)
	if err := os.WriteFile(c.profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	q := ChatSearchRequest{Query: "研发", Type: "all"}
	for _, tc := range []struct {
		mode         string
		count        int
		source, code string
	}{
		{"ok", 4, "user", "refine_query"},
		{"duplicate", 3, "bot", "refine_query"},
		{"user_permission", 3, "user", "permission_denied"},
		{"bot_permission", 3, "bot", "permission_denied"},
		{"group_permission", 2, "group", "permission_denied"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			setMode(t, dir, tc.mode)
			page, err := c.SearchChats(context.Background(), a, q)
			if err != nil || len(page.Chats) != tc.count {
				t.Fatal(page, err)
			}
			found := false
			for _, w := range page.SearchWarnings {
				if w.Source == tc.source && w.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatal(page.SearchWarnings)
			}
		})
	}
	setMode(t, dir, "ok")
	page, err := c.SearchChats(context.Background(), a, ChatSearchRequest{Query: "研发", Type: "group", PageToken: "next"})
	if err != nil || page.HasMore || len(page.Chats) != 0 || len(page.SearchWarnings) != 0 {
		t.Fatal(page, err)
	}
	page, err = c.SearchChats(context.Background(), a, ChatSearchRequest{Query: "研发", Type: "p2p"})
	if err != nil || page.HasMore || len(page.Chats) != 2 || len(page.SearchWarnings) != 2 {
		t.Fatal(page, err)
	}
	for _, mode := range []string{"bad_mode", "bad_cursor", "bad_chat", "bad_name", "missing_more", "response_bot", "switch"} {
		t.Run(mode, func(t *testing.T) {
			_ = os.Remove(filepath.Join(dir, "read"))
			setMode(t, dir, mode)
			page, err := c.SearchChats(context.Background(), a, q)
			if err == nil || page.Chats != nil {
				t.Fatal("unverified result", page, err)
			}
			if mode == "switch" && ErrorCode(err) != "identity_mismatch" {
				t.Fatal(err)
			}
		})
	}
}

func TestChatSearchRejectsInvalidInputBeforeCLI(t *testing.T) {
	parallelClientTest(t)
	c, dir, a := testClient(t)
	for _, q := range []ChatSearchRequest{
		{Query: "", Type: "all"}, {Query: " ", Type: "all"}, {Query: strings.Repeat("字", 51), Type: "all"},
		{Query: "test\n", Type: "all"}, {Query: string([]byte{0xff}), Type: "all"}, {Query: "test", Type: "unknown"},
		{Query: "test", Type: "p2p", PageToken: "next"}, {Query: "test", Type: "group", PageToken: "bad\n"},
	} {
		if _, err := c.SearchChats(context.Background(), a, q); ErrorCode(err) != "invalid_request" {
			t.Fatal(q, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("invalid input reached CLI", err)
	}
}
