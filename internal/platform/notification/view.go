package notification

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

// Participant preserves native identity independently of its optional display
// name. Receiver is true only after comparing IDs in the same native scope.
type Participant struct {
	Name       string              `json:"name,omitempty"`
	ID         platform.ExternalID `json:"id,omitzero"`
	Type       string              `json:"type,omitempty"`
	Tenant     platform.ExternalID `json:"tenant,omitzero"`
	IsReceiver bool                `json:"is_receiver"`
	All        bool                `json:"all,omitempty"`
}

type Context struct {
	Name string              `json:"name,omitempty"`
	ID   platform.ExternalID `json:"id,omitzero"`
	Type string              `json:"type,omitempty"`
	URL  string              `json:"url,omitempty"`
}

type Attachment struct {
	Type      string `json:"type"`
	Reference string `json:"reference"`
	Status    string `json:"status"`
}

// Content is a bounded, immutable business snapshot produced by the adapter.
// Raw Text and Source remain the authority for delivery, replay and attachment
// admission. Older records retain their body and known envelope facts with
// structured=false; display labels are never guessed into native identities.
type Content struct {
	Text              string        `json:"text"`
	Structured        bool          `json:"structured"`
	Sender            *Participant  `json:"sender,omitempty"`
	Conversation      *Context      `json:"conversation,omitempty"`
	Context           *Context      `json:"context,omitempty"`
	Mentions          []Participant `json:"mentions,omitempty"`
	Attachments       []Attachment  `json:"attachments,omitempty"`
	Action            string        `json:"action,omitempty"`
	Changed           bool          `json:"changed,omitempty"`
	Deleted           bool          `json:"deleted,omitempty"`
	Truncated         bool          `json:"truncated,omitempty"`
	MetadataTruncated bool          `json:"metadata_truncated,omitempty"`
}

// View is shared by human preview and Agent consumption. Storage schema,
// subscription/delivery/dedup keys, routes and access snapshots stay internal.
// Native reply IDs are source evidence; verified followup refs travel alongside
// this view and remain the normal tool arguments.
type View struct {
	Content
	Provider  string                   `json:"provider"`
	Channel   string                   `json:"channel"`
	Instance  string                   `json:"instance"`
	Tenant    platform.ExternalID      `json:"tenant,omitzero"`
	Receiver  platform.AccountIdentity `json:"receiver,omitzero"`
	CreatedAt time.Time                `json:"created_at"`
	Project   string                   `json:"project,omitempty"`
	ProjectID int64                    `json:"project_id,omitempty"`
	MessageID platform.ExternalID      `json:"message_id,omitzero"`
	ReplyTo   platform.ExternalID      `json:"reply_to,omitzero"`
	RootID    platform.ExternalID      `json:"root_id,omitzero"`
	ThreadID  platform.ExternalID      `json:"thread_id,omitzero"`
}

func Project(item Item) View {
	s, r := item.Source, item.Source.Reference
	v := View{Content: Content{Text: item.Text}, Provider: s.Provider, Channel: s.Channel, Instance: s.Instance,
		Tenant: s.Tenant, CreatedAt: item.CreatedAt, Project: s.Project, ProjectID: s.ProjectID,
		ReplyTo: r.ParentID, RootID: r.RootID, ThreadID: r.DiscussionID}
	if r.RecipientID.Valid() {
		v.Receiver = platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: s.Provider, Instance: s.Instance, Tenant: r.AccountTenant}, ID: r.RecipientID}
	}
	if r.Target.Type == "message" {
		v.MessageID = r.Target.ID
	} else if r.NoteID.Valid() {
		v.MessageID = r.NoteID
	}
	if r.ChatID.Valid() {
		v.Conversation = &Context{ID: r.ChatID}
	}
	if item.Content != nil {
		// Detach slices/pointers from stored evidence before returning it.
		b, _ := json.Marshal(item.Content)
		_ = json.Unmarshal(b, &v.Content)
	}
	return v
}

// Content is supplemental evidence and fits inside the Inbox's existing entry
// budget. Validate before persistence as well as when loading old snapshots.
func (c *Content) Valid() bool {
	if c == nil {
		return true
	}
	validText := func(values ...string) bool {
		for _, value := range values {
			if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return false
			}
		}
		return true
	}
	if !validText(c.Text, c.Action) {
		return false
	}
	for _, context := range []*Context{c.Conversation, c.Context} {
		if context != nil && !validText(context.Name, context.Type, context.URL, context.ID.Namespace, context.ID.Value) {
			return false
		}
	}
	people := c.Mentions
	if c.Sender != nil {
		people = append(append([]Participant(nil), people...), *c.Sender)
	}
	for _, p := range people {
		if !validText(p.Name, p.Type, p.ID.Namespace, p.ID.Value, p.Tenant.Namespace, p.Tenant.Value) {
			return false
		}
	}
	for _, a := range c.Attachments {
		if !validText(a.Type, a.Reference, a.Status) {
			return false
		}
	}
	b, err := json.Marshal(c)
	return err == nil && len(b) <= 64<<10
}

func BoundedBody(text string, limit int) (string, bool) {
	text = strings.ReplaceAll(text, "\x00", "�")
	return boundedText(text, limit), len(text) > limit
}

// WithContent bounds the combined raw + business snapshot, including JSON
// escaping. A large presentation must not make a formerly deliverable notice
// exceed the Inbox's default entry budget. Missing native facts stay explicit.
func WithContent(item Item, content *Content) Item {
	item.Content = content
	if content == nil {
		return item
	}
	for {
		b, err := json.Marshal(item)
		if err == nil && len(b) <= 96<<10 && content.Valid() {
			return item
		}
		switch {
		case len(content.Mentions) > 0:
			content.Mentions = content.Mentions[:len(content.Mentions)/2]
			content.MetadataTruncated = true
		case len(content.Attachments) > 0:
			content.Attachments = content.Attachments[:len(content.Attachments)/2]
			content.MetadataTruncated = true
		case len(content.Text) > 128:
			content.Text, _ = BoundedBody(content.Text, len(content.Text)/2)
			content.Truncated = true
		default:
			item.Content = nil
			return item
		}
	}
}
