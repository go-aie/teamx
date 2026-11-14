package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentNameIsIndependentMetadataAndDirectTitle(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "group", true: "direct"}[direct], func(t *testing.T) {
			root := t.TempDir()
			defaults := testTeamDefinition(t).Runtime
			var m *roomManager
			var entry roomSummary
			if direct {
				m = directTestManager(t, root, defaults)
				selection := directSelection("display-name")
				selection.Name = "我的助手"
				entry = createDirectConversation(t, m, selection)
			} else {
				m = testManager(t, root, managerTestProfiles(t))
				entry = managerCreate(t, m, "display-name")
			}
			var config managedRoomConfig
			if err := readSnapshot(filepath.Join(m.roomDir(entry.RoomID), "room-config.json"), &config, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			member := config.Members[0]
			before := map[string][]byte{}
			for _, file := range []string{"room-config.json", "room.json"} {
				data, err := os.ReadFile(filepath.Join(m.roomDir(entry.RoomID), file))
				if err != nil {
					t.Fatal(err)
				}
				before[file] = data
			}
			id := MemberIdentity{RoomIdentity: entry.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			expected := member.AgentID
			if direct {
				expected = entry.Name
			}
			req := map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "room_id": id.RoomID, "room_instance": id.RoomInstance, "agent_id": id.AgentID, "membership_id": id.MembershipID, "expected_name": expected, "agent_name": "编程 助手"}
			if w := managerHTTP(m, "/v1/member/name", req); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if got := m.agentNames.name(id); got != "编程 助手" {
				t.Fatal(got)
			}
			if direct && m.index.Rooms[0].Name != "编程 助手" || !direct && m.index.Rooms[0].Name != entry.Name {
				t.Fatal("conversation title authority is wrong")
			}
			if w := managerHTTP(m, "/v1/member/name", req); w.Code != 409 {
				t.Fatal("stale edit should conflict", w.Code, w.Body.String())
			}
			req["expected_name"] = "编程 助手"
			req["membership_id"] = "replacement"
			if w := managerHTTP(m, "/v1/member/name", req); w.Code != 403 {
				t.Fatal("replacement identity accepted", w.Code, w.Body.String())
			}
			for file, data := range before {
				after, err := os.ReadFile(filepath.Join(m.roomDir(entry.RoomID), file))
				if err != nil || !bytes.Equal(data, after) {
					t.Fatal("renaming changed execution state", file, err)
				}
			}
			if direct {
				w := managerHTTP(m, "/v1/rooms/rename", roomRenameRequest{m.epoch, humanCaller, id.RoomIdentity, "新的私信标题", "编程 助手"})
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				if m.agentNames.name(id) != "新的私信标题" {
					t.Fatal("direct title did not rename Agent")
				}
			}
			if err := m.validateIndex(); err != nil {
				t.Fatal(err)
			}
			want := m.agentNames.name(id)
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored, err := newRoomManager(root, 7)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.close()
			if restored.agentNames.name(id) != want {
				t.Fatal("name lost on restart")
			}
			if !direct && restored.agentNames.name(MemberIdentity{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: "replacement"}) != id.AgentID {
				t.Fatal("name leaked into a replacement member")
			}
		})
	}
}

func TestMentionIdentitySurvivesRenamingAndUnicode(t *testing.T) {
	state := historyRoom(0, "")
	h := newRoomHarness(t, state, nil)
	registry := &agentNameRegistry{}
	names := map[RoomIdentity]roomAgentNames{h.id: {Members: map[string]agentDisplayName{"generation-A": {AgentID: "A", AgentName: "编程助手"}}}}
	registry.value.Store(&names)
	s := &service{info: ServiceInfo{RoomIdentity: h.id}, room: h.addr, agentNames: registry}
	text := "🙂 @编程助手 请确认"
	mentions, err := s.inferMessageMentions(context.Background(), text)
	if err != nil || len(mentions) != 1 {
		t.Fatal(mentions, err)
	}
	if mentions[0].Start != 3 || mentions[0].End != 8 || mentions[0].MembershipID != "generation-A" {
		t.Fatal(mentions)
	}
	msg := PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "bound-mention", Text: text, Mentions: mentions}
	receipt := roomCall[PostReceipt](t, h, msg)
	names2 := map[RoomIdentity]roomAgentNames{h.id: {Members: map[string]agentDisplayName{"generation-A": {AgentID: "A", AgentName: "新的名称"}}}}
	registry.value.Store(&names2)
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
	labelled := s.labelMessage(page)
	if labelled.Messages[0].Text != text || labelled.Messages[0].Mentions[0].AgentName != "新的名称" || labelled.Messages[0].Mentions[0].MembershipID != "generation-A" {
		t.Fatal(labelled)
	}
	snapshot, err := h.store.LoadRoom()
	if err != nil || snapshot.Messages[0].Mentions[0].AgentName != "" || snapshot.Messages[0].Text != text {
		t.Fatal("source message was rewritten", err)
	}
	if duplicate := roomCall[PostReceipt](t, h, msg); duplicate.Seq != receipt.Seq {
		t.Fatal("rename broke idempotency")
	}
	bad := msg
	bad.Mentions = append([]MessageMention{}, mentions...)
	bad.Mentions[0].MembershipID = "replacement"
	if _, err := h.addr.Call[PostReceipt](h.ctx, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("idempotency accepted another mention target", err)
	}
	bad.ClientMessageID = "stale-mention"
	if _, err := h.addr.Call[PostReceipt](h.ctx, bad); !errors.Is(err, ErrIdentity) {
		t.Fatal("stale member accepted", err)
	}
	raw, _ := json.Marshal(labelled)
	if !bytes.Contains(raw, []byte(`"membership_id":"generation-A"`)) {
		t.Fatal(string(raw))
	}
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: Caller{Author: Author{Kind: "agent", ID: "A"}, MembershipID: "generation-A"}, ClientMessageID: "old-author", Text: "原成员发言"})
	roomCall[MemberInfo](t, h, RemoveMember{h.memberID("A", "generation-A")})
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID("A", "replacement"), Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	page = s.labelMessage(roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll}))
	if page.Messages[0].Mentions[0].MembershipID != "generation-A" || page.Messages[0].Mentions[0].AgentName != "新的名称" || page.Messages[1].AuthorMembershipID != "generation-A" || page.Messages[1].AgentName != "新的名称" {
		t.Fatal("replacement changed the historical member identity", page)
	}
}

func TestMentionRangesRejectMalformedMetadata(t *testing.T) {
	good := MessageMention{Start: 0, End: 2, Kind: "agent", ID: "A", MembershipID: "generation-A"}
	if !validMentions("@A", []MessageMention{good}) {
		t.Fatal("valid range rejected")
	}
	for _, bad := range []MessageMention{{Start: -1, End: 2, Kind: "agent", ID: "A", MembershipID: "generation-A"}, {Start: 0, End: 9, Kind: "agent", ID: "A", MembershipID: "generation-A"}, {Start: 0, End: 2, Kind: "agent", ID: "A"}, {Start: 0, End: 2, Kind: "system", ID: "A"}} {
		if validMentions("@A", []MessageMention{bad}) {
			t.Fatal(bad)
		}
	}
	if validMentions("@A", []MessageMention{good, good}) {
		t.Fatal("overlapping mentions accepted")
	}
}
