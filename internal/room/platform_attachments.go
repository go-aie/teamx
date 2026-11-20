package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// Attachment requests identify a saved Room message or member Inbox entry. The
// client cannot choose a connection, origin or unrelated attachment/project.
type attachmentRequest struct {
	ServiceInfo
	AgentID, MembershipID string
	SourceRef, Partition  string
	Seq                   uint64
	Path                  string
	Local                 bool                `json:"-"` // Room user attachment, never a platform source.
	Page                  *roomAttachmentPage `json:"-"` // Exact server-selected formal page.
	Range                 *attachmentRange    `json:"-"` // Internal immutable selection; never decoded from HTTP.
}

// A model read must carry the actual running member even for a Room message.
// Page preview authority alone never admits a model download.
type attachmentReader struct {
	MemberIdentity
	TurnNumber uint64
}

// Kept in memory only. Preparation rechecks source evidence at the owning
// manager/Store gate; successful download grants no lasting access.
type attachmentAccess struct {
	local   *roomAttachmentMeta
	request attachmentRequest
	reader  *attachmentReader
	source  platform.SourceRef
	text    string
	account registry.Account
	secret  platform.Secret
}

func (m *roomManager) checkAttachmentReaderLocked(ctx context.Context, req attachmentRequest, reader *attachmentReader) error {
	if reader == nil {
		return nil
	}
	if reader.RoomIdentity != req.RoomIdentity || reader.TurnNumber == 0 ||
		(req.AgentID != "" && (req.AgentID != reader.AgentID || req.MembershipID != reader.MembershipID)) {
		return ErrIdentity
	}
	if err := m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: req.ServiceInfo, Caller: agentCallerIdentity(reader.MemberIdentity)}); err != nil {
		return err
	}
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	svc.store.mu.Lock()
	defer svc.store.mu.Unlock()
	agent, err := svc.store.runningTurnLocked(reader.MemberIdentity, reader.TurnNumber)
	if err != nil {
		return err
	}
	if req.Range != nil {
		return req.Range.checkLocked(req, reader)
	}
	if req.Partition != "" {
		return ErrIdentity
	}
	if req.SourceRef != "" {
		link, _, err := svc.store.inputSourceContentLocked(reader.MemberIdentity, req.SourceRef)
		if err != nil {
			return err
		}
		if link.Location == nil {
			return ErrIdentity
		}
		return svc.store.authorizeInputLocationLocked(reader.MemberIdentity, reader.TurnNumber, *link.Location)
	}
	if svc.unifiedInputs && req.AgentID == "" {
		p := svc.store.platformInputs[reader.MembershipID]
		if p == nil {
			return ErrIdentity
		}
		state, err := p.repository.State(inputstream.Execution{Generation: p.generation, Turn: reader.TurnNumber})
		if err != nil {
			return err
		}
		for _, a := range state.Attempts {
			if a.Kind == "room" && a.Phase != "failed_before_output" {
				for _, l := range a.Entries {
					if l.Seq == req.Seq && l.Partition == roomPartitionRef(req.RoomIdentity) {
						return nil
					}
				}
			}
		}
		return ErrIdentity
	}
	start, end := agent.Turn.StartReadThrough, agent.Turn.SnapshotSeq
	if req.AgentID != "" {
		start, end = agent.Turn.InboxStart, agent.Turn.InboxSnapshotSeq
	}
	if req.Seq <= start || req.Seq > end {
		return ErrIdentity
	}
	return nil
}

func (m *roomManager) checkAttachmentAccessLocked(ctx context.Context, access *attachmentAccess) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.closing || m.roomBusyLocked(access.request.RoomIdentity) {
		return ErrBusy
	}
	source, text, err := m.attachmentSourceLocked(access.request)
	if err != nil || source != access.source || text != access.text {
		return ErrIdentity
	}
	if err := m.checkAttachmentReaderLocked(ctx, access.request, access.reader); err != nil {
		return err
	}
	account, secret, err := m.attachmentReadAccountLocked(access.request, access.reader, source)
	if err != nil || !reflect.DeepEqual(account, access.account) || secret.Reveal() != access.secret.Reveal() {
		return ErrConflict
	}
	return nil
}

