package lark

import (
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestNotificationReferenceUsesCapturedNativeRelationships(t *testing.T) {
	s := sampleState(t)
	chat := Chat{ID: "oc_chat", Mode: "group", TenantKey: "external_tenant"}
	m := sampleMessage("om_reply", chat.ID, "captured body")
	m.RootID, m.ParentID, m.ThreadID = "om_root", "om_parent", "omt_thread"
	source := sourceRef(s, m, "original", chat)
	now := time.Now().UTC()
	r := ResolveNotification(source, now)
	if r.Status != platform.ResolutionResolved || r.Target == nil || r.Target.Object.ID.Value != m.RootID ||
		r.Target.Message.Value != m.ID || r.Target.Discussion.Value != m.ThreadID || r.Target.Tenant == r.Account.Tenant ||
		!r.ObjectVerified || !r.MessageVerified || !r.DiscussionVerified || !r.Complete ||
		r.EvidenceSource != NotificationEvidence || !r.CheckedAt.Equal(now) {
		t.Fatal("lost captured identity or confused account and chat tenants", r)
	}
	// Another reply and a native edit retain one root object without reading it.
	m.ID, m.Updated = "om_second", "1002000"
	edited := ResolveNotification(sourceRef(s, m, "edit", chat), now)
	if edited.Target.Object != r.Target.Object || edited.Target.Message == r.Target.Message {
		t.Fatal("native grouping lost", edited)
	}
	// A parent alone is not a root; missing information remains explicit.
	m.RootID = ""
	partial := ResolveNotification(sourceRef(s, m, "partial", chat), now)
	if partial.Status != platform.ResolutionPartial || partial.ReasonCode != "root_missing" || partial.Complete || partial.Target.Object.ID.Value != m.ID {
		t.Fatal("guessed a root from a parent", partial)
	}
	m.ParentID, m.ThreadID = "", ""
	one := ResolveNotification(sourceRef(s, m, "one", chat), now)
	m.ID = "om_independent"
	other := ResolveNotification(sourceRef(s, m, "other", chat), now)
	if one.Target.Object == other.Target.Object || one.Target.Object.Type != "message" {
		t.Fatal("conversation became a single followup object")
	}
}

func TestNotificationReferenceRejectsIncompleteOrMixedNativeEvidence(t *testing.T) {
	s := sampleState(t)
	m := sampleMessage("om_one", "oc_chat", "body is not identity")
	m.ParentID, m.RootID, m.ThreadID = "om_parent", "om_root", "omt_thread"
	source := sourceRef(s, m, "key", Chat{ID: m.ChatID, Mode: "p2p", TenantKey: "chat_tenant"})
	for name, mutate := range map[string]func(*platform.SourceRef){
		"provider":          func(s *platform.SourceRef) { s.Provider = "gitlab" },
		"channel":           func(s *platform.SourceRef) { s.Channel = "events" },
		"instance":          func(s *platform.SourceRef) { s.Instance = "https://unrelated.invalid" },
		"version":           func(s *platform.SourceRef) { s.Reference.Version = 1 },
		"recipient":         func(s *platform.SourceRef) { s.Reference.RecipientID.Value = "other" },
		"app":               func(s *platform.SourceRef) { s.Reference.ChatID.Namespace = "lark:chat_id:cli_other" },
		"message_namespace": func(s *platform.SourceRef) { s.Reference.Target.ID.Namespace = "lark:message_id:cli_other" },
		"message_id":        func(s *platform.SourceRef) { s.Reference.Target.ID.Value = "oc_chat" },
		"parent":            func(s *platform.SourceRef) { s.Reference.ParentID.Namespace = "lark:message_id:cli_other" },
		"root":              func(s *platform.SourceRef) { s.Reference.RootID.Value = "oc_chat" },
		"thread":            func(s *platform.SourceRef) { s.Reference.DiscussionID.Namespace = "lark:thread_id:cli_other" },
		"tenant":            func(s *platform.SourceRef) { s.Tenant.Namespace = "wrong:tenant" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := source
			mutate(&bad)
			r := ResolveNotification(bad, time.Now().UTC())
			if r.Status != platform.ResolutionMismatch || r.Target != nil || r.ObjectVerified || r.MessageVerified || r.DiscussionVerified {
				t.Fatal("promoted invalid source", r)
			}
		})
	}
}
