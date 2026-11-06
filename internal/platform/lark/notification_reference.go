package lark

import (
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const NotificationEvidence = "notification"

// ResolveNotification consumes only the native envelope saved by collection.
// It must not be used with Agent-supplied locators, and grants no remote access.
func ResolveNotification(s platform.SourceRef, checkedAt time.Time) platform.TargetResolution {
	r := platform.TargetResolution{EvidenceSource: NotificationEvidence, Status: platform.ResolutionMismatch,
		ReasonCode: "source_mismatch", Reason: "通知缺少有效的飞书原生身份；保留正文与来源", CheckedAt: checkedAt}
	ref := s.Reference
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: s.Instance, Tenant: ref.AccountTenant}, ID: ref.RecipientID}
	app := strings.TrimPrefix(account.ID.Namespace, "lark:open_id:")
	if s.Provider != "lark" || s.Channel != "messages" || !ref.Valid() || ref.Version != 2 ||
		!ValidAccount(account, platform.CLIProfile{Brand: "feishu"}) && !ValidAccount(account, platform.CLIProfile{Brand: "lark"}) ||
		!namePattern.MatchString(app) || len(app) <= len("cli_") || !nativeID(account.ID.Value, "ou_") ||
		ref.ChatID.Namespace != nativeNamespace(account, "chat_id") || !nativeID(ref.ChatID.Value, "oc_") ||
		ref.Target.ID.Namespace != nativeNamespace(account, "message_id") || !nativeID(ref.Target.ID.Value, "om_") ||
		s.Tenant != (platform.ExternalID{}) && (s.Tenant.Namespace != "lark:tenant_key" || !s.Tenant.Valid()) {
		return r
	}
	for _, id := range []platform.ExternalID{ref.ParentID, ref.RootID} {
		if id != (platform.ExternalID{}) && (id.Namespace != nativeNamespace(account, "message_id") || !nativeID(id.Value, "om_")) {
			return r
		}
	}
	if ref.DiscussionID != (platform.ExternalID{}) && (ref.DiscussionID.Namespace != nativeNamespace(account, "thread_id") || !nativeID(ref.DiscussionID.Value, "omt_")) {
		return r
	}
	target := platform.TargetIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: s.Instance, Tenant: s.Tenant},
		Containers: []platform.NativeIdentity{{Type: "chat", ID: ref.ChatID}}, Object: platform.NativeIdentity{Type: "message", ID: ref.Target.ID},
		Message: ref.Target.ID, Discussion: ref.DiscussionID}
	if ref.RootID.Valid() {
		target.Object.ID = ref.RootID
	}
	r.Status, r.ReasonCode = platform.ResolutionResolved, "notification_snapshot"
	r.Reason = "基于已保存的飞书通知建立关联；未查询当前远端状态。需要最新内容或前后文时再使用 lark read"
	r.Target, r.Account = &target, account
	r.ObjectVerified, r.MessageVerified, r.DiscussionVerified, r.Complete = true, true, ref.DiscussionID.Valid(), true
	if !ref.RootID.Valid() && ref.ParentID.Valid() {
		// A parent alone does not prove the root. Keep the known message identity
		// instead of guessing a grouping from conversation text or parent IDs.
		r.Status, r.ReasonCode, r.Complete = platform.ResolutionPartial, "root_missing", false
		r.Reason += "；通知未提供根消息，暂按当前消息关联"
	}
	return r
}
