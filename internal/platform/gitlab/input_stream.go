package gitlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"strconv"
)

// inputPayload is GitLab-owned. Original source/business identity and attachment
// evidence remain local; none of these fields expand the public input schema.
type inputPayload struct {
	Original                                           platform.SourceRef
	Route                                              notification.Route
	DeliveryKey                                        string
	Instance, Tenant, Recipient, Subscription, Channel string
	ProjectID, Project, ObjectType, IID                string
	Key, Order, URL, Text, ObjectLabel                 string
	Evidence                                           inputEvidence
	Content                                            *notification.Content `json:",omitempty"`
}
type inputEvidence struct {
	Version                                      int
	Target, Object                               platform.ObjectReference
	Recipient, Project, Note, Discussion, Tenant platform.ExternalID
	Scope, Branch, Ref, RefType, SHA, BeforeSHA  string
	SourceProjectID                              int64
}

type inputCodec struct{}

func (inputCodec) ValidateStoredRecord(rec inputstream.StoredRecord) error {
	p, err := decodeInput(rec.Schema, rec.Payload)
	if err != nil {
		return err
	}
	if p.Channel != rec.Channel || p.Original.Provider != "gitlab" || p.Original.Channel != rec.Channel {
		return inputstream.ErrIdentity
	}
	return nil
}

func InputCapability() *notification.InputCapability {
	return &notification.InputCapability{Codec: inputCodec{}, EncodeStoredInput: encodeInput, DecodeStoredInput: decodeStoredInput}
}
func (inputCodec) Descriptor() inputstream.Descriptor {
	return inputstream.Descriptor{Provider: "gitlab", Codec: "gitlab.notification", Schema: 1, GroupingVersion: 1, Channels: []string{"events", "todos"}, Policy: inputstream.PlatformPolicy()}
}
func encodeInput(item notification.Item) (inputstream.AppendRequest, error) {
	s := item.Source
	r := s.Reference
	g := notification.StoredInputGroup(item)
	if s.Provider != "gitlab" {
		return inputstream.AppendRequest{}, inputstream.ErrIdentity
	}
	u, err := url.Parse(s.Instance)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return inputstream.AppendRequest{}, inputstream.ErrInvalid
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	if g.Tenant == "" {
		g.Tenant = notification.NativeID(s.Tenant)
	}
	p := inputPayload{Original: s, Route: item.Route, DeliveryKey: item.Key, Instance: u.String(), Tenant: g.Tenant, Subscription: s.SubscriptionID, Channel: s.Channel, Project: s.Project, Key: s.ItemKey, Order: item.Order, URL: s.URL, Text: item.Text, ObjectLabel: s.Object, Content: item.Content}
	if positiveREST(r.RecipientID, "id") {
		p.Recipient = g.Account
	}
	if r.Version == 1 && positiveREST(r.ProjectID, "id") {
		p.ProjectID = r.ProjectID.Value
	}
	switch strings.ToLower(r.Object.Type) {
	case "mergerequest", "merge_request":
		p.ObjectType = "merge_request"
	case "issue":
		p.ObjectType = "issue"
	}
	if r.Version == 1 && positiveREST(r.Object.IID, "iid") {
		p.IID = r.Object.IID.Value
	}
	p.Evidence = inputEvidence{Version: r.Version, Target: r.Target, Object: r.Object, Recipient: r.RecipientID, Project: r.ProjectID, Note: r.NoteID, Discussion: r.DiscussionID, Tenant: s.Tenant, Scope: r.Scope, Branch: r.Branch, Ref: r.Ref, RefType: r.RefType, SHA: r.SHA, BeforeSHA: r.BeforeSHA, SourceProjectID: s.ProjectID}
	b, _ := json.Marshal(p)
	if _, err := (inputCodec{}).Decode(1, b); err != nil {
		return inputstream.AppendRequest{}, err
	}
	// Preserve original immutable delivery digest semantics: Content is supplemental.
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
	if p.ProjectID != "" && !inputPositiveNumber(p.ProjectID) || p.IID != "" && !inputPositiveNumber(p.IID) {
		return p, inputstream.ErrInvalid
	}
	if p.Instance == "" || p.Subscription == "" || (p.Channel != "events" && p.Channel != "todos") {
		return p, inputstream.ErrInvalid
	}
	return p, nil
}
func (inputCodec) Decode(schema int, b json.RawMessage) (inputstream.Projection, error) {
	p, err := decodeInput(schema, b)
	if err != nil {
		return inputstream.Projection{}, err
	}
	g := inputstream.Group{Version: 1, Instance: p.Instance, Tenant: p.Tenant, Account: p.Recipient, Subscription: p.Subscription, Context: []string{"subscription", p.Subscription}, Precision: "source", Missing: "native_context_missing"}
	if p.Recipient != "" && p.ProjectID != "" && p.ObjectType != "" && p.IID != "" {
		g.Context = []string{p.ProjectID, p.ObjectType, p.IID}
		g.Precision = "context"
		g.Missing = ""
	}
	label := p.ObjectLabel
	incomplete := false
	if p.Content != nil {
		if p.Content.Context != nil && p.Content.Context.Name != "" {
			label = p.Content.Context.Name
		}
		incomplete = p.Content.Truncated || p.Content.MetadataTruncated
	}
	if label == "" {
		label = fmt.Sprintf("%s / %s %s", p.Project, p.ObjectType, p.IID)
	}
	if p.Project != "" && !strings.Contains(label, p.Project) {
		label = p.Project + " / " + label
	}
	projection := inputstream.Projection{Group: g, Label: label, SourceLabel: "GitLab " + p.Instance + " " + p.Evidence.Recipient.Value, Incomplete: incomplete}
	return projection, g.Validate()
}
func inputPositiveNumber(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

// DecodeStoredInput restores native business evidence at the adapter boundary.
// The common envelope and reader never interpret the payload's native fields.
func decodeStoredInput(rec inputstream.StoredRecord) (notification.Item, error) {
	p, err := decodeInput(rec.Schema, rec.Payload)
	if err != nil {
		return notification.Item{}, err
	}
	if p.Original.Provider != "gitlab" || p.Original.Channel != rec.Channel || p.DeliveryKey == "" {
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
