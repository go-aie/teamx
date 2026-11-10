package lark

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSearchPrivateAppMessagesDoNotBlockFollowingPeer(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	app := sampleMessage("om_app", "oc_bot", "app notification")
	app.Sender.Type = "app"
	app.Sender.IDType, app.Sender.ID = "app_id", "cli_bot"
	peer := sampleMessage("om_peer", "oc_peer", "person message")
	f.messages[app.ID], f.messages[peer.ID] = app, peer
	// There is no personnel relationship proof for oc_bot. Its native app
	// sender already excludes this message; do not infer a peer relationship.
	f.chats[peer.ChatID] = peerChat(peer.ChatID, "p2p")
	f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
		item := discoveryItem(app, true)
		item.MetaData.FromID = "ou_bot_open"
		return MessageSearchPage{Items: []MessageSearchItem{item, discoveryItem(peer, true)}}, nil
	}
	ingestionSteps(t, h, source, 16)
	if h.s.Search.Branches[0].Completed != h.now.Unix()-1 || len(h.s.Inbox) != 1 {
		t.Fatal("excluded app candidate blocked personnel progress")
	}
	for _, e := range h.s.Inbox {
		if e.Message.ID != peer.ID {
			t.Fatal("app message admitted as a personnel message")
		}
	}
	if f.chatReads != 1 || len(h.s.Chats) != 1 {
		t.Fatal("app exclusion invented or queried a personnel relationship")
	}
}

func TestSearchGroupAppMessagesKeepExistingRules(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Groups: GroupConfig{Enabled: true, Scope: "all", MessageMode: "all"}})
	m := sampleMessage("om_group_app", "oc_group", "group app notification")
	m.Sender.Type = "app"
	m.Sender.IDType, m.Sender.ID = "app_id", "cli_bot"
	f.messages[m.ID] = m
	f.chats[m.ChatID] = Chat{ID: m.ChatID, Mode: "group"}
	f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
		item := discoveryItem(m, false)
		item.MetaData.FromID = "ou_bot_open"
		return MessageSearchPage{Items: []MessageSearchItem{item}}, nil
	}
	ingestionSteps(t, h, source, 12)
	if len(h.s.Inbox) != 1 || len(f.members) != 1 {
		t.Fatal("private app exclusion changed verified group message behavior")
	}
}

func TestSearchCurrentWindowWaitsForIntervalAfterSlowRead(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	ingestionSteps(t, h, source, 2) // freeze and read an empty current window
	end := h.s.Search.Branches[0].Scan.End
	h.now = h.now.Add(2 * time.Second) // real CLI calls do not freeze wall time
	ingestionSteps(t, h, source, 1)
	b := h.s.Search.Branches[0]
	if b.Completed != end || !b.RetryAt.Equal(h.now.Add(time.Duration(h.s.Config.IntervalSeconds)*time.Second)) {
		t.Fatal("elapsed request time bypassed the configured polling interval")
	}
	count := len(f.requests)
	ingestionSteps(t, h, source, 3)
	if len(f.requests) != count {
		t.Fatal("empty current window was immediately searched again")
	}
	h.now = b.RetryAt
	ingestionSteps(t, h, source, 2)
	if len(f.requests) != count+1 {
		t.Fatal("polling did not resume after its configured interval")
	}
}

// Exercise multiple overlapping windows and pages with durable acknowledgements.
// Restart at every step, including immediately after admission and delivery.
func TestSearchBacklogRestartAndDelayedIndex(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	const count = 600
	baseline := h.s.Baseline.Unix()
	h.now = time.Unix(baseline+3601, 0)
	f.chats["oc_peer"] = peerChat("oc_peer", "p2p")
	for i := range count {
		m := sampleMessage(fmt.Sprintf("om_backlog_%04d", i), "oc_peer", "backlog")
		m.Created = strconv.FormatInt((baseline+int64(i)*6+1)*1000, 10)
		m.Updated = m.Created
		f.messages[m.ID] = m
	}
	// This message becomes searchable four minutes after creation. Initially it
	// is absent even though the latest window can complete successfully.
	delayed := sampleMessage("om_delayed", "oc_peer", "late index")
	delayed.Created = strconv.FormatInt((h.now.Unix()-120)*1000, 10)
	delayed.Updated = delayed.Created
	f.messages[delayed.ID] = delayed
	indexed := false
	f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
		var items []MessageSearchItem
		for _, m := range f.messages {
			at, _ := strconv.ParseInt(m.Created, 10, 64)
			if m.ID == delayed.ID && !indexed || at < q.Start.UnixMilli() || at > q.End.UnixMilli() {
				continue
			}
			items = append(items, discoveryItem(m, true))
		}
		// Search order deliberately differs from chronological order.
		slices.SortFunc(items, func(a, b MessageSearchItem) int { return -strings.Compare(a.ID, b.ID) })
		offset := 0
		if q.PageToken != "" {
			var err error
			offset, err = strconv.Atoi(q.PageToken)
			if err != nil || offset >= len(items) {
				t.Fatal("invalid synthetic page offset")
			}
		}
		end := min(offset+50, len(items))
		page := MessageSearchPage{Items: append([]MessageSearchItem{}, items[offset:end]...), HasMore: end < len(items)}
		if page.HasMore {
			page.PageToken = strconv.Itoa(end)
		}
		return page, nil
	}
	receipts := map[string]bool{}
	finish := func() {
		t.Helper()
		for step := range 4000 {
			ingestionSteps(t, h, source, 1)
			for _, item := range h.s.PendingItems() {
				if receipts[item.DeliveryID] {
					t.Fatal("a previously acknowledged revision was emitted again")
				}
				receipts[item.DeliveryID] = true
				h.s = h.s.Acknowledge(item.Key).(State)
			}
			if h.s.Search.Branches[0].Completed >= h.now.Unix()-1 {
				return
			}
			if step == 3999 {
				t.Fatal("backlog failed to make bounded progress")
			}
		}
	}
	finish()
	if len(receipts) != count || len(f.requests) < 10 || len(f.batches) < 10 {
		t.Fatalf("backlog coverage: receipts=%d pages=%d batches=%d", len(receipts), len(f.requests), len(f.batches))
	}
	indexed = true
	h.now = h.now.Add(2 * time.Minute)
	finish()
	if len(receipts) != count+1 || h.s.Metrics.NewItems != count+1 {
		t.Fatalf("overlap missed delayed index or duplicated messages: receipts=%d accepted=%d", len(receipts), h.s.Metrics.NewItems)
	}
	for _, q := range f.requests {
		if q.End.Sub(q.Start) > 15*time.Minute || q.Start.Unix() < baseline {
			t.Fatal("backlog escaped fixed windows or locked baseline")
		}
	}
}
