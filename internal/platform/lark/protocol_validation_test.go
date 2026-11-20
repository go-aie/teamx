package lark

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCLIErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		input, code string
		native      int
	}{
		{`{`, "query_failed", 0},
		{`{"error":{"type":"config","subtype":"invalid_client"}}`, "credential_rejected", 0},
		{`{"error":{"type":"config"}}`, "credential_unavailable", 0},
		{`{"error":{"type":"authentication"}}`, "credential_rejected", 0},
		{`{"error":{"type":"authentication","subtype":"refresh_server_error"}}`, "network_error", 0},
		{`{"error":{"type":"authorization"}}`, "permission_denied", 0},
		{`{"error":{"type":"permission"}}`, "permission_denied", 0},
		{`{"error":{"type":"network","subtype":"timeout"}}`, "timeout", 0},
		{`{"error":{"type":"network"}}`, "network_error", 0},
		{`{"error":{"subtype":"rate_limit"}}`, "rate_limited", 0},
		{`{"error":{"code":99991400}}`, "rate_limited", 99991400},
		{`{"error":{"type":"authentication","code":1020}}`, "cursor_expired", 1020},
		{`{"error":{"code":54321,"message":"private-secret-body"}}`, "query_failed", 54321},
	} {
		t.Run(tc.input, func(t *testing.T) {
			err := classify([]byte(tc.input))
			var native *Error
			if ErrorCode(err) != tc.code || !errors.As(err, &native) || native.NativeCode != tc.native || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
		})
	}
}