// Shared page/model operation: saved-source admission, account resolution,
// registered cancellation, network outside the gate, and late-result rejection.
// Models must choose explicit byte and pixel caps; pages retain their own cap.
func (m *roomManager) downloadAttachment(ctx context.Context, req attachmentRequest, reader *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return nil, "", nil, err
	}
	if m.closing || m.roomBusyLocked(req.RoomIdentity) {
		m.mu.Unlock()
		return nil, "", nil, ErrBusy
	}
	if reader != nil && (limits.MaxBytes <= 0 || limits.MaxBytes > attachment.MaxBytes || limits.MaxPixels <= 0) {
		m.mu.Unlock()
		return nil, "", nil, ErrRequest
	}
	source, saved, err := m.attachmentSourceLocked(req)
	upload, found := gitlab.FindUpload(saved, source, req.Path)
	if source.Provider == "lark" {
		for _, image := range findNotificationImages(saved, source).Images {
			found = found || image.Supported && image.Path == req.Path
		}
	}
	if err != nil || !found {
		m.mu.Unlock()
		return nil, "", nil, ErrIdentity
	}
	if reader != nil {
		// The model path accepts image syntax only, unlike manual page preview.
		found = false
		for _, image := range findNotificationImages(saved, source).Images {
			// GitLab can spell the same upload with different equivalent paths.
			candidate, valid := gitlab.ParseUpload(image.Path, source)
			if image.Supported && (image.Path == req.Path || source.Provider == "gitlab" && valid && candidate == upload) {
				found = true
				break
			}
		}
		if !found {
			m.mu.Unlock()
			return nil, "", nil, ErrIdentity
		}
		copy := *reader
		reader = &copy
	}
	if err := m.checkAttachmentReaderLocked(ctx, req, reader); err != nil {
		m.mu.Unlock()
		return nil, "", nil, err
	}
	account, secret, err := m.attachmentReadAccountLocked(req, reader, source)
	if err != nil {
		m.mu.Unlock()
		return nil, "", nil, err
	}
	access := &attachmentAccess{request: req, reader: reader, source: source, text: saved, account: account, secret: secret}
	callCtx, cancel := context.WithCancel(ctx)
	call := &platformCall{owner: req.RoomIdentity, platformID: account.PlatformID, agentID: req.AgentID, membershipID: req.MembershipID, subscription: notification.Key{Owner: req.RoomIdentity, Source: notification.SourceID{Provider: source.Provider, Channel: source.Channel}, SubscriptionID: source.SubscriptionID}}
	if reader != nil {
		call.agentID, call.membershipID = reader.AgentID, reader.MembershipID
	}
	call.Call = m.platformCalls.Register(call, cancel)
	transport := m.platformHTTP
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	var data []byte
	var mime string
	var downloadErr error
	if source.Provider == "lark" {
		client, err := m.larkClients.New(*account.CLIProfile)
		if err != nil {
			downloadErr = err
		} else {
			data, mime, downloadErr = client.Image(callCtx, account.Identity, source, strings.TrimPrefix(req.Path, lark.ImagePrefix), limits)
		}
	} else {
		useCtx, credential, release, err := m.acquireGitLabAccess(callCtx, req.RoomIdentity, account, secret, 30*time.Second)
		if err != nil {
			downloadErr = err
		} else {
			data, mime, downloadErr = gitlab.DownloadUploadWithCredential(useCtx, account.URL, credential, upload, transport, limits)
			if checkErr := release(); checkErr != nil {
				downloadErr = checkErr
			}
		}
	}
	m.mu.Lock()
	checkErr := m.checkAttachmentAccessLocked(callCtx, access)
	m.mu.Unlock()
	if checkErr != nil {
		return nil, "", nil, checkErr
	}
	if downloadErr != nil {
		return nil, "", nil, downloadErr
	}
	return data, mime, access, nil
}

