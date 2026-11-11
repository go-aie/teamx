package room

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRoomMessagePreviewIsSingleLineAndBounded(t *testing.T) {
	author := Author{Kind: "agent", ID: "developer"}
	for _, sample := range []struct{ text, want string }{
		{"  first\n\tsecond\r\nthird  ", "first second third"},
		{strings.Repeat("好🤖", 100), strings.Repeat("好🤖", 80) + "…"},
		{" \n\t", ""},
	} {
		got := summarizeRoomMessage(RoomMessage{Author: author, Text: sample.text})
		if got.Author != author || got.Text != sample.want || !utf8.ValidString(got.Text) {
			t.Fatalf("preview = %+v, want %v: %q", got, author, sample.want)
		}
	}
}
