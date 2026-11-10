package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// MessageSearchRequest describes one frozen search page. Times are ISO 8601 on
// the wire, independently of history's Unix seconds and Message's milliseconds.
// FromID is a single sender, sufficient for the own-context branch.
type MessageSearchRequest struct {
	ChatType  string
	ChatIDs   []string
	FromID    string
	IsAtMe    bool
	Start     time.Time
	End       time.Time
	PageSize  int
	PageToken string
}

func validPageToken(token string) bool {
	return len(token) <= 8192 && !strings.ContainsAny(token, "\x00\r\n")
}

func uniqueNativeIDs(ids []string, prefix string, limit int) bool {
	if len(ids) > limit {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !nativeID(id, prefix) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (r MessageSearchRequest) Valid() bool {
	return (r.ChatType == "p2p" || r.ChatType == "group") &&
		uniqueNativeIDs(r.ChatIDs, "oc_", 32) && (r.FromID == "" || nativeID(r.FromID, "ou_")) &&
		!r.Start.IsZero() && r.Start.Unix() > 0 && r.End.After(r.Start) && r.End.Year() <= 9999 &&
		r.PageSize >= 1 && r.PageSize <= 50 && validPageToken(r.PageToken)
}

// MessageSearchItem is discovery evidence, never a complete Message or proof
// of joined-group/person-to-person membership. Display summaries are omitted.
type MessageSearchItem struct {
	ID       string `json:"id"`
	MetaData struct {
		MessageID string `json:"message_id"`
		ChatID    string `json:"chat_id"`
		FromID    string `json:"from_id"`
		Created   string `json:"create_time"`
		IsP2PChat *bool  `json:"is_p2p_chat"`
	} `json:"meta_data"`
}

type MessageSearchPage struct {
	Items     []MessageSearchItem `json:"items"`
	HasMore   bool                `json:"has_more"`
	PageToken string              `json:"page_token"`
}

// SearchNoticeError retains native completeness evidence without exposing it
// in error strings/logs. No candidate page escapes when a notice is present.
type SearchNoticeError struct{ Notice string }

func (e *SearchNoticeError) Error() string { return "lark read: search_incomplete" }
func (e *SearchNoticeError) Unwrap() error { return failure("search_incomplete") }

func (c *Client) SearchMessages(ctx context.Context, account platform.AccountIdentity, r MessageSearchRequest) (MessageSearchPage, error) {
	var page MessageSearchPage
	if !r.Valid() {
		return page, failure("invalid_request")
	}
	err := c.guarded(ctx, account, func(ctx context.Context) error {
		filter := map[string]any{
			"chat_type":  r.ChatType,
			"time_range": map[string]string{"start_time": r.Start.UTC().Format(time.RFC3339Nano), "end_time": r.End.UTC().Format(time.RFC3339Nano)},
		}
		if len(r.ChatIDs) > 0 {
			filter["chat_ids"] = r.ChatIDs
		}
		if r.FromID != "" {
			filter["from_ids"] = []string{r.FromID}
		}
		if r.IsAtMe {
			filter["is_at_me"] = true
		}
		params := map[string]any{"page_size": r.PageSize}
		if r.PageToken != "" {
			params["page_token"] = r.PageToken
		}
		p, _ := json.Marshal(params)
		body, _ := json.Marshal(map[string]any{"query": "", "filter": filter})
		var wire struct {
			Items     *[]MessageSearchItem `json:"items"`
			HasMore   *bool                `json:"has_more"`
			PageToken string               `json:"page_token"`
			Notice    string               `json:"notice"`
		}
		if err := c.apiJSON(ctx, []string{"api", "POST", "/open-apis/im/v1/messages/search", "--params", string(p), "--data", string(body)}, &wire); err != nil {
			return messageReadError(err)
		}
		if wire.Notice != "" {
			return &SearchNoticeError{Notice: wire.Notice}
		}
		if wire.Items == nil || wire.HasMore == nil || len(*wire.Items) > r.PageSize || !validPageToken(wire.PageToken) ||
			*wire.HasMore && (wire.PageToken == "" || wire.PageToken == r.PageToken) {
			return failure("invalid_response")
		}
		seen := map[string]bool{}
		for _, item := range *wire.Items {
			m := item.MetaData
			created, err := time.Parse(time.RFC3339Nano, m.Created)
			if !nativeID(item.ID, "om_") || item.ID != m.MessageID || !nativeID(m.ChatID, "oc_") ||
				!nativeID(m.FromID, "ou_") || m.IsP2PChat == nil || *m.IsP2PChat != (r.ChatType == "p2p") ||
				err != nil || created.Before(r.Start) || created.After(r.End) || seen[item.ID] || r.FromID != "" && m.FromID != r.FromID {
				return failure("invalid_response")
			}
			if len(r.ChatIDs) > 0 {
				found := false
				for _, id := range r.ChatIDs {
					found = found || id == m.ChatID
				}
				if !found {
					return failure("invalid_response")
				}
			}
			seen[item.ID] = true
		}
		page = MessageSearchPage{Items: *wire.Items, HasMore: *wire.HasMore, PageToken: wire.PageToken}
		return nil
	})
	if err != nil {
		return MessageSearchPage{}, err
	}
	return page, nil
}

// MessageBatch explicitly identifies successful-envelope omissions. Missing IDs
// remain unresolved, never interpreted as deleted. Items follow request order.
type MessageBatch struct {
	Items      []Message
	MissingIDs []string
}

// BatchMessages performs one mget of at most 50 IDs. It never splits/retries a
// failed batch or silently expands it into single reads. A caller can persist
// MissingIDs and budget a small number of Message calls in a later step.
func (c *Client) BatchMessages(ctx context.Context, account platform.AccountIdentity, ids []string) (MessageBatch, error) {
	var batch MessageBatch
	if len(ids) == 0 || !uniqueNativeIDs(ids, "om_", 50) {
		return batch, failure("invalid_request")
	}
	err := c.guarded(ctx, account, func(ctx context.Context) error {
		var wire struct {
			Items *[]json.RawMessage `json:"items"`
		}
		if err := c.api(ctx, "/open-apis/im/v1/messages/mget", map[string]any{"message_ids": ids}, &wire); err != nil {
			return messageReadError(err)
		}
		if wire.Items == nil || len(*wire.Items) > len(ids) {
			return failure("invalid_response")
		}
		requested := map[string]bool{}
		for _, id := range ids {
			requested[id] = true
		}
		messages := map[string]Message{}
		for _, raw := range *wire.Items {
			var m Message
			// Do not let absent native detail fields become zero-value content.
			// System records may have an empty sender, but must carry the object.
			var required struct {
				Body *struct {
					Content *string `json:"content"`
				} `json:"body"`
				Sender *Sender `json:"sender"`
			}
			if decode(raw, &m) != nil || decode(raw, &required) != nil || required.Body == nil ||
				required.Body.Content == nil || required.Sender == nil || !m.valid() || !requested[m.ID] || messages[m.ID].ID != "" {
				return failure("invalid_response")
			}
			messages[m.ID] = m
		}
		for _, id := range ids {
			if m, ok := messages[id]; ok {
				batch.Items = append(batch.Items, m)
			} else {
				batch.MissingIDs = append(batch.MissingIDs, id)
			}
		}
		c.senderNames(ctx, batch.Items)
		return nil
	})
	if err != nil {
		return MessageBatch{}, err
	}
	return batch, nil
}

// Only fixed message-read APIs interpret these native codes. In particular an
// unreadable detail does not imply withdrawal, and is not a cursor failure.
func messageReadError(err error) error {
	var e *Error
	if errors.As(err, &e) && e.Code == "query_failed" {
		switch e.NativeCode {
		case 1021, 99992402:
			return &Error{Code: "invalid_request", NativeCode: e.NativeCode}
		case 99992354:
			return &Error{Code: "details_unavailable", NativeCode: e.NativeCode}
		}
	}
	return err
}
