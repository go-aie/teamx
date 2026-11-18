package lark

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type inputPayload struct {
	Original                                                platform.SourceRef
	Route                                                   notification.Route
	DeliveryKey                                             string
	Instance, Tenant, Recipient, Subscription, App, Channel string
	ChatID, Chat, ThreadID, RootID, ReplyID, MessageID      string
	Evidence                                                inputEvidence
	Key, Order, URL, Text                                   string
	Content                                                 *notification.Content `json:",omitempty"`
}
type inputEvidence struct {
	Recipient, Tenant, Chat, Thread, Root, Reply platform.ExternalID
	Target                                       platform.ObjectReference
	Scope                                        string
	Version                                      int
}
type inputCodec struct{}

func (inputCodec) ValidateStoredRecord(rec inputstream.StoredRecord) error {
	p, err := decodeInput(rec.Schema, rec.Payload)
	if err != nil {
		return err
	}
	if p.Channel != rec.Channel || p.Original.Provider != "lark" || p.Original.Channel != rec.Channel {
		return inputstream.ErrIdentity
	}
	return nil
}

func inputCapability() *notification.InputCapability {
	return &notification.InputCapability{Codec: inputCodec{}, EncodeStoredInput: encodeInput, DecodeStoredInput: decodeStoredInput}
}
func (inputCodec) Descriptor() inputstream.Descriptor {
	return inputstream.Descriptor{Provider: "lark", Codec: "lark.notification", Schema: 1, GroupingVersion: 1, Channels: []string{"messages"}, Policy: inputstream.PlatformPolicy()}
}
func encodeInput(item notification.Item) (inputstream.AppendRequest, error) {
	s := item.Source
	r := s.Reference
	if s.Provider != "lark" {
		return inputstream.AppendRequest{}, inputstream.ErrIdentity
	}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: s.Provider, Instance: s.Instance, Tenant: r.AccountTenant}, ID: r.RecipientID}
	p := inputPayload{Original: s, Route: item.Route, DeliveryKey: item.Key, Instance: s.Instance, Tenant: notification.NativeID(r.AccountTenant), Subscription: s.SubscriptionID, Channel: s.Channel, Key: s.ItemKey, Order: item.Order, URL: s.URL, Text: item.Text, Content: item.Content}
	if ValidAccount(account, platform.CLIProfile{Brand: "feishu"}) || ValidAccount(account, platform.CLIProfile{Brand: "lark"}) {
		p.Recipient = notification.NativeID(r.RecipientID)
		p.App = strings.TrimPrefix(r.RecipientID.Namespace, "lark:open_id:")
		if r.Version == 2 && r.ChatID.Namespace == "lark:chat_id:"+p.App && nativeID(r.ChatID.Value, "oc_") {
			p.ChatID = r.ChatID.Value
		}
	}
	p.Evidence = inputEvidence{Recipient: r.RecipientID, Tenant: r.AccountTenant, Chat: r.ChatID, Thread: r.DiscussionID, Root: r.RootID, Reply: r.ParentID, Target: r.Target, Scope: r.Scope, Version: r.Version}
	if r.DiscussionID.Namespace == "lark:thread_id:"+p.App && nativeID(r.DiscussionID.Value, "omt_") {
		p.ThreadID = r.DiscussionID.Value
	}
	p.RootID = r.RootID.Value
	p.ReplyID = r.ParentID.Value
	p.MessageID = r.Target.ID.Value
	v := projectNotification(item)
	if v.Conversation != nil {
		p.Chat = v.Conversation.Name
	}
	b, _ := json.Marshal(p)
	if _, err := (inputCodec{}).Decode(1, b); err != nil {
		return inputstream.AppendRequest{}, err
	}
	item.Content = nil
	item.CreatedAt = item.CreatedAt.UTC()
	original, _ := json.Marshal(item)
	return inputstream.AppendRequest{DeliveryID: item.DeliveryID, Channel: s.Channel, CreatedAt: item.CreatedAt, Payload: b, DeliveryDigest: inputstream.Digest(original)}, nil
}
func decodeInput(schema int, b json.RawMessage) (inputPayload, error) {
	var p inputPayload
	if schema != 1 {
		return p, inputstream.ErrVersion
	}
	if err := inputstream.StrictDecode(b, &p); err != nil {
		return p, err
	}
	if p.Content != nil && !p.Content.Valid() {
		return p, inputstream.ErrInvalid
	}
	if p.Instance == "" || p.Subscription == "" || p.Channel != "messages" {
		return p, inputstream.ErrInvalid
	}
	return p, nil
}
func (inputCodec) Decode(schema int, b json.RawMessage) (inputstream.Projection, error) {
	p, err := decodeInput(schema, b)
	if err != nil {
		return inputstream.Projection{}, err
	}
	g := inputstream.Group{Version: 1, Instance: p.Instance, Tenant: p.Tenant, Account: p.Recipient, Subscription: p.Subscription, Precision: "source", Missing: "native_context_missing", Context: []string{"subscription", p.Subscription}}
	if p.Recipient != "" && p.ChatID != "" {
		g.Context = []string{inputstream.Ref("chat_", p.App, p.ChatID)}
		g.Precision = "context"
		g.Missing = ""
	}
	v := inputstream.Projection{Group: g, Label: p.Chat, SourceLabel: "飞书 " + p.Instance + " " + p.Evidence.Recipient.Value}
	if v.Label == "" {
		v.Label = p.ChatID
	}
	if p.Content != nil {
		v.Incomplete = p.Content.Truncated || p.Content.MetadataTruncated
	}
	return v, g.Validate()
}

// DecodeStoredInput restores native business evidence at the adapter boundary.
// The common envelope and reader never interpret the payload's native fields.
func decodeStoredInput(rec inputstream.StoredRecord) (notification.Item, error) {
	p, err := decodeInput(rec.Schema, rec.Payload)
	if err != nil {
		return notification.Item{}, err
	}
	if p.Original.Provider != "lark" || p.Original.Channel != rec.Channel || p.DeliveryKey == "" {
		return notification.Item{}, inputstream.ErrCorrupt
	}
	item := notification.Item{Key: p.DeliveryKey, DeliveryID: rec.DeliveryID, CreatedAt: rec.CreatedAt, Order: p.Order, Text: p.Text, Source: p.Original, Content: p.Content, Route: p.Route}
	request, err := encodeInput(item)
	if err != nil {
		return notification.Item{}, err
	}
	if request.DeliveryDigest != rec.DeliveryDigest || !bytes.Equal(request.Payload, rec.Payload) {
		return notification.Item{}, inputstream.ErrCorrupt
	}
	return item, nil
}
