package room

import (
	"testing"
	"time"

	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestInputGroupingUsesSavedNativeIdentity(t *testing.T) {
	f := newStreamFixture(t, "direct")
	member := inputstream.Member{"fixture-room", "fixture-instance", "fixture-member"}
	r := f.records[1]
	ref, _ := r.Group.StreamRef(member)
	item := notification.Item{Text: "renamed with same numbers", Source: platform.SourceRef{Provider: "gitlab", Channel: "todos", Instance: "https://GITLAB.example.test/", SubscriptionID: "different-channel", Reference: platform.NotificationReference{Version: 1,
		RecipientID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9"}, ProjectID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "7"},
		Target: platform.ObjectReference{Type: "Note", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "123"}}, Object: platform.ObjectReference{Type: "merge_request", IID: platform.ExternalID{Namespace: "gitlab:rest:iid", Value: "56"}}}}}
	group := frozenInputCapabilities("gitlab").Input.GroupStoredInput
	g, err := group(item)
	if err != nil {
		t.Fatal(err)
	}
	same, _ := g.StreamRef(member)
	if same != ref {
		t.Fatal("events/todos or parent-object normalization diverged")
	}
	// The real collector saves its title in Content.Context, not Source.Object.
	stored := item
	stored.Source.Object = ""
	stored.Content = &notification.Content{Structured: true, Context: &notification.Context{Name: "group/project · MR !56 · 发布检查"}, Truncated: true}
	storedGroup, err := inputstream.GroupGitLab(stored)
	if err != nil || storedGroup.Facts.Label != stored.Content.Context.Name || !storedGroup.Facts.Incomplete {
		t.Fatal("saved collector title/coverage missing", storedGroup, err)
	}
	item.Source.Reference.Object.IID = platform.ExternalID{}
	item.Source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "56"}
	fallback, err := group(item)
	if err != nil || fallback.Precision != "source" {
		t.Fatal("REST ID guessed as IID", fallback, err)
	}
	unknown, _ := fallback.StreamRef(member)
	item.Source.SubscriptionID = "another-subscription"
	fallback, _ = group(item)
	other, _ := fallback.StreamRef(member)
	if other == unknown {
		t.Fatal("unknown identities merged across subscriptions")
	}
	item.Source.Reference.Version = 9
	fallback, err = group(item)
	if err != nil || fallback.Precision != "source" {
		t.Fatal("unknown evidence version promoted")
	}
	larkItem := notification.Item{Source: platform.SourceRef{Provider: "lark", Channel: "messages", Instance: "https://open.feishu.cn", SubscriptionID: "messages", Reference: platform.NotificationReference{Version: 2,
		AccountTenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}, RecipientID: platform.ExternalID{Namespace: "lark:open_id:cli_a", Value: "ou_reader"}, ChatID: platform.ExternalID{Namespace: "lark:chat_id:cli_a", Value: "oc_chat"}, DiscussionID: platform.ExternalID{Namespace: "lark:thread_id:cli_a", Value: "omt_one"}}}}
	lg, err := inputstream.GroupLark(larkItem)
	if err != nil || lg.Precision != "context" {
		t.Fatal(lg, err)
	}
	first, _ := lg.StreamRef(member)
	larkItem.Source.Reference.DiscussionID.Value = "omt_two"
	larkItem.Source.Reference.RootID = platform.ExternalID{Namespace: "lark:message_id:cli_a", Value: "om_root"}
	lg, err = inputstream.GroupLark(larkItem)
	second, _ := lg.StreamRef(member)
	if err != nil || first != second {
		t.Fatal("thread/root split chat", lg, err)
	}
	larkItem.Source.Reference.RecipientID.Namespace = "lark:open_id:cli_b"
	lg, _ = inputstream.GroupLark(larkItem)
	if lg.Precision != "source" {
		t.Fatal("cross-app chat guessed")
	}
}

func TestInputDescriptorsReuseStaticAssembly(t *testing.T) {
	m := &roomManager{}
	adapters := m.gitlabNotificationAdapters()
	adapters[lark.MessagesSourceID] = m.larkNotificationAdapter()
	before, err := inputStreamDescriptors(adapters, frozenInputCapabilities)
	if err != nil || len(before) != 2 {
		t.Fatal(before, err)
	}
	// A third provider AND another channel use the existing adapter registration.
	third := notification.SourceID{Provider: "fixture", Channel: "updates"}
	source := inputFixtureSource{id: third}
	adapters[third] = notificationAdapter{Source: source}
	another := notification.SourceID{Provider: "fixture", Channel: "alerts"}
	adapters[another] = notificationAdapter{Source: inputFixtureSource{id: another}}
	caps := func(p string) frozenCapabilities {
		if p != "fixture" {
			return frozenInputCapabilities(p)
		}
		return frozenCapabilities{Input: &inputstream.InputCapability{Descriptor: inputstream.Descriptor{Provider: p, Family: "object", Codec: "fixture", Schema: 1, Policy: "platform/v1"}, GroupStoredInput: func(i notification.Item) (inputstream.Group, error) {
			g := inputstream.StoredInputGroup(i)
			return g, g.Validate()
		}}}
	}
	after, err := inputStreamDescriptors(adapters, caps)
	if err != nil || len(after) != 3 || len(after[0].Channels) != 2 {
		t.Fatal(after, err)
	}
	item := notification.Item{Source: platform.SourceRef{Provider: "fixture", Channel: "alerts", Instance: "https://fixture.test", SubscriptionID: "s"}}
	g, err := caps("fixture").Input.GroupStoredInput(item)
	if err != nil {
		t.Fatal(err)
	}
	records := []inputstream.Record{{Location: inputstream.Location{"fixture", 1}, Group: g, Pending: true, ReceivedAt: time.Unix(1, 0)}}
	c, err := inputstream.NewCatalog(inputstream.Member{"r", "i", "m"}, 1, after, records, inputstream.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.List(inputstream.Query{Turn: 1, Provider: "fixture"})
	if err != nil || list.Matched != 1 {
		t.Fatal(list, err)
	}
}

type inputFixtureSource struct{ id notification.SourceID }

func (s inputFixtureSource) ID() notification.SourceID { return s.id }
func (inputFixtureSource) Schema() int                 { return 1 }
func (inputFixtureSource) Decode([]byte, platform.RoomIdentity) (notification.State, error) {
	return nil, notification.ErrInvalid
}
func (inputFixtureSource) NewRunner(notification.StepHost) notification.Runner { return nil }
