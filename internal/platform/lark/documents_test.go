package lark

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocumentURLBoundary(t *testing.T) {
	for _, raw := range []string{
		"https://example.feishu.cn/docx/Doc_123?from=share#share-part",
		"https://example.larksuite.com/wiki/Doc_123/",
		"https://example.doubao.com/docx/Doc_123",
	} {
		got, err := NormalizeDocumentURL(raw)
		if err != nil || strings.ContainsAny(got, "?#") || !strings.HasSuffix(got, "/Doc_123") {
			t.Fatal(raw, got, err)
		}
	}
	for _, raw := range []string{"", "Doc123", "http://example.feishu.cn/docx/Doc", "file:///docx/Doc", "https://user:secret@example.feishu.cn/docx/Doc", "https://example.feishu.cn:8080/wiki/Doc", "https://example.feishu.cn/docx/", "https://example.feishu.cn/sheets/Doc", "https://example.feishu.cn/wiki/Doc/extra", "https://example.feishu.cn/docx/%2Fetc", "https://example.feishu.cn/docx/--profile=other", "https://example.feishu.cn/docx/Doc\n", "https://example.feishu.cn/docx/" + strings.Repeat("a", 257)} {
		if _, err := NormalizeDocumentURL(raw); ErrorCode(err) != "unsupported_document_url" {
			t.Fatal("accepted unsupported document URL", raw, err)
		}
	}
}

func TestDocumentResponsePreservesEvidenceAndCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name, raw       string
		valid, complete bool
	}{
		{"full", `{"document":{"document_id":"Doc","revision_id":2,"content":"# 正文","reference_map":{"comments":{"c1":{"data":"comment"}}}}}`, true, true},
		{"empty", `{"document":{"document_id":"Doc","content":""}}`, true, true},
		{"tips", `{"document":{"document_id":"Doc","content":"部分","tips":"degraded"}}`, true, false},
		{"warning", `{"document":{"document_id":"Doc","content":"部分"},"warnings":["limit"]}`, true, false},
		{"fragment", `{"document":{"document_id":"Doc","content":"<fragment>部分</fragment>"}}`, true, false},
		{"truncated", `{"document":{"document_id":"Doc","content":"部分","truncated":true}}`, true, false},
		{"more", `{"document":{"document_id":"Doc","content":"部分"},"has_more":true}`, true, false},
		{"missing", `{}`, false, false},
		{"missing content", `{"document":{"document_id":"Doc"}}`, false, false},
		{"null content", `{"document":{"document_id":"Doc","content":null}}`, false, false},
		{"wrong ID", `{"document":{"document_id":"Other","content":"private"}}`, false, false},
		{"malformed", `{"document":`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeDocumentResult("https://example.feishu.cn/docx/Doc", json.RawMessage(tc.raw))
			if (err == nil) != tc.valid || got.Complete != tc.complete || err != nil && len(got.Data) != 0 {
				t.Fatal(got, err)
			}
			if tc.valid && (string(got.Data) != tc.raw || got.Format != "markdown") {
				t.Fatal("lost original document/reference_map/tips", got)
			}
		})
	}
	// Wiki resolution may return a different native document ID.
	if _, err := decodeDocumentResult("https://example.feishu.cn/wiki/Node", json.RawMessage(`{"document":{"document_id":"Doc","content":"正文"}}`)); err != nil {
		t.Fatal(err)
	}
}
