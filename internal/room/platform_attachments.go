package room

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Attachment requests identify a saved Room message or member Inbox entry. The
// client cannot choose a connection, origin or unrelated attachment/project.
type attachmentRequest struct {
	ServiceInfo
	AgentID, MembershipID string
	Seq                   uint64
	Path                  string
}

func (m *roomManager) attachmentSourceLocked(req attachmentRequest) (platform.SourceRef, string, error) {
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	if svc == nil || svc.info != req.ServiceInfo || svc.closing.Load() || svc.restarting.Load() {
		return platform.SourceRef{}, "", ErrIdentity
	}
	if req.AgentID != "" && req.MembershipID != "" {
		id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
		svc.store.mu.Lock()
		defer svc.store.mu.Unlock()
		entry, err := svc.store.inboxEntryLocked(id, req.Seq)
		if err != nil {
			return platform.SourceRef{}, "", ErrIdentity
		}
		return entry.Item.Source, entry.Item.Text, nil
	}
	if req.AgentID != "" || req.MembershipID != "" {
		return platform.SourceRef{}, "", ErrRequest
	}
	room, err := svc.store.LoadRoom()
	if err != nil {
		return platform.SourceRef{}, "", err
	}
	for _, message := range room.Messages {
		if message.Seq == req.Seq && message.Author.Kind == "integration" && message.Source != nil {
			return *message.Source, message.Text, nil
		}
	}
	return platform.SourceRef{}, "", ErrIdentity
}

func (m *roomManager) attachmentAccountLocked(owner RoomIdentity, source platform.SourceRef) (connection.Account, platform.Secret, error) {
	key := notification.Key{Owner: owner, Source: notification.SourceID{Provider: source.Provider, Channel: source.Channel}, SubscriptionID: source.SubscriptionID}
	state, ok := m.notifications.StateLocked(key)
	if !ok || source.Provider != "gitlab" {
		return connection.Account{}, platform.Secret{}, ErrIdentity
	}
	var id, generation, origin string
	var user int64
	switch s := state.(type) {
	case gitlab.EventsState:
		if s.Retired || (s.ProjectID != source.ProjectID && s.Binding.ProjectID != source.ProjectID) {
			return connection.Account{}, platform.Secret{}, ErrIdentity
		}
		id, generation, origin, user = s.Binding.ConnectionID, s.Binding.ConnectionGeneration, s.Binding.URL, s.UserID
	case gitlab.TodosState:
		if s.Retired || s.Project != nil && s.Project.ProjectID != source.ProjectID {
			return connection.Account{}, platform.Secret{}, ErrIdentity
		}
		id, generation, origin, user = s.Binding.ConnectionID, s.Binding.ConnectionGeneration, s.Binding.URL, s.Binding.UserID
	default:
		return connection.Account{}, platform.Secret{}, ErrIdentity
	}
	account, secret, err := m.resolvePlatformAccountLocked(owner, id, "gitlab")
	if err != nil {
		return account, secret, ErrIdentity
	}
	if origin != source.Instance || account.URL != origin || account.Generation != generation || account.UserID != user {
		return connection.Account{}, platform.Secret{}, ErrConflict
	}
	return account, secret, nil
}

func (m *roomManager) servePlatformAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	seq, err := strconv.ParseUint(q.Get("seq"), 10, 64)
	if err != nil || seq == 0 || !validText(q.Get("caller_id")) {
		writeAPIError(w, ErrRequest)
		return
	}
	req := attachmentRequest{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}, ServiceEpoch: q.Get("service_epoch")}, AgentID: q.Get("agent_id"), MembershipID: q.Get("membership_id"), Seq: seq, Path: q.Get("path")}
	m.mu.Lock()
	if m.closing || m.roomBusyLocked(req.RoomIdentity) {
		m.mu.Unlock()
		writeAPIError(w, ErrBusy)
		return
	}
	source, text, err := m.attachmentSourceLocked(req)
	upload, found := gitlab.FindUpload(text, source, req.Path)
	if err != nil || !found {
		m.mu.Unlock()
		writeAPIError(w, ErrIdentity)
		return
	}
	account, secret, err := m.attachmentAccountLocked(req.RoomIdentity, source)
	if err != nil {
		m.mu.Unlock()
		writeAPIError(w, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	call := &platformCall{owner: req.RoomIdentity, connectionID: account.ConnectionID, agentID: req.AgentID, membershipID: req.MembershipID, cancel: cancel, done: make(chan struct{})}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	transport := m.platformHTTP
	m.mu.Unlock()
	defer m.finishPlatformCall(call)

	data, mime, downloadErr := gitlab.DownloadUpload(ctx, account.URL, secret, upload, transport)
	m.mu.Lock()
	current, currentSecret, checkErr := m.attachmentAccountLocked(req.RoomIdentity, source)
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	valid := checkErr == nil && current == account && currentSecret.Reveal() == secret.Reveal() && ctx.Err() == nil && !m.roomBusyLocked(req.RoomIdentity) && svc != nil && svc.info == req.ServiceInfo && !svc.closing.Load() && !svc.restarting.Load()
	m.mu.Unlock()
	if !valid {
		writeAPIError(w, ErrConflict)
		return
	}
	if downloadErr != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(apiError{Code: "attachment_unavailable", Error: "图片加载失败，请检查 GitLab 权限或附件是否仍然存在"})
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(data)
}