func TestDecodeSingleJSONValue(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"items":[]}`, true}, {" {\"items\":[]} \n", true},
		{``, false}, {`{`, false}, {`{"items":"private"}`, false}, {`{"items":[]} {}`, false}, {`{"items":[]} private`, false},
	} {
		var wire messagePageWire
		if err := decode([]byte(tc.raw), &wire); (err == nil) != tc.valid || err != nil && (ErrorCode(err) != "invalid_response" || strings.Contains(err.Error(), "private")) {
			t.Fatal(tc.raw, err)
		}
	}
}

const historyPageJSON = `{"items":[{"message_id":"om_one","chat_id":"oc_chat","msg_type":"text","create_time":"100000","update_time":"100001","body":{"content":"private"}}],"has_more":true,"page_token":"next"}`

func TestMessagePageValidation(t *testing.T) {
	r := MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2, PageToken: "previous"}
	for name, change := range map[string]func(*messagePageWire){
		"valid":         func(w *messagePageWire) {},
		"missing_items": func(w *messagePageWire) { w.Items = nil },
		"missing_more":  func(w *messagePageWire) { w.HasMore = nil },
		"oversize":      func(w *messagePageWire) { *w.Items = append(*w.Items, (*w.Items)[0], (*w.Items)[0]) },
		"missing_token": func(w *messagePageWire) { w.PageToken = "" },
		"loop":          func(w *messagePageWire) { w.PageToken = r.PageToken },
		"long_token":    func(w *messagePageWire) { w.PageToken = strings.Repeat("x", 8193) },
		"wrong_chat":    func(w *messagePageWire) { (*w.Items)[0].ChatID = "oc_other" },
		"duplicate":     func(w *messagePageWire) { *w.Items = append(*w.Items, (*w.Items)[0]) },
		"bad_time":      func(w *messagePageWire) { (*w.Items)[0].Created = "oops" },
		"before_window": func(w *messagePageWire) { (*w.Items)[0].Created = "98999" },
		"after_window":  func(w *messagePageWire) { (*w.Items)[0].Created = "102000" },
		"unordered": func(w *messagePageWire) {
			m := (*w.Items)[0]
			m.ID = "om_two"
			m.Created = "99000"
			*w.Items = append(*w.Items, m)
		},
		"equal_time":      func(w *messagePageWire) { m := (*w.Items)[0]; m.ID = "om_two"; *w.Items = append(*w.Items, m) },
		"boundary_second": func(w *messagePageWire) { (*w.Items)[0].Created = "101999"; (*w.Items)[0].Updated = "" },
		"empty_final":     func(w *messagePageWire) { *w.Items = []Message{}; *w.HasMore = false; w.PageToken = "" },
	} {
		t.Run(name, func(t *testing.T) {
			var w messagePageWire
			if err := decode([]byte(historyPageJSON), &w); err != nil {
				t.Fatal(err)
			}
			change(&w)
			valid := name == "valid" || name == "equal_time" || name == "boundary_second" || name == "empty_final"
			if err := validateMessagePage(w, r); (err == nil) != valid {
				t.Fatal(err)
			}
		})
	}
	// Missing and explicit null wire fields must both remain distinguishable from an empty page.
	for _, raw := range []string{`{"has_more":false}`, `{"items":null,"has_more":false}`, `{"items":[]}`} {
		var w messagePageWire
		if err := decode([]byte(raw), &w); err != nil {
			t.Fatal(err)
		}
		if ErrorCode(validateMessagePage(w, r)) != "invalid_response" {
			t.Fatal(raw)
		}
	}
}

const searchPageJSON = `{"items":[{"id":"om_one","meta_data":{"message_id":"om_one","chat_id":"oc_chat","from_id":"ou_sender","create_time":"1970-01-01T00:01:40Z","is_p2p_chat":false}}],"has_more":true,"page_token":"next"}`

func TestMessageSearchPageValidation(t *testing.T) {
	r := searchRequest()
	r.ChatIDs = []string{"oc_chat"}
	r.FromID = "ou_sender"
	r.PageToken = "previous"
	for name, change := range map[string]func(*messageSearchWire){
		"valid":         func(w *messageSearchWire) {},
		"notice":        func(w *messageSearchWire) { w.Notice = "private incomplete" },
		"missing_items": func(w *messageSearchWire) { w.Items = nil },
		"missing_more":  func(w *messageSearchWire) { w.HasMore = nil },
		"missing_token": func(w *messageSearchWire) { w.PageToken = "" },
		"loop":          func(w *messageSearchWire) { w.PageToken = r.PageToken },
		"bad_token":     func(w *messageSearchWire) { w.PageToken = "bad\n" },
		"long_token":    func(w *messageSearchWire) { w.PageToken = strings.Repeat("x", 8193) },
		"oversize": func(w *messageSearchWire) {
			for range 51 {
				*w.Items = append(*w.Items, (*w.Items)[0])
			}
		},
		"duplicate":     func(w *messageSearchWire) { *w.Items = append(*w.Items, (*w.Items)[0]) },
		"bad_id":        func(w *messageSearchWire) { (*w.Items)[0].ID = "oc_wrong" },
		"different_id":  func(w *messageSearchWire) { (*w.Items)[0].MetaData.MessageID = "om_other" },
		"bad_chat":      func(w *messageSearchWire) { (*w.Items)[0].MetaData.ChatID = "om_wrong" },
		"wrong_chat":    func(w *messageSearchWire) { (*w.Items)[0].MetaData.ChatID = "oc_other" },
		"bad_sender":    func(w *messageSearchWire) { (*w.Items)[0].MetaData.FromID = "cli_bot" },
		"wrong_sender":  func(w *messageSearchWire) { (*w.Items)[0].MetaData.FromID = "ou_other" },
		"missing_type":  func(w *messageSearchWire) { (*w.Items)[0].MetaData.IsP2PChat = nil },
		"wrong_type":    func(w *messageSearchWire) { *(*w.Items)[0].MetaData.IsP2PChat = true },
		"bad_time":      func(w *messageSearchWire) { (*w.Items)[0].MetaData.Created = "100000" },
		"before_window": func(w *messageSearchWire) { (*w.Items)[0].MetaData.Created = "1970-01-01T00:01:38Z" },
		"after_window":  func(w *messageSearchWire) { (*w.Items)[0].MetaData.Created = "1970-01-01T00:01:42Z" },
		"unordered": func(w *messageSearchWire) {
			m := (*w.Items)[0]
			m.ID = "om_two"
			m.MetaData.MessageID = m.ID
			m.MetaData.Created = "1970-01-01T00:01:39Z"
			*w.Items = append(*w.Items, m)
		},
		"empty_more": func(w *messageSearchWire) { *w.Items = []MessageSearchItem{} },
	} {
		t.Run(name, func(t *testing.T) {
			var w messageSearchWire
			if err := decode([]byte(searchPageJSON), &w); err != nil {
				t.Fatal(err)
			}
			change(&w)
			err := validateMessageSearchPage(w, r)
			valid := name == "valid" || name == "unordered" || name == "empty_more"
			if (err == nil) != valid {
				t.Fatal(err)
			}
			if name == "notice" {
				var notice *SearchNoticeError
				if !errors.As(err, &notice) || notice.Notice != w.Notice || strings.Contains(err.Error(), "private") {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMessageBatchValidation(t *testing.T) {
	m := sampleMessage("om_one", "oc_chat", "private")
	for name, change := range map[string]func(*messageBatchWire){
		"valid":         func(w *messageBatchWire) {},
		"missing_items": func(w *messageBatchWire) { w.Items = nil },
		"empty":         func(w *messageBatchWire) { *w.Items = []json.RawMessage{} },
		"duplicate":     func(w *messageBatchWire) { *w.Items = append(*w.Items, (*w.Items)[0]) },
		"oversize":      func(w *messageBatchWire) { *w.Items = append(*w.Items, (*w.Items)[0], (*w.Items)[0]) },
		"unrequested": func(w *messageBatchWire) {
			(*w.Items)[0] = json.RawMessage(strings.ReplaceAll(string((*w.Items)[0]), "om_one", "om_other"))
		},
		"malformed": func(w *messageBatchWire) { (*w.Items)[0] = json.RawMessage(`{`) },
		"missing_body": func(w *messageBatchWire) {
			(*w.Items)[0] = json.RawMessage(`{"message_id":"om_one","chat_id":"oc_chat","msg_type":"text","create_time":"100000","sender":{}}`)
		},
		"missing_content": func(w *messageBatchWire) {
			(*w.Items)[0] = json.RawMessage(`{"message_id":"om_one","chat_id":"oc_chat","msg_type":"text","create_time":"100000","body":{},"sender":{}}`)
		},
		"missing_sender": func(w *messageBatchWire) {
			(*w.Items)[0] = json.RawMessage(`{"message_id":"om_one","chat_id":"oc_chat","msg_type":"text","create_time":"100000","body":{"content":"private"}}`)
		},
		"bad_time": func(w *messageBatchWire) {
			(*w.Items)[0] = json.RawMessage(strings.ReplaceAll(string((*w.Items)[0]), m.Created, "bad"))
		},
		"bad_reply": func(w *messageBatchWire) { bad := m; bad.ThreadID = "om_wrong"; (*w.Items)[0], _ = json.Marshal(bad) },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(m)
			items := []json.RawMessage{raw}
			w := messageBatchWire{Items: &items}
			change(&w)
			batch, err := validateMessageBatch(w, []string{"om_one", "om_two"})
			if name == "valid" {
				if err != nil || !reflect.DeepEqual(batch.Items, []Message{m}) || !reflect.DeepEqual(batch.MissingIDs, []string{"om_two"}) {
					t.Fatal(batch, err)
				}
			} else if name == "empty" {
				if err != nil || len(batch.Items) != 0 || !reflect.DeepEqual(batch.MissingIDs, []string{"om_one", "om_two"}) {
					t.Fatal(batch, err)
				}
			} else if ErrorCode(err) != "invalid_response" || !reflect.DeepEqual(batch, MessageBatch{}) {
				t.Fatal(batch, err)
			}
		})
	}
}

func TestSenderNamesValidation(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want map[string]string
		code string
	}{
		{`{"users":[{"user_id":"ou_two","name":"同名用户"},{"user_id":"ou_one","name":"同名用户"}]}`, map[string]string{"ou_one": "同名用户", "ou_two": "同名用户"}, ""},
		{`{"users":[{"user_id":"ou_two","name":"同名用户"}]}`, map[string]string{"ou_two": "同名用户"}, ""},
		{`{"users":[{"user_id":"ou_two","name":"bad\nname"},{"user_id":"ou_one","name":"有效"}]}`, map[string]string{"ou_one": "有效"}, ""},
		{`{"users":[{"user_id":"ou_other","name":"错误姓名"}]}`, nil, "invalid_response"},
		{`{"users":[{"user_id":"ou_one","name":"有效"},{"user_id":"ou_one","name":"重复"}]}`, nil, "invalid_response"},
		{`{"users":[{},{},{}]}`, nil, "invalid_response"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var w senderNamesWire
			if err := decode([]byte(tc.raw), &w); err != nil {
				t.Fatal(err)
			}
			names, err := validateSenderNames(w, []string{"ou_one", "ou_two"})
			if ErrorCode(err) != tc.code || !reflect.DeepEqual(names, tc.want) {
				t.Fatal(names, err)
			}
		})
	}
}

func TestMembershipPageValidation(t *testing.T) {
	account := sampleState(t).Binding.Account
	for name, change := range map[string]func(*membershipWire){
		"valid":         func(w *membershipWire) {},
		"missing_items": func(w *membershipWire) { w.Items = nil },
		"missing_more":  func(w *membershipWire) { w.More = nil },
		"missing_token": func(w *membershipWire) { w.Token = "" },
		"loop":          func(w *membershipWire) { w.Token = "previous" },
		"bad_token":     func(w *membershipWire) { w.Token = "bad\n" },
		"duplicate":     func(w *membershipWire) { *w.Items = append(*w.Items, (*w.Items)[0]) },
		"oversize": func(w *membershipWire) {
			for range 101 {
				*w.Items = append(*w.Items, (*w.Items)[0])
			}
		},
		"wrong_type":   func(w *membershipWire) { (*w.Items)[0].Type = "union_id" },
		"bad_id":       func(w *membershipWire) { (*w.Items)[0].ID = "cli_bot" },
		"wrong_tenant": func(w *membershipWire) { (*w.Items)[0].Tenant = "wrong" },
		"not_joined":   func(w *membershipWire) { (*w.Items)[0].ID = "ou_other" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := `{"items":[{"member_id":"` + account.ID.Value + `","member_id_type":"open_id","tenant_key":"` + account.Tenant.Value + `"}],"has_more":true,"page_token":"next"}`
			var w membershipWire
			if err := decode([]byte(raw), &w); err != nil {
				t.Fatal(err)
			}
			change(&w)
			page, err := validateMembershipPage(w, account, "previous")
			if name == "valid" || name == "not_joined" {
				if err != nil || page.Joined != (name == "valid") || !page.HasMore || page.PageToken != "next" {
					t.Fatal(page, err)
				}
			} else {
				code := "invalid_response"
				if name == "wrong_tenant" {
					code = "conversation_unverified"
				}
				if ErrorCode(err) != code || page != (MembershipPage{}) {
					t.Fatal(page, err)
				}
			}
		})
	}
}

func TestImageMessageValidation(t *testing.T) {
	for name, change := range map[string]func(*Message){
		"valid":         func(m *Message) {},
		"wrong_key":     func(m *Message) { m.Body.Content = `{"image_key":"img_other"}` },
		"wrong_chat":    func(m *Message) { m.ChatID = "oc_other" },
		"wrong_message": func(m *Message) { m.ID = "om_other" },
		"deleted":       func(m *Message) { m.Deleted = true },
		"invalid":       func(m *Message) { m.Created = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			m := sampleMessage("om_one", "oc_chat", "")
			m.Type = "image"
			m.Body.Content = `{"image_key":"img_one"}`
			change(&m)
			if validImageMessage([]Message{m}, "om_one", "oc_chat", "img_one") != (name == "valid") {
				t.Fatal("resource authority", name)
			}
			if validImageMessage(nil, "om_one", "oc_chat", "img_one") || validImageMessage([]Message{m, m}, "om_one", "oc_chat", "img_one") {
				t.Fatal("ambiguous message")
			}
		})
	}
}
