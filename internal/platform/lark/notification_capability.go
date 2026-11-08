package lark

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// NotificationCapabilities keeps native relationship evidence and message
// presentation inside this adapter, including historical saved envelopes.
func NotificationCapabilities() notification.Capabilities {
	return notification.Capabilities{
		Admission: &notification.AdmissionCapability{
			EvidencePath: conversationEvidencePath,
			Inspect:      inspectNotificationAdmission,
		},
		Project: projectNotification,
		MatchAction: func(v notification.View, q platform.TargetActionRequest, a platform.AccessContext) bool {
			if a.Account.Provider != "lark" || (q.Operation != platform.OperationMessageReply && q.Operation != platform.OperationMessageSend && q.Operation != platform.OperationMessageSendSelf) {
				return false
			}
			chat := q.Target.Object.ID
			if q.Target.Object.Type != "chat" {
				chat = platform.ExternalID{}
				for _, c := range q.Target.Containers {
					if c.Type == "chat" {
						chat = c.ID
					}
				}
			}
			return chat.Valid() && v.Conversation != nil && v.Conversation.ID == chat
		},
	}
}

func conversationEvidencePath(s platform.SourceRef) string {
	r := s.Reference
	// Keep the existing immutable relationship snapshots readable.
	b, _ := json.Marshal([]any{s.Instance, r.RecipientID, r.AccountTenant, r.ChatID})
	h := sha256.Sum256(b)
	return "lark-conversations/conversation_" + hex.EncodeToString(h[:]) + ".json"
}

func (r ConversationRelation) MatchesSource(source platform.SourceRef) bool {
	s := source.Reference
	return source.Provider == "lark" && strings.HasPrefix(r.Account.ID.Namespace, "lark:open_id:cli_") && r.ValidFor(r.Account, s.ChatID.Value) && r.Account.Instance == source.Instance &&
		r.Account.ID == s.RecipientID && (!s.AccountTenant.Valid() || r.Account.Tenant == s.AccountTenant) &&
		s.ChatID.Namespace == "lark:chat_id:"+r.Account.ID.Namespace[len("lark:open_id:"):]
}

func inspectNotificationAdmission(item notification.Item, evidence json.RawMessage) (notification.Admission, error) {
	allow := notification.Admission{Verified: true, Decision: notification.AdmissionAllow}
	if c := item.Content; c != nil && c.Conversation != nil && (c.Conversation.Type == "group" || c.Conversation.Type == "topic") {
		return allow, nil
	}
	if len(evidence) == 0 {
		return notification.Admission{ReasonCode: "relationship_pending"}, nil
	}
	var r ConversationRelation
	if json.Unmarshal(evidence, &r) != nil || !r.MatchesSource(item.Source) {
		return notification.Admission{}, notification.ErrInvalid
	}
	if r.Kind == "self" {
		return notification.Admission{Verified: true, Decision: notification.AdmissionExcluded, ReasonCode: "self_excluded"}, nil
	}
	return allow, nil
}

func projectNotification(item notification.Item) notification.View {
	if item.Content == nil {
		item.Content = SavedContent(item)
	}
	v := notification.Project(item)
	// Older content snapshots predate the adapter-supplied context_only flag.
	v.ContextOnly = v.ContextOnly || v.Sender != nil && v.Sender.IsReceiver &&
		v.Receiver.ID.Valid() && v.Sender.ID == v.Receiver.ID
	return v
}
