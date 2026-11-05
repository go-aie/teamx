package notification

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestQuoteExcerptRetainsBudgetAndTruncationEvidence(t *testing.T) {
	for _, body := range []string{strings.Repeat("长", 1000), strings.Repeat("![](*)", 600), strings.Repeat("line\n", 600)} {
		got := QuoteExcerpt(body, 2048, false)
		if len(got) > 2048 || !utf8.ValidString(got) || !strings.HasSuffix(got, "> "+TruncatedBodyNotice) {
			t.Fatal("excerpt lost its budget or notice", len(got), got)
		}
	}
	if got := QuoteExcerpt("short upstream excerpt", 2048, true); !strings.Contains(got, "short upstream excerpt") || !strings.HasSuffix(got, TruncatedBodyNotice) {
		t.Fatal(got)
	}
	for _, body := range []string{"", "unchanged…", "![screenshot](/uploads/path.png)", "```\ncode\n```"} {
		if got := QuoteExcerpt(body, 2048, false); got != Quote(body, 2048) {
			t.Fatal("untruncated body changed", got)
		}
	}
}
