package room

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"
)

// Offsets use UTF-16 code units, the same coordinates as browser textareas.
// Names are read-time labels. Only identity and the original text range persist.
type MessageMention struct {
	Start        int    `json:"start"`
	End          int    `json:"end"`
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	MembershipID string `json:"membership_id,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
}

func validMentions(text string, mentions []MessageMention) bool {
	if len(mentions) > 128 {
		return false
	}
	units := utf16.Encode([]rune(text))
	end := 0
	for _, at := range mentions {
		if at.Start < end || at.End <= at.Start+1 || at.End > len(units) || units[at.Start] != '@' || !validText(at.ID) || at.AgentName != "" {
			return false
		}
		if at.Start > 0 && units[at.Start] >= 0xdc00 && units[at.Start] <= 0xdfff || at.End < len(units) && units[at.End] >= 0xdc00 && units[at.End] <= 0xdfff {
			return false
		}
		if at.Kind == "agent" {
			if !safeMembershipID(at.MembershipID) {
				return false
			}
		} else if at.Kind != "human" || at.MembershipID != "" {
			return false
		}
		end = at.End
	}
	return true
}
func mentionWord(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_@.+-", r)
}
func (s *service) inferMessageMentions(ctx context.Context, text string) ([]MessageMention, error) {
	list, err := s.room.Call[MemberList](ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return nil, err
	}
	labels := map[string][]MemberInfo{}
	for _, member := range list.Members {
		for _, label := range slices.Compact([]string{member.AgentID, s.agentName(member.AgentID, member.MembershipID)}) {
			labels[label] = append(labels[label], member)
		}
	}
	type match struct {
		MessageMention
	}
	var matches []match
	for label, members := range labels {
		if len(members) != 1 {
			continue
		} // A plain ambiguous name never chooses a target.
		token := "@" + label
		for offset := 0; offset < len(text); {
			found := strings.Index(text[offset:], token)
			if found < 0 {
				break
			}
			start := offset + found
			end := start + len(token)
			offset = end
			before, after := []rune(text[:start]), []rune(text[end:])
			if len(before) > 0 && mentionWord(before[len(before)-1]) || len(after) > 0 && mentionWord(after[0]) {
				continue
			}
			member := members[0]
			matches = append(matches, match{MessageMention: MessageMention{Start: len(utf16.Encode(before)), End: len(utf16.Encode([]rune(text[:end]))), Kind: "agent", ID: member.AgentID, MembershipID: member.MembershipID}})
		}
	}
	slices.SortFunc(matches, func(a, b match) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return b.End - a.End
	})
	var result []MessageMention
	end := 0
	for _, m := range matches {
		if m.Start < end {
			continue
		}
		result = append(result, m.MessageMention)
		end = m.End
		if len(result) == 128 {
			break
		}
	}
	return result, nil
}
func (s *service) labelMessage(page ReadPage) ReadPage {
	for i := range page.Messages {
		message := &page.Messages[i]
		if message.Author.Kind == "agent" {
			message.AgentName = s.agentName(message.Author.ID, message.AuthorMembershipID)
		}
		message.Mentions = slices.Clone(message.Mentions)
		for j := range message.Mentions {
			mention := &message.Mentions[j]
			if mention.Kind == "agent" {
				mention.AgentName = s.agentName(mention.ID, mention.MembershipID)
			}
		}
	}
	return page
}

// A list preview uses current labels without rewriting the saved source text.
func (m *roomManager) displayMentionText(room RoomIdentity, message RoomMessage) string {
	return renderMentionText(message, func(at MessageMention) string {
		return m.agentNames.name(MemberIdentity{RoomIdentity: room, AgentID: at.ID, MembershipID: at.MembershipID})
	})
}

func renderMentionText(message RoomMessage, agentName func(MessageMention) string) string {
	if len(message.Mentions) == 0 {
		return message.Text
	}
	units := utf16.Encode([]rune(message.Text))
	var out strings.Builder
	end := 0
	for _, at := range message.Mentions {
		out.WriteString(string(utf16.Decode(units[end:at.Start])))
		if at.Kind == "agent" {
			out.WriteString("@" + agentName(at))
		} else {
			out.WriteString(string(utf16.Decode(units[at.Start:at.End])))
		}
		end = at.End
	}
	out.WriteString(string(utf16.Decode(units[end:])))
	return out.String()
}
