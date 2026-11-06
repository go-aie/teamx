package lark

import (
	"context"
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform"
	"strconv"
	"time"
	"unicode/utf8"
)

// Capabilities has no Actions: collection never authorizes external replies.
func Capabilities(reader MessageReader) platform.ReferenceCapabilities {
	return platform.ReferenceCapabilities{CredentialKinds: []string{"lark:cli_profile"}, Resolve: func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		return ResolveTarget(ctx, l, a, reader)
	}, Read: func(ctx context.Context, q platform.ReadTargetRequest, a platform.AccessContext) (platform.TargetPage, error) {
		return ReadTarget(ctx, q, a, reader)
	}}
}
func ResolveTarget(ctx context.Context, l platform.TargetLocator, a platform.AccessContext, reader MessageReader) (platform.TargetResolution, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r := platform.TargetResolution{Status: platform.ResolutionMismatch, ReasonCode: "source_mismatch", CheckedAt: time.Now().UTC(), Account: a.Account}
	if reader == nil || !a.Valid() || a.Account.Provider != "lark" || l.Source == nil {
		return r, platform.ErrReference
	}
	s := *l.Source
	ref := s.Reference
	if s.Provider != "lark" || s.Channel != "messages" || s.Instance != a.Account.Instance || !ref.Valid() || ref.Version != 2 || ref.Target.ID.Namespace != nativeNamespace(a.Account, "message_id") || ref.ChatID.Namespace != nativeNamespace(a.Account, "chat_id") || ref.RecipientID.Namespace != a.Account.ID.Namespace || !a.Receiver.Valid() || a.Receiver.ID != ref.RecipientID || a.Receiver.Tenant != ref.AccountTenant {
		return r, nil
	}
	message, err := reader.Message(ctx, a.Account, ref.Target.ID.Value)
	if err != nil {
		r.Status = platform.ResolutionUnavailable
		r.ReasonCode = ErrorCode(err)
		return r, nil
	}
	if !message.valid() || message.ChatID != ref.ChatID.Value || message.ID != ref.Target.ID.Value {
		return r, nil
	}
	// Chat tenant is target evidence, never inferred from sender or receiver tenant.
	chat, err := reader.Chat(ctx, a.Account, message.ChatID)
	if err != nil {
		r.Status = platform.ResolutionUnavailable
		r.ReasonCode = ErrorCode(err)
		return r, nil
	}
	tenant := platform.ExternalID{}
	if chat.TenantKey != "" {
		tenant = platform.ExternalID{Namespace: "lark:tenant_key", Value: chat.TenantKey}
	}
	if !chat.Valid() || chat.ID != message.ChatID || tenant != s.Tenant {
		return r, nil
	}
	target := platform.TargetIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: s.Instance, Tenant: tenant}, Containers: []platform.NativeIdentity{{Type: "chat", ID: ref.ChatID}}, Object: platform.NativeIdentity{Type: "message", ID: ref.Target.ID}, Message: ref.Target.ID}
	r.Status = platform.ResolutionResolved
	r.ReasonCode = ""
	r.ObjectVerified = true
	r.MessageVerified = true
	r.Complete = true
	if message.RootID != "" && message.RootID != message.ID {
		root, e := reader.Message(ctx, a.Account, message.RootID)
		if e == nil && root.valid() && root.ID == message.RootID && root.ChatID == message.ChatID && (root.RootID == "" || root.RootID == root.ID) {
			target.Object.ID.Value = root.ID
		} else {
			r.Status = platform.ResolutionPartial
			r.ReasonCode = "root_unavailable"
			r.Complete = false
		}
	} else if message.ParentID != "" {
		r.Status = platform.ResolutionPartial
		r.ReasonCode = "root_missing"
		r.Complete = false
	}
	if message.ThreadID != "" {
		target.Discussion = platform.ExternalID{Namespace: nativeNamespace(a.Account, "thread_id"), Value: message.ThreadID}
		r.DiscussionVerified = true
	}
	r.Target = &target
	return r, nil
}
func ReadTarget(ctx context.Context, q platform.ReadTargetRequest, a platform.AccessContext, reader MessageReader) (platform.TargetPage, error) {
	p := platform.TargetPage{Status: platform.ResolutionMismatch, Target: q.Target, Account: a.Account, Contents: []platform.TargetContent{}}
	if reader == nil || !a.Valid() || !q.Target.Valid() || !q.Limits.Valid() || q.Target.Provider != "lark" || q.Target.Instance != a.Account.Instance || len(q.Target.Containers) != 1 || q.Target.Containers[0].Type != "chat" || q.Target.Containers[0].ID.Namespace != nativeNamespace(a.Account, "chat_id") || q.Target.Object.Type != "message" || q.Target.Object.ID.Namespace != nativeNamespace(a.Account, "message_id") {
		return p, platform.ErrReference
	}
	if q.Scope != "content" && q.Scope != "message" && q.Scope != "context" {
		p.Status = platform.ResolutionUnsupported
		p.ReasonCode = "unsupported_scope"
		return p, nil
	}
	if q.Cursor != nil && !q.Cursor.ValidFor(q.Target, a, q.Scope) {
		return p, platform.ErrReference
	}
	if q.Cursor != nil && q.Scope != "context" {
		return p, platform.ErrReference
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := q.Target.Message
	if !id.Valid() {
		id = q.Target.Object.ID
	}
	if id.Namespace != nativeNamespace(a.Account, "message_id") {
		return p, platform.ErrReference
	}
	message, err := reader.Message(ctx, a.Account, id.Value)
	if err != nil {
		p.Status = platform.ResolutionUnavailable
		p.ReasonCode = ErrorCode(err)
		return p, nil
	}
	chatID := q.Target.Containers[0].ID.Value
	if !message.valid() || message.ChatID != chatID || message.ID != id.Value || q.Target.Object.ID.Value != message.ID && q.Target.Object.ID.Value != message.RootID || q.Target.Discussion.Valid() && (q.Target.Discussion.Namespace != nativeNamespace(a.Account, "thread_id") || q.Target.Discussion.Value != message.ThreadID) {
		return p, nil
	}
	chat, err := reader.Chat(ctx, a.Account, chatID)
	if err != nil {
		p.Status = platform.ResolutionUnavailable
		p.ReasonCode = ErrorCode(err)
		return p, nil
	}
	tenant := platform.ExternalID{}
	if chat.TenantKey != "" {
		tenant = platform.ExternalID{Namespace: "lark:tenant_key", Value: chat.TenantKey}
	}
	if !chat.Valid() || chat.ID != chatID || tenant != q.Target.Tenant {
		return p, nil
	}
	messages := []Message{message}
	more := false
	token := ""
	if q.Scope == "context" {
		ms, _ := strconv.ParseInt(message.Created, 10, 64)
		req := MessagesRequest{ChatID: chatID, Start: max(1, ms/1000-300), End: ms/1000 + 300, PageSize: min(50, q.Limits.Items)}
		if q.Cursor != nil {
			req.PageToken = q.Cursor.Token
		}
		page, e := reader.Messages(ctx, a.Account, req)
		if e != nil {
			p.Status = platform.ResolutionUnavailable
			p.ReasonCode = ErrorCode(e)
			return p, nil
		}
		messages = page.Items
		more = page.HasMore
		token = page.PageToken
	}
	p.Status = platform.ResolutionResolved
	p.Complete = !more
	bytesLeft := q.Limits.Bytes - 2048
	for _, m := range messages {
		body := m.ContentWithSenderFor(a.Account.ID.Value)
		complete := true
		n := min(q.Limits.BodyBytes, max(0, bytesLeft-1024))
		if len(body) > n {
			body = body[:n]
			for !utf8.ValidString(body) {
				body = body[:len(body)-1]
			}
			complete = false
			p.Complete = false
			p.ReasonCode = "body_truncated"
		}
		target := q.Target
		target.Message.Value = m.ID
		if m.ID != message.ID {
			target.Object.ID.Value = m.ID
			target.Discussion = platform.ExternalID{}
		}
		p.Contents = append(p.Contents, platform.TargetContent{Target: target, Body: body, Complete: complete})
		bytesLeft -= len(body) + 1024
	}
	if more {
		key, _ := q.Target.Key()
		p.Next = &platform.ReadCursor{Token: token, TargetKey: key, Access: a, Scope: q.Scope}
		if p.ReasonCode == "" {
			p.ReasonCode = "page_incomplete"
		}
	}
	// Enforce the serialized response bound, including target identities/cursors.
	for {
		b, _ := json.Marshal(p)
		if len(b) <= q.Limits.Bytes {
			break
		}
		changed := false
		for i := range p.Contents {
			body := p.Contents[i].Body
			if len(body) > 0 {
				body = body[:max(0, len(body)-512)]
				for !utf8.ValidString(body) {
					body = body[:len(body)-1]
				}
				p.Contents[i].Body = body
				p.Contents[i].Complete = false
				changed = true
			}
		}
		if !changed {
			return platform.TargetPage{}, platform.ErrReference
		}
		p.Complete = false
		p.ReasonCode = "byte_limit"
	}
	return p, nil
}
