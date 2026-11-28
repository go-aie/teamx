package scion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/go-aie/teamx/internal/platform"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func decodeRepositoryID(s string) RepositoryEvidence {
	var r RepositoryEvidence
	_ = inputstream.StrictDecode([]byte(s), &r)
	return r
}
func validRepositoryID(s, name string) bool {
	r := decodeRepositoryID(s)
	return r.Valid() && r.Name == name && repositoryID(r) == s
}
func nativeNumber(i notification.Item) int64 {
	n, _ := strconv.ParseInt(i.Source.Reference.Target.ID.Value, 10, 64)
	return n
}

type inputPayload struct {
	Item notification.Item `json:"item"`
}
type inputCodec struct{}

func (inputCodec) Descriptor() inputstream.Descriptor {
	return inputstream.Descriptor{Provider: "scion", Codec: "scion.notification", Schema: 1, GroupingVersion: 1, Channels: []string{"workitems"}, Policy: inputstream.PlatformPolicy()}
}
func validateItem(i notification.Item) error {
	s := i.Source
	r := s.Reference
	n, e := strconv.ParseInt(r.Target.ID.Value, 10, 64)
	if e != nil || n <= 0 || strconv.FormatInt(n, 10) != r.Target.ID.Value || s.Provider != "scion" || s.Channel != "workitems" || s.Object != "issue" || !validRepository(s.Project) || !validInstance(s.Instance) || s.SubscriptionID == "" || len(i.Key) != 64 || strings.Trim(i.Key, "0123456789abcdef") != "" || s.ItemKey != i.Key || r.Version != 1 || !r.Valid() || r.Scope != "created" || r.Target.Type != "issue" || r.Target.ID.Namespace != "scion:issue" || r.Object != r.Target || r.RecipientID.Namespace != "scion:subject" || !r.RecipientID.Valid() || r.ProjectID.Namespace != "scion:repository-composite" || !validRepositoryID(r.ProjectID.Value, s.Project) || s.URL != issueURL(s.Instance, s.Project, n) || i.CreatedAt.IsZero() || i.Key != creationKey(s.Instance, decodeRepositoryID(r.ProjectID.Value), n) || i.Order != fmt.Sprintf("%020d", n) || !utf8.ValidString(i.Text) || strings.ContainsRune(i.Text, 0) || i.Text == "" || len(i.Text) > 16<<10 || !i.Content.Valid() || !i.Route.Target.Valid() || i.Route.Target.Mode != "inbox" || i.Route.Version == 0 {
		return inputstream.ErrInvalid
	}
	repo := decodeRepositoryID(r.ProjectID.Value)
	expected := sourceRef(State{Repository: repo, SubscriptionID: s.SubscriptionID, Binding: Binding{Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Instance: s.Instance}, ID: r.RecipientID}}}, Issue{Number: n}, i.Key)
	if s != expected {
		return inputstream.ErrInvalid
	}
	if i.DeliveryID == "" {
		return inputstream.ErrInvalid
	}
	return nil
}
func decodePayload(schema int, b json.RawMessage) (notification.Item, error) {
	var p inputPayload
	if schema != 1 {
		return p.Item, inputstream.ErrVersion
	}
	if inputstream.StrictDecode(b, &p) != nil || validateItem(p.Item) != nil {
		return p.Item, inputstream.ErrInvalid
	}
	return p.Item, nil
}
func (inputCodec) Decode(schema int, b json.RawMessage) (inputstream.Projection, error) {
	i, e := decodePayload(schema, b)
	if e != nil {
		return inputstream.Projection{}, e
	}
	s := i.Source
	r := s.Reference
	g := inputstream.Group{Version: 1, Instance: s.Instance, Account: notification.NativeID(r.RecipientID), Subscription: s.SubscriptionID, Precision: "context", Context: []string{r.ProjectID.Value, "issue", r.Target.ID.Value}}
	return inputstream.Projection{Group: g, Label: s.Project + " · Issue #" + r.Target.ID.Value, SourceLabel: "Scion " + s.Instance, Incomplete: i.Content != nil && i.Content.Truncated}, g.Validate()
}
func (inputCodec) ValidateStoredRecord(rec inputstream.StoredRecord) error {
	_, e := decodeStoredInput(rec)
	return e
}
func encodeInput(i notification.Item) (inputstream.AppendRequest, error) {
	if e := validateItem(i); e != nil {
		return inputstream.AppendRequest{}, e
	}
	i.CreatedAt = i.CreatedAt.UTC()
	b, _ := json.Marshal(inputPayload{i})
	original := i
	original.Content = nil
	raw, _ := json.Marshal(original)
	return inputstream.AppendRequest{DeliveryID: i.DeliveryID, Channel: "workitems", CreatedAt: i.CreatedAt, Payload: b, DeliveryDigest: inputstream.Digest(raw)}, nil
}
func decodeStoredInput(rec inputstream.StoredRecord) (notification.Item, error) {
	i, e := decodePayload(rec.Schema, rec.Payload)
	if e != nil {
		return i, e
	}
	q, e := encodeInput(i)
	if e != nil || rec.Channel != q.Channel || rec.DeliveryID != q.DeliveryID || !rec.CreatedAt.Equal(q.CreatedAt) || rec.DeliveryDigest != q.DeliveryDigest || !bytes.Equal(rec.Payload, q.Payload) {
		return i, inputstream.ErrCorrupt
	}
	return i, nil
}
func NotificationCapabilities() notification.Capabilities {
	return notification.Capabilities{Project: notification.Project, Input: &notification.InputCapability{Codec: inputCodec{}, EncodeStoredInput: encodeInput, DecodeStoredInput: decodeStoredInput}}
}
