package inputstreamtest

import (
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func LarkCapability() *InputCapability {
	return &InputCapability{Descriptor: Descriptor{Provider: "lark", Family: "conversation", Channels: []string{"messages"}, Codec: "lark.notification", Schema: 2, Policy: "platform/v1"}, GroupStoredInput: GroupLark}
}
func GroupLark(item notification.Item) (Group, error) {
	g := StoredInputGroup(item)
	s := item.Source
	r := s.Reference
	if s.Provider != "lark" {
		return g, ErrInvalid
	}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: s.Provider, Instance: s.Instance, Tenant: r.AccountTenant}, ID: r.RecipientID}
	if !lark.ValidAccount(account, platform.CLIProfile{Brand: "feishu"}) && !lark.ValidAccount(account, platform.CLIProfile{Brand: "lark"}) {
		g.Account = ""
	}
	app := strings.TrimPrefix(r.RecipientID.Namespace, "lark:open_id:")
	if r.Version == 2 && (lark.ValidAccount(account, platform.CLIProfile{Brand: "feishu"}) || lark.ValidAccount(account, platform.CLIProfile{Brand: "lark"})) &&
		strings.HasPrefix(r.RecipientID.Namespace, "lark:open_id:cli_") && r.ChatID.Namespace == "lark:chat_id:"+app && frozenLarkNativeID(r.ChatID.Value, "oc_") {
		g.Precision, g.Missing = "context", ""
		g.Facts.AccountLabel = app + "/" + r.RecipientID.Value
		g.Context = []string{notification.NativeID(r.ChatID)}
		g.Facts.ChatID = r.ChatID.Value
	}
	v := lark.NotificationCapabilities().Project(item)
	if v.Conversation != nil {
		g.Facts.Chat = v.Conversation.Name
		g.Facts.Label = v.Conversation.Name
	}
	g.Facts.ThreadID = r.DiscussionID.Value
	g.Facts.Summary = v.Text
	g.Facts.Incomplete = v.Truncated || v.MetadataTruncated
	return g, g.Validate()
}

func frozenLarkNativeID(s, prefix string) bool {
	return strings.HasPrefix(s, prefix) && len(s) > len(prefix) && len(s) <= 256 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}
