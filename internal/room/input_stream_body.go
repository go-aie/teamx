package room

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// These are explicit business DTOs. Persistence and native identity envelopes
// cannot acquire model-visible fields by adding JSON tags to storage types.
type inputBody struct {
	Context   string                  `json:"context,omitempty"`
	Entries   []any                   `json:"entries"`
	Remaining int                     `json:"remaining_entries"`
	Stop      string                  `json:"stop_reason,omitempty"`
	Followups []inputBusinessFollowup `json:"followups,omitempty"`
	Images    []streamImageCoverage   `json:"images,omitempty"`
}
type inputAuthor struct {
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
	ID       string `json:"id,omitempty"`
	Receiver bool   `json:"is_receiver,omitempty"`
	All      bool   `json:"all,omitempty"`
}
type inputNotificationBody struct {
	Text              string                    `json:"text"`
	Created           time.Time                 `json:"created_at"`
	Source            string                    `json:"source_ref"`
	Author            *inputAuthor              `json:"author,omitempty"`
	Mentions          []inputAuthor             `json:"mentions,omitempty"`
	Reply             string                    `json:"reply_to,omitempty"`
	Root              string                    `json:"root,omitempty"`
	Thread            string                    `json:"thread,omitempty"`
	ContextOnly       bool                      `json:"context_only,omitempty"`
	Action            string                    `json:"action,omitempty"`
	Changed           bool                      `json:"changed,omitempty"`
	Deleted           bool                      `json:"deleted,omitempty"`
	Truncated         bool                      `json:"truncated,omitempty"`
	MetadataTruncated bool                      `json:"metadata_truncated,omitempty"`
	Attachments       []notification.Attachment `json:"attachments,omitempty"`
	Todo              bool                      `json:"todo,omitempty"`
	Evidence          string                    `json:"evidence_note,omitempty"`
}
type inputRoomBody struct {
	Seq     uint64           `json:"seq"`
	Author  Author           `json:"author"`
	Name    string           `json:"agent_name,omitempty"`
	Text    string           `json:"text"`
	Created time.Time        `json:"created_at"`
	Reply   uint64           `json:"reply_to,omitempty"`
	Event   *agentRoomEvent  `json:"event,omitempty"`
	Source  *agentRoomSource `json:"external_source,omitempty"`
}
type inputBusinessFollowup struct {
	Ref          string                       `json:"unit_ref,omitempty"`
	Object       string                       `json:"object_ref,omitempty"`
	Status       string                       `json:"status,omitempty"`
	Summary      string                       `json:"summary,omitempty"`
	Next         string                       `json:"next,omitempty"`
	Questions    []followupQuestionSummary    `json:"questions,omitempty"`
	Instructions []followupInstructionSummary `json:"instructions,omitempty"`
	Actions      []referenceActionSummary     `json:"actions,omitempty"`
}

// The prepared business content is shared by text and image rendering.
type inputPageContent struct {
	room          bool
	roomEntries   []agentRoomMessage
	notifications []inputStreamNotificationEntry
	followups     []agentFollowupSummary
	images        []streamImageCoverage
	remaining     int
	stop          string
}

type inputPageRenderer func(inputstream.ReadAttempt, inputPageContent) (json.RawMessage, error)

func (s *inputSession) renderInputPage(a inputstream.ReadAttempt, page inputPageContent) (json.RawMessage, error) {
	body := projectInputBody(page)
	s.knownMu.Lock()
	known := s.known[a.Stream]
	s.knownMu.Unlock()
	if !known && len(body.Entries) > 0 {
		if page.room {
			body.Context = "当前 Room"
		} else {
			label, err := s.storage.repository.InputContext(s.execution, a.Stream)
			if err != nil {
				return nil, err
			}
			body.Context = label
		}
	}
	// Reproduce the JSON the delivery cache used to render after decoding
	// entries. Store exactly those bytes for receipt verification and replay.
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var rendered inputBody
	if err := json.Unmarshal(data, &rendered); err != nil {
		return nil, err
	}
	return json.Marshal(rendered)
}

func inputPerson(p notification.Participant) inputAuthor {
	return inputAuthor{Name: p.Name, Kind: p.Type, ID: p.ID.Value, Receiver: p.IsReceiver, All: p.All}
}
func projectInputBody(v inputPageContent) inputBody {
	out := inputBody{Entries: []any{}, Remaining: v.remaining}
	out.Images = append([]streamImageCoverage(nil), v.images...)
	for i := range out.Images {
		c := &out.Images[i]
		if v.room {
			_, _ = fmt.Sscanf(c.Source, "Room #%d", &c.RoomSeq)
			c.Source = ""
		}
		if strings.HasPrefix(c.SharedSource, "Room #") {
			_, _ = fmt.Sscanf(c.SharedSource, "Room #%d", &c.SharedRoom)
			c.SharedSource = ""
		}
	}
	if v.room {
		for _, m := range v.roomEntries {
			out.Entries = append(out.Entries, inputRoomBody{Seq: m.Seq, Author: m.Author, Name: m.AgentName, Text: m.Text, Created: m.CreatedAt, Reply: m.ReplyTo, Event: m.Event, Source: m.Source})
		}
	} else {
		for _, e := range v.notifications {
			n := e.Item
			b := inputNotificationBody{Text: n.Text, Created: n.CreatedAt, Source: e.Source.Ref, Reply: n.ReplyTo.Value, Root: n.RootID.Value, Thread: n.ThreadID.Value, ContextOnly: n.ContextOnly, Action: n.Action, Changed: n.Changed, Deleted: n.Deleted, Truncated: n.Truncated, MetadataTruncated: n.MetadataTruncated, Attachments: n.Attachments, Todo: n.Channel == "todos"}
			if n.Sender != nil {
				a := inputPerson(*n.Sender)
				b.Author = &a
			}
			for _, p := range n.Mentions {
				b.Mentions = append(b.Mentions, inputPerson(p))
			}
			if e.Source.Status != "" && e.Source.Status != "resolved" {
				b.Evidence = e.Source.Reason
				if b.Evidence == "" {
					b.Evidence = "Source evidence needs verification; inspect this source_ref before dependent actions."
				}
			}
			out.Entries = append(out.Entries, b)
		}
	}
	for _, f := range v.followups {
		if f.Summary == "" && f.Next == "" && len(f.Questions) == 0 && len(f.Instructions) == 0 && len(f.Actions) == 0 && (f.Status == "" || f.Status == "pending") {
			continue
		}
		out.Followups = append(out.Followups, inputBusinessFollowup{Ref: f.Ref, Object: f.ObjectRef, Status: f.Status, Summary: f.Summary, Next: f.Next, Questions: f.Questions, Instructions: f.Instructions, Actions: f.Actions})
	}
	if len(out.Entries) == 0 {
		if out.Remaining > 0 {
			out.Stop = "read_budget_exhausted"
		} else {
			out.Stop = "stream_empty"
		}
	}
	if v.stop == "read_budget_exhausted" {
		out.Stop = v.stop
	}
	return out
}
