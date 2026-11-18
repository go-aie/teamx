package inputstream

import (
	"encoding/json"
	"strings"
)

const Contract = "inbox_streams/v3"

type ListRequest struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}
type StreamReadRequest struct {
	Stream string `json:"stream_ref"`
	Limit  int    `json:"limit,omitempty"`
}
type Directory struct {
	Pending int             `json:"pending_streams"`
	Streams []StreamSummary `json:"streams"`
	Next    string          `json:"next_cursor,omitempty"`
	Stop    string          `json:"stop_reason,omitempty"`
}

func DecodeList(data []byte) (ListRequest, error) {
	var q ListRequest
	if err := ValidateRequest(data); err != nil {
		return q, err
	}
	if err := StrictDecode(data, &q); err != nil {
		return q, err
	}
	var f map[string]json.RawMessage
	_ = json.Unmarshal(data, &f)
	if q.Limit < 0 || q.Limit > 20 || len(q.Cursor) > 128 || len(f["limit"]) > 0 && q.Limit == 0 || len(f["cursor"]) > 0 && (!validKey(q.Cursor) || strings.TrimSpace(q.Cursor) == "") || q.Cursor != "" && len(f) != 1 {
		return q, ErrInvalid
	}
	return q, nil
}
func DecodeStreamRead(data []byte) (StreamReadRequest, error) {
	var q StreamReadRequest
	if err := ValidateRequest(data); err != nil {
		return q, err
	}
	if err := StrictDecode(data, &q); err != nil {
		return q, err
	}
	var f map[string]json.RawMessage
	_ = json.Unmarshal(data, &f)
	if !validKey(q.Stream) || strings.TrimSpace(q.Stream) == "" || len(q.Stream) > 128 || q.Limit < 0 || q.Limit > 10000 || len(f["limit"]) > 0 && q.Limit == 0 {
		return q, ErrInvalid
	}
	return q, nil
}
