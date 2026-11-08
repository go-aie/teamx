package room

import (
	"context"
	"encoding/json"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// Recover classification even for disabled/retired sources and frozen routes.
// The source binding, not the member's possibly changed read-tool binding,
// identifies the original account. No lock is held during native reads.
func (m *roomManager) inboxLarkConversation(ctx context.Context, svc *service, source platform.SourceRef) (lark.ConversationRelation, error) {
	var empty lark.ConversationRelation
	m.mu.Lock()
	current, _ := m.runtimeServiceLocked(svc.info.RoomIdentity)
	if m.closing || current != svc || svc.closing.Load() {
		m.mu.Unlock()
		return empty, ErrClosing
	}
	var original *lark.State
	for _, state := range m.notifications.StatesLocked(svc.info.RoomIdentity) {
		if s, ok := state.(lark.State); ok && s.SubscriptionID == source.SubscriptionID && s.Binding.Account.ID == source.Reference.RecipientID && s.Binding.Account.Instance == source.Instance && (!source.Reference.AccountTenant.Valid() || s.Binding.Account.Tenant == source.Reference.AccountTenant) {
			original = &s
			break
		}
	}
	if original == nil {
		m.mu.Unlock()
		return empty, ErrIdentity
	}
	chatID := source.Reference.ChatID.Value
	// Any subscription under this exact account can supply immutable P2P proof.
	for _, room := range m.index.Rooms {
		for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
			if s, ok := state.(lark.State); ok && s.Binding.Account == original.Binding.Account {
				r := s.Chats[chatID].Chat.Relation
				if r.MatchesSource(source) {
					m.mu.Unlock()
					return r, nil
				}
			}
		}
	}
	a, _, err := m.resolvePlatformAccountLocked(svc.info.RoomIdentity, original.Binding.ConnectionID, "lark")
	// Retired sources keep their historical credential stamp. A currently
	// verified credential for the same generation/account may classify history;
	// it does not restore the source or grant any write capability.
	if err != nil || a.CLIProfile == nil || a.Generation != original.Binding.Generation || a.Identity != original.Binding.Account {
		m.mu.Unlock()
		return empty, ErrConflict
	}
	m.mu.Unlock()
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(svc.ctx, cancel)
	defer stop()
	client, err := m.larkClients.New(*a.CLIProfile)
	if err != nil {
		return empty, err
	}
	chat, err := client.NotificationChat(queryCtx, a.Identity, chatID)
	if err != nil {
		return empty, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, _ = m.runtimeServiceLocked(svc.info.RoomIdentity)
	after, _, err := m.resolvePlatformAccountLocked(svc.info.RoomIdentity, a.ConnectionID, "lark")
	if err != nil || m.closing || current != svc || queryCtx.Err() != nil || after.ConnectionVersion != a.ConnectionVersion || larkBinding(after) != larkBinding(a) {
		return empty, ErrConflict
	}
	if !chat.Relation.MatchesSource(source) {
		return empty, ErrIdentity
	}
	return chat.Relation, nil
}

func (m *roomManager) resolveLarkInboxAdmission(ctx context.Context, svc *service, source platform.SourceRef) (json.RawMessage, error) {
	r, err := m.inboxLarkConversation(ctx, svc, source)
	if err != nil {
		return nil, err
	}
	return json.Marshal(r)
}
