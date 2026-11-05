package gitlab

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestNotificationImagesMarkdownSyntaxAndOrderedDeduplication(t *testing.T) {
	source, path := uploadFixture()
	body := "![first](" + path + "){width=900 height=536}\n\n" +
		"![other](" + strings.Replace(path, "image.png", "other.jpg", 1) + ")\n\n" +
		"![again](" + source.Instance + "/-/project/312" + path + ")\n\n" +
		"![full][shot] ![collapsed][] ![shortcut]\n\n" +
		"[shot]: " + path + "\n[collapsed]: " + path + "\n[shortcut]: " + path
	got := FindNotificationImages(notification.Quote(body, 16384), source, 20)
	if got.References != 6 || got.Omitted != 0 || len(got.Images) != 2 || !got.Images[0].Supported || !got.Images[1].Supported ||
		got.Images[1].Upload.Filename != "other.jpg" || !reflect.DeepEqual(got.Images[0].Positions, []int{1, 3, 4, 5, 6}) {
		t.Fatalf("%+v", got)
	}
}

func TestNotificationImagesExcludeCodeLinksAndIncompleteReferences(t *testing.T) {
	source, path := uploadFixture()
	for _, body := range []string{
		"`![inline](" + path + ")`", "```markdown\n![fenced](" + path + ")\n```",
		"~~~\n![fenced](" + path + ")\n~~~", "    ![indented](" + path + ")",
		"[ordinary](" + path + ")", path, "\\![escaped](" + path + ")",
		"![missing][unknown]", "![partial](" + path, "![partial][shot]\n\n[shot]: ",
	} {
		if got := FindNotificationImages(notification.Quote(body, 16384), source, 20); got.References != 0 {
			t.Fatalf("discovered non-image in %q: %+v", body, got)
		}
	}
	for _, quote := range []func(string, int) string{notification.Quote, func(s string, n int) string { return notification.QuoteExcerpt(s, n, false) }} {
		// A truncated reference definition remains syntactically valid Markdown;
		// it must not turn into a request for a guessed/partial filename.
		body := "![shot]\n\n[shot]: " + path + strings.Repeat("x", 500)
		got := FindNotificationImages(quote(body, 200), source, 20)
		for _, img := range got.Images {
			if img.Supported {
				t.Fatalf("partial path accepted: %+v", img)
			}
		}
	}
}

func TestNotificationImagesUnsupportedDestinationsAndReferenceBudget(t *testing.T) {
	source, path := uploadFixture()
	body := "![one](" + path + ") ![two](https://elsewhere.invalid" + path + ") ![three](/-/project/313" + path + ") ![four](" + path + ")"
	saved := notification.Quote(body, 16384)
	got := FindNotificationImages(saved, source, 3)
	if got.References != 4 || got.Omitted != 1 || len(got.Images) != 3 || !got.Images[0].Supported || got.Images[1].Supported || got.Images[2].Supported {
		t.Fatalf("%+v", got)
	}
	got = FindNotificationImages(saved, source, 0)
	if got.References != 4 || got.Omitted != 4 || len(got.Images) != 0 {
		t.Fatalf("%+v", got)
	}
	got = FindNotificationImages(notification.Quote(strings.Repeat("![s]("+path+") ", 110), 65536), source, 1000)
	if got.References != 110 || got.Omitted != 10 || len(got.Images) != 1 || len(got.Images[0].Positions) != MaxImageReferences {
		t.Fatalf("%+v", got)
	}
}

func TestGitLabBodyTruncationSurvivesNormalizationAndFormatting(t *testing.T) {
	var raw rawTodo
	if err := json.Unmarshal([]byte(rawTodoFixture), &raw); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body            string
		upstream, final bool
	}{
		{"short", false, false}, {strings.Repeat("x", 4096), false, true}, {strings.Repeat("x", 4097), true, true},
	} {
		raw.Body, _ = json.Marshal(tc.body)
		todo, err := normalizeTodo(raw, "https://gitlab.example.com", 101, TodoQuery{State: "pending"})
		if err != nil || todo.BodyTruncated != tc.upstream || len([]rune(todo.Body)) > 4096 {
			t.Fatal(todo, err)
		}
		// Persisted source state must carry the upstream fact without raw body.
		encoded, _ := json.Marshal(todo)
		var restored Todo
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		state := TodosState{}
		state.Binding.URL = "https://gitlab.example.com"
		text := TodosMessage(state, TodosInboxEntry{Todo: restored}).Text
		if strings.Contains(text, notification.TruncatedBodyNotice) != tc.final {
			t.Fatal(text)
		}
	}
	// Upstream loss remains visible even if the final saved excerpt is short,
	// and even when the saved body happens to equal the target title.
	todo := Todo{Body: "same", Title: "same", BodyTruncated: true}
	if text := TodosMessage(TodosState{}, TodosInboxEntry{Todo: todo}).Text; !strings.Contains(text, notification.TruncatedBodyNotice) {
		t.Fatal(text)
	}
	for _, body := range []string{"short", strings.Repeat("长", 2000)} {
		text := EventsMessage(EventsState{}, EventsInboxEntry{Event: Event{Kind: "note", NoteBody: body}}).Text
		if strings.Contains(text, notification.TruncatedBodyNotice) != (len(body) > 2048) {
			t.Fatal(text)
		}
	}
	// Legacy DTOs decode without inventing truncation from an ellipsis.
	var old Todo
	if err := json.Unmarshal([]byte(`{"body":"legacy…"}`), &old); err != nil || old.BodyTruncated {
		t.Fatal(old, err)
	}
}
