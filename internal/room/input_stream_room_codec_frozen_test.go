package room

import (
	"encoding/json"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/inputstreamtest/frozen"
)

// Room owns the native schema and index. The public query compiler never
// interprets event types or operation identities.
type roomInputCodec struct{}

func (roomInputCodec) Descriptor() inputstream.Descriptor {
	return inputstream.Descriptor{Provider: "room", Codec: "room/v1", Schema: 1}
}
func (roomInputCodec) Decode(schema int, payload json.RawMessage) (frozen.Projection, error) {
	var m RoomMessage
	if schema != 1 {
		return frozen.Projection{}, inputstream.ErrUnsupported
	}
	if err := inputstream.StrictDecode(payload, &m); err != nil {
		return frozen.Projection{}, err
	}
	return frozen.Projection{Text: m.Text}, nil
}
func (roomInputCodec) NewIndex() frozen.NativeIndex {
	return &roomInputIndex{map[inputstream.Location]RoomMessage{}}
}
func (roomInputCodec) DescribeInput() frozen.Guidance {
	return frozen.Guidance{Version: "room/v1", Text: `Room filter fields: entry_kind (message or event), event_type (exact saved event type), operation_id (exact operation). Event fields require entry_kind=event. Example: {"entry_kind":"event","event_type":"user_selection"}. Filtering discovers a stream; read delivers that stream in Room order, including earlier unmatched messages.`}
}

type roomInputIndex struct {
	entries map[inputstream.Location]RoomMessage
}

func (i *roomInputIndex) Clone() frozen.NativeIndex {
	n := &roomInputIndex{map[inputstream.Location]RoomMessage{}}
	for k, v := range i.entries {
		n.entries[k] = v
	}
	return n
}
func (i *roomInputIndex) Add(r inputstream.StoredRecord) error {
	var m RoomMessage
	if err := inputstream.StrictDecode(r.Payload, &m); err != nil {
		return err
	}
	i.entries[r.Location] = m
	return nil
}
func (i *roomInputIndex) CompileInputFilter(ctx frozen.FilterContext, b json.RawMessage) (frozen.Selection, error) {
	var q struct {
		Kind      string `json:"entry_kind,omitempty"`
		Type      string `json:"event_type,omitempty"`
		Operation string `json:"operation_id,omitempty"`
	}
	if err := frozen.ValidateFilter(b); err != nil {
		return frozen.Selection{}, err
	}
	if err := inputstream.StrictDecode(b, &q); err != nil {
		return frozen.Selection{}, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	for _, v := range fields {
		if string(v) == "null" || string(v) == `""` {
			return frozen.Selection{}, inputstream.ErrInvalid
		}
	}
	if q.Kind != "" && q.Kind != "message" && q.Kind != "event" || (q.Type != "" || q.Operation != "") && q.Kind != "event" {
		return frozen.Selection{}, inputstream.ErrConflict
	}
	out := frozen.Selection{}
	for _, l := range ctx.Allowed {
		m, ok := i.entries[l]
		if !ok {
			return out, inputstream.ErrCorrupt
		}
		kind := "message"
		if m.Event != nil {
			kind = "event"
		}
		if q.Kind != "" && kind != q.Kind {
			continue
		}
		if q.Type != "" && m.Event.Kind != q.Type || q.Operation != "" && m.Event.OperationID != q.Operation {
			continue
		}
		out.Entries = append(out.Entries, l)
	}
	return out, nil
}
