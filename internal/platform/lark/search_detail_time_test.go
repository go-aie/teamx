package lark

import (
	"testing"
	"time"
)

func TestSearchDetailAppAndUserIdentityNamespaces(t *testing.T) {
	m := sampleMessage("om_app", "oc_chat", "app message")
	item := discoveryItem(m, false)
	q := MessageSearchRequest{ChatType: "group", Start: time.Unix(1000, 0), End: time.Unix(1010, 0), PageSize: 50}
	for _, tc := range []struct {
		sender Sender
		want   bool
	}{
		{Sender{Type: "user", IDType: "open_id", ID: "ou_sender"}, true},
		{Sender{Type: "user", IDType: "open_id", ID: "ou_other"}, false},
		{Sender{Type: "app", IDType: "open_id", ID: "ou_other"}, false},
		{Sender{Type: "app", IDType: "app_id", ID: "cli_bot"}, true},
		{Sender{Type: "user", IDType: "app_id", ID: "cli_bot"}, false},
		{Sender{Type: "app", IDType: "unknown", ID: "cli_bot"}, false},
		{Sender{Type: "app", IDType: "app_id", ID: "ou_sender"}, false},
	} {
		m.Sender = tc.sender
		if got := searchDetailMatches(item, m, q); got != tc.want {
			t.Errorf("sender type=%s namespace=%s: got %v want %v", tc.sender.Type, tc.sender.IDType, got, tc.want)
		}
	}
	m.Sender = Sender{Type: "app", IDType: "app_id", ID: "cli_bot"}
	m.ChatID = "oc_other"
	if searchDetailMatches(item, m, q) {
		t.Fatal("app namespace difference waived native chat locator")
	}
}

func TestSearchDetailNativeTimestampPrecision(t *testing.T) {
	m := Message{ID: "om_time", ChatID: "oc_chat", Type: "text", Created: "100555", Sender: Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}}
	m.Body.Content = `{"text":"sample"}`
	item := discoveryItem(m, false)
	q := MessageSearchRequest{ChatType: "group", Start: time.Unix(99, 0), End: time.Unix(102, 0), PageSize: 50}
	for _, tc := range []struct {
		created string
		want    bool
	}{
		{"1970-01-01T00:01:40Z", true},
		{"1970-01-01T08:01:40+08:00", true},
		{"1970-01-01T00:01:40.555Z", true},
		{"1970-01-01T00:01:40.000Z", false},
		{"1970-01-01T00:01:40.554Z", false},
		{"1970-01-01T00:01:41Z", false},
	} {
		item.MetaData.Created = tc.created
		if got := searchDetailMatches(item, m, q); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.created, got, tc.want)
		}
	}
	item.MetaData.Created = "1970-01-01T00:01:40Z"
	q.End = time.Unix(100, 0)
	if !searchDetailMatches(item, m, q) {
		t.Fatal("native upper-second candidate cannot be deferred")
	}
	q.End = time.Unix(102, 0)
	q.Start = time.Unix(100, 556000000)
	if searchDetailMatches(item, m, q) {
		t.Fatal("detail before frozen window admitted")
	}
	q.Start = time.Unix(99, 0)
	m.Sender.ID = "ou_other"
	if searchDetailMatches(item, m, q) {
		t.Fatal("timestamp precision waived sender identity")
	}
}

func TestSearchUpperSecondDefersWithoutDedupUntilNextWindow(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Groups: GroupConfig{Enabled: true, Scope: "all", MessageMode: "all"}})
	m := sampleMessage("om_boundary", "oc_group", "boundary")
	m.Created, m.Updated = "1009555", "1009555"
	f.messages[m.ID] = m
	f.chats[m.ChatID] = Chat{ID: m.ChatID, Mode: "group"}
	item := discoveryItem(m, false)
	item.MetaData.Created = time.Unix(1009, 0).UTC().Format(time.RFC3339)
	f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
		return MessageSearchPage{Items: []MessageSearchItem{item}}, nil
	}
	ingestionSteps(t, h, source, 6)
	if h.s.Search.Branches[0].Completed != 1009 || len(h.s.Inbox) != 0 || len(h.s.Observed) != 0 || len(h.s.Accepted) != 0 {
		t.Fatal("upper-second candidate blocked progress or became observed")
	}
	h.now = time.Unix(1020, 0)
	ingestionSteps(t, h, source, 10)
	if len(h.s.Inbox) != 1 || len(h.s.Accepted) != 1 || h.s.Search.Branches[0].Completed != 1019 {
		t.Fatal("deferred candidate not admitted in next overlap")
	}
}
