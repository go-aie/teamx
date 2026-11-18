package frozen

import (
	"encoding/json"
	"github.com/go-aie/teamx/internal/inputstream"
	"sort"
)

type larkQueryCodec struct{ inputstream.Codec }

type larkQueryRow struct {
	ChatID, Chat, ThreadID, Channel, Recipient, App, Text string
}

func (c larkQueryCodec) Decode(schema int, b json.RawMessage) (Projection, error) {
	metadata, err := c.Codec.Decode(schema, b)
	p := Projection{Projection: metadata}
	if err != nil {
		return p, err
	}
	var row larkQueryRow
	if err := json.Unmarshal(b, &row); err != nil {
		return p, err
	}
	p.Text = row.Text
	return p, nil
}

func (larkQueryCodec) DescribeInput() Guidance {
	return Guidance{Version: "lark/filter/v1", Text: `Within this saved account, filter accepts chat (exact saved name), chat_id, thread_id and channel=messages. Same-name chats return choices. thread_id selects a conversation stream without splitting it; read delivers its full context in order. Missing saved identities do not prove a native match. Help and discovery use local data and grant no remote permissions.`}
}

type larkInputIndex struct {
	rows map[inputstream.Location]larkQueryRow
}

func (larkQueryCodec) NewIndex() NativeIndex {
	return &larkInputIndex{rows: map[inputstream.Location]larkQueryRow{}}
}
func (i *larkInputIndex) Add(r inputstream.StoredRecord) error {
	var p larkQueryRow
	err := json.Unmarshal(r.Payload, &p)
	if err != nil {
		return err
	}
	if p.Channel != r.Channel {
		return inputstream.ErrIdentity
	}
	i.rows[r.Location] = p
	return nil
}
func (i *larkInputIndex) CompileInputFilter(c FilterContext, b json.RawMessage) (Selection, error) {
	var f struct {
		Chat     string `json:"chat"`
		ChatID   string `json:"chat_id"`
		ThreadID string `json:"thread_id"`
		Channel  string `json:"channel"`
	}
	if c.IndexVersion != 1 {
		return Selection{}, inputstream.ErrVersion
	}
	if err := DecodeFilter(b, &f); err != nil {
		return Selection{}, err
	}
	if f.Channel != "" && f.Channel != "messages" {
		return Selection{}, inputstream.ErrInvalid
	}
	names := map[string]string{}
	for _, l := range c.Allowed {
		p := i.rows[l]
		if p.ChatID != "" && p.Chat == f.Chat {
			names[p.ChatID] = p.Chat
		}
	}
	if f.Chat != "" && f.ChatID != "" {
		for _, l := range c.Allowed {
			p := i.rows[l]
			if p.ChatID == f.ChatID && p.Chat != f.Chat {
				return Selection{}, inputstream.ErrConflict
			}
		}
	}
	if f.Chat != "" && f.ChatID == "" {
		if len(names) > 1 {
			choices := []Scope{}
			ids := []string{}
			for id := range names {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			if len(ids) > 5 {
				ids = ids[:5]
			}
			for _, id := range ids {
				name := names[id]
				choices = append(choices, Scope{Key: inputstream.Ref("context_", []string{inputstream.Ref("chat_", i.appFor(c, id), id)}), Label: name + " / " + id})
			}
			return Selection{}, &ScopeError{Reason: ErrAmbiguous, Native: choices}
		}
		for id := range names {
			f.ChatID = id
		}
	}
	out := Selection{}
	for _, l := range c.Allowed {
		p, ok := i.rows[l]
		if !ok {
			return out, inputstream.ErrCorrupt
		}
		if (f.Chat != "" || f.ChatID != "" || f.ThreadID != "") && (p.ChatID == "" || p.Recipient == "") || f.ThreadID != "" && p.ThreadID == "" {
			out.Missing++
			continue
		}
		if f.Chat != "" && p.Chat != f.Chat || f.ChatID != "" && p.ChatID != f.ChatID || f.ThreadID != "" && p.ThreadID != f.ThreadID {
			continue
		}
		out.Entries = append(out.Entries, l)
	}
	return out, nil
}

func (i *larkInputIndex) Clone() NativeIndex {
	out := &larkInputIndex{rows: make(map[inputstream.Location]larkQueryRow, len(i.rows))}
	for k, v := range i.rows {
		out.rows[k] = v
	}
	return out
}

func (i *larkInputIndex) appFor(c FilterContext, chat string) string {
	for _, l := range c.Allowed {
		if p := i.rows[l]; p.ChatID == chat {
			return p.App
		}
	}
	return ""
}