func (m *roomManager) attachmentSourceLocked(req attachmentRequest) (platform.SourceRef, string, error) {
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	if svc == nil || svc.info != req.ServiceInfo || svc.closing.Load() || svc.restarting.Load() {
		return platform.SourceRef{}, "", ErrIdentity
	}
	if req.Range != nil {
		if req.Range.session == nil || req.Range.session.service != svc {
			return platform.SourceRef{}, "", ErrIdentity
		}
		return req.Range.source(context.Background())
	}
	if req.AgentID != "" && req.MembershipID != "" {
		id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
		svc.store.mu.Lock()
		defer svc.store.mu.Unlock()
		if req.Partition != "" {
			if !svc.unifiedInputs || req.SourceRef != "" {
				return platform.SourceRef{}, "", ErrIdentity
			}
			location := inputstream.Location{Partition: req.Partition, Seq: req.Seq}
			entry, err := svc.store.businessInputLocked(id, 0, &location)
			if errors.Is(err, inputstream.ErrExpired) {
				return platform.SourceRef{}, "", ErrInboxExpired
			}
			if err != nil {
				return platform.SourceRef{}, "", err
			}
			return entry.Item.Source, entry.Item.Text, nil
		}
		if req.SourceRef != "" {
			_, entry, err := svc.store.inputSourceContentLocked(id, req.SourceRef)
			if err != nil {
				return platform.SourceRef{}, "", err
			}
			if entry == nil {
				return platform.SourceRef{}, "", ErrInboxExpired
			}
			return entry.Item.Source, entry.Item.Text, nil
		}
		entry, err := svc.store.inboxEntryLocked(id, req.Seq)
		if err != nil {
			return platform.SourceRef{}, "", ErrIdentity
		}
		if entry.payloadType() != InboxPlatformNotification {
			return platform.SourceRef{}, "", ErrRequest
		}
		return entry.Item.Source, entry.Item.Text, nil
	}
	if req.AgentID != "" || req.MembershipID != "" || req.Partition != "" {
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

// History image reads use the executing member's selected connection. Shared
// collection grants local text visibility, never the collector's credentials.
func (m *roomManager) attachmentReadAccountLocked(req attachmentRequest, reader *attachmentReader, source platform.SourceRef) (registry.Account, platform.Secret, error) {
	if req.Range == nil {
		return m.attachmentAccountLocked(req.RoomIdentity, source)
	}
	if reader == nil {
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return registry.Account{}, platform.Secret{}, err
	}
	toolReq := toolsRequest{ServiceInfo: req.ServiceInfo, Caller: agentCallerIdentity(reader.MemberIdentity), Tool: source.Provider}
	if req.Range.stream.grant == "" {
		toolReq.platformID, err = m.sourcePlatformLocked(req.RoomIdentity, source)
		if err != nil {
			return registry.Account{}, platform.Secret{}, err
		}
	}
	binding, definition, err := m.resolveToolBindingLocked(f, toolReq)
	if err != nil || definition.Provider != source.Provider {
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	account, secret, err := m.resolvePlatformAccountLocked(req.RoomIdentity, binding.PlatformID, source.Provider)
	if err != nil || account.Generation != binding.PlatformGeneration || account.Identity.Instance != source.Instance {
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	if req.Range.stream.grant == "" {
		original, _, err := m.attachmentAccountLocked(req.RoomIdentity, source)
		if err != nil || original.Identity != account.Identity {
			return registry.Account{}, platform.Secret{}, ErrIdentity
		}
	}
	return account, secret, nil
}

func (m *roomManager) attachmentAccountLocked(owner RoomIdentity, source platform.SourceRef) (registry.Account, platform.Secret, error) {
	key := notification.Key{Owner: owner, Source: notification.SourceID{Provider: source.Provider, Channel: source.Channel}, SubscriptionID: source.SubscriptionID}
	state, ok := m.notifications.StateLocked(key)
	if !ok {
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	if s, ok := state.(lark.State); ok {
		if s.Retired || source.Provider != "lark" || source.Channel != "messages" || source.Instance != s.Binding.Account.Instance || source.Reference.RecipientID != s.Binding.Account.ID || source.Reference.AccountTenant != s.Binding.Account.Tenant {
			return registry.Account{}, platform.Secret{}, ErrIdentity
		}
		account, secret, err := m.resolvePlatformAccountLocked(owner, s.Binding.PlatformID, "lark")
		if err != nil || account.CLIProfile == nil || larkBinding(account) != s.Binding {
			return registry.Account{}, platform.Secret{}, ErrConflict
		}
		return account, secret, nil
	}
	if source.Provider != "gitlab" {
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	var id, generation, origin string
	var user int64
	switch s := state.(type) {
	case gitlab.EventsState:
		if s.Retired || s.Status == "binding_changed" || s.Binding.Project != source.Project || (s.ProjectID != source.ProjectID && s.Binding.ProjectID != source.ProjectID) {
			return registry.Account{}, platform.Secret{}, ErrIdentity
		}
		id, generation, origin, user = s.Binding.PlatformID, s.Binding.PlatformGeneration, s.Binding.URL, s.UserID
	case gitlab.TodosState:
		if s.Retired || s.Status == "binding_changed" {
			return registry.Account{}, platform.Secret{}, ErrIdentity
		}
		id, generation, origin, user = s.Binding.PlatformID, s.Binding.PlatformGeneration, s.Binding.URL, s.Binding.UserID
	default:
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	account, secret, err := m.resolvePlatformAccountLocked(owner, id, "gitlab")
	if err != nil {
		return account, secret, ErrIdentity
	}
	if origin != source.Instance || account.URL != origin || account.Generation != generation || account.UserID != user {
		return registry.Account{}, platform.Secret{}, ErrConflict
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
	req := attachmentRequest{ServiceInfo: ServiceInfo{RoomIdentity: RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}, ServiceEpoch: q.Get("service_epoch")}, AgentID: q.Get("agent_id"), MembershipID: q.Get("membership_id"), Partition: q.Get("partition"), Seq: seq, Path: q.Get("path")}
	data, mime, _, err := m.downloadAttachment(r.Context(), req, nil, attachment.ImageLimits{})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeAPIError(w, ErrConflict)
			return
		}
		if !errors.Is(err, attachment.ErrUnavailable) && !errors.Is(err, attachment.ErrLimit) && !errors.Is(err, attachment.ErrFormat) {
			writeAPIError(w, err)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(apiError{Code: "attachment_unavailable", Error: "图片加载失败，请检查来源平台权限或附件是否仍然存在"})
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", "inline")
	_, _ = w.Write(data)
}

// Revalidate all previewed source locations and successful credential evidence
// together immediately before prompt submission. No network runs under this gate.
func (m *roomManager) checkMaterialAttachments(ctx context.Context, info ServiceInfo, reader attachmentReader, turn TurnRecord, notices []materialNotice, accesses []*attachmentAccess) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	svc, _ := m.runtimeServiceLocked(reader.RoomIdentity)
	if svc == nil || svc.info != info || m.closing || m.roomBusyLocked(reader.RoomIdentity) {
		return ErrIdentity
	}
	if err := m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(reader.MemberIdentity)}); err != nil {
		return err
	}
	svc.store.mu.Lock()
	agent, err := svc.store.runningTurnLocked(reader.MemberIdentity, reader.TurnNumber)
	svc.store.mu.Unlock()
	if err != nil || *agent.Turn != turn {
		return ErrIdentity
	}
	for _, notice := range notices {
		source, text, err := m.attachmentSourceLocked(notice.request)
		if err != nil || source != notice.source || text != notice.text {
			return ErrIdentity
		}
		if err := m.checkAttachmentReaderLocked(ctx, notice.request, &reader); err != nil {
			return err
		}
	}
	for _, access := range accesses {
		if access == nil {
			return ErrIdentity
		}
		if err := m.checkAttachmentAccessLocked(ctx, access); err != nil {
			return err
		}
	}
	return nil
}

// Formal Inbox cache replay also rechecks connected sources without images.
// This performs no network or download, and is deliberately separate from the
// read-only Room/Inbox preview contract.
func (m *roomManager) checkMCPInboxSources(ctx context.Context, info ServiceInfo, reader attachmentReader, notices []materialNotice) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, _ := m.runtimeServiceLocked(reader.RoomIdentity)
	if svc == nil || svc.info != info || m.closing || m.roomBusyLocked(reader.RoomIdentity) {
		return ErrIdentity
	}
	if err := m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: info, Caller: agentCallerIdentity(reader.MemberIdentity)}); err != nil {
		return err
	}
	for _, notice := range notices {
		if err := m.checkAttachmentReaderLocked(ctx, notice.request, &reader); err != nil {
			return err
		}
		if notice.source.Provider == "gitlab" || notice.source.Provider == "lark" {
			if _, _, err := m.attachmentAccountLocked(info.RoomIdentity, notice.source); err != nil {
				return err
			}
			continue
		}
		key := notification.Key{Owner: info.RoomIdentity, Source: notification.SourceID{Provider: notice.source.Provider, Channel: notice.source.Channel}, SubscriptionID: notice.source.SubscriptionID}
		state, exists := m.notifications.StateLocked(key)
		if !exists || state.Metadata().Retired {
			return ErrIdentity
		}
		account, _, err := m.resolvePlatformAccountLocked(info.RoomIdentity, state.Metadata().PlatformID, notice.source.Provider)
		if err != nil || account.Identity.Instance != notice.source.Instance || notice.source.Reference.RecipientID.Valid() && account.Identity.ID != notice.source.Reference.RecipientID {
			return ErrIdentity
		}
	}
	return ctx.Err()
}
