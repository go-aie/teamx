package room

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// One source resolution owns its admitted call through durable linkage. This
// private scope carries no identity DTO and is never persisted or sent to Agents.
type referenceResolutionScope struct {
	ctx    context.Context
	finish func()
}
type referenceResolutionScopeKey struct{}

// Static capability descriptions require neither credentials nor tool processes.
type referenceAdapterFactory func() platform.ReferenceCapabilities

func (m *roomManager) initPlatformReferences() {
	if m.bindingEvidencePreparers == nil {
		m.bindingEvidencePreparers = map[string]bindingEvidencePreparer{"lark": m.prepareLarkBindingEvidence}
	}
	if m.inboxAdmissionResolvers == nil {
		m.inboxAdmissionResolvers = map[string]inboxAdmissionResolver{"lark": m.resolveLarkInboxAdmission}
	}
	if m.referenceAdapters == nil {
		m.referenceAdapters = map[string]referenceAdapterFactory{
			"gitlab": gitlabReferenceCapabilities,
			"lark": func() platform.ReferenceCapabilities {
				return lark.AcceptedActionCapabilities(nil, nil, nil)
			},
		}
	}
	if m.referenceExecutors == nil {
		m.referenceExecutors = map[string]referenceActionPreparer{"gitlab": m.prepareGitlabReferenceAction, "lark": m.prepareLarkReferenceAction}
	}
	if m.actionResolvers == nil {
		m.actionResolvers = map[string]actionTargetResolver{"lark": m.resolveLarkActionTarget}
	}
	if m.referenceResolvers == nil {
		m.referenceResolvers = map[string]followupResolver{"gitlab": m.resolveGitlabFollowup, "lark": m.resolveLarkFollowup}
	}
}

func (m *roomManager) referenceCapabilities(provider string) (platform.ReferenceCapabilities, error) {
	factory := m.referenceAdapters[provider]
	if factory == nil {
		return platform.ReferenceCapabilities{}, platform.ErrReference
	}
	return factory(), nil
}

// Shared by runtime assembly and isolated provider acceptance. Registration
// changes only the adapter maps; ownership and capability dispatch stay here.
func (m *roomManager) bindPlatformReferences(svc *service) {
	svc.maintainInbox.Store(&inboxMaintenance{run: func(ctx context.Context, id MemberIdentity) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closing {
			return ErrClosing
		}
		current, _ := m.runtimeServiceLocked(id.RoomIdentity)
		if current != svc {
			return ErrBusy
		}
		target := notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}
		if err := m.notifications.ExpireTargetLocked(id.RoomIdentity, target); err != nil {
			return notificationRoomError(err)
		}
		return svc.maintainInboxLocal(ctx, id, m.notifications.PendingDeliveriesLocked(id.RoomIdentity, target), m.notifications.Now())
	}})

	svc.resolveInboxAdmission = func(ctx context.Context, source platform.SourceRef) (json.RawMessage, error) {
		resolve := m.inboxAdmissionResolvers[source.Provider]
		if resolve == nil {
			return nil, ErrBusy
		}
		return resolve(ctx, svc, source)
	}
	svc.materialAttachments = &materialAttachments{download: m.downloadAttachment, check: m.checkMaterialAttachments}
	svc.beginPlatformMemberChange = func(ctx context.Context, id MemberIdentity) (func(), error) {
		return m.beginPlatformMemberChange(ctx, svc, id)
	}
	// Serialize only the final evidence check and durable linkage with binding
	// changes. Network requests and cancellation waits never hold this gate.
	svc.referenceCommit = func(ctx context.Context, id MemberIdentity, access platform.AccessContext, commit func() (followupEvent, error)) (followupEvent, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		req := toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Tool: access.Account.Provider}
		current, _ := m.runtimeServiceLocked(id.RoomIdentity)
		if current != svc {
			return followupEvent{}, ErrIdentity
		}
		if err := m.checkToolMemberLocked(ctx, req); err != nil {
			return followupEvent{}, err
		}
		if scope, _ := ctx.Value(referenceResolutionScopeKey{}).(*referenceResolutionScope); scope != nil && scope.ctx != nil {
			if err := scope.ctx.Err(); err != nil {
				return followupEvent{}, err
			}
		}

		if access.Valid() && access.Account.Provider == "gitlab" {
			if err := m.checkGitlabReferenceAccessLocked(ctx, req, access); err != nil {
				return followupEvent{}, err
			}
		}
		if access.Valid() && access.Account.Provider == "lark" {
			if err := m.checkLarkReferenceAccessLocked(ctx, req, access); err != nil {
				return followupEvent{}, err
			}
		}
		return commit()
	}

	svc.referenceSpecs = func(provider string) []platform.ActionSpec {
		capabilities, _ := m.referenceCapabilities(provider)
		specs := []platform.ActionSpec{}
		for _, action := range capabilities.Actions {
			specs = append(specs, action.Spec)
		}
		sort.Slice(specs, func(i, j int) bool { return specs[i].Operation < specs[j].Operation })
		return specs
	}
	svc.referenceResolver = func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		m.mu.Lock()
		current, _ := m.runtimeServiceLocked(id.RoomIdentity)
		resolve := m.referenceResolvers[source.Provider]
		closing := m.closing
		m.mu.Unlock()
		if closing || current != svc {
			return platform.TargetResolution{}, platform.AccessContext{}, ErrIdentity
		}
		if resolve == nil {
			return platform.TargetResolution{}, platform.AccessContext{}, platform.ErrReference
		}
		return resolve(ctx, id, source)
	}
}

// The managed resolver path also supports direct in-package contract tests.
func (m *roomManager) readReferenceInbox(ctx context.Context, req apiRequest) (referenceInboxPage, error) {
	tr := toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}
	m.mu.Lock()
	err := m.checkToolMemberLocked(ctx, tr)
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	m.mu.Unlock()
	if err != nil {
		return referenceInboxPage{}, err
	}
	if svc == nil || req.AgentID != "" || req.MembershipID != "" {
		return referenceInboxPage{}, ErrIdentity
	}
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	return svc.readReferenceInbox(ctx, id, req.TurnNumber, req.Limit, func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		m.mu.Lock()
		current, _ := m.runtimeServiceLocked(req.RoomIdentity)
		if current != svc || m.checkToolMemberLocked(ctx, tr) != nil {
			m.mu.Unlock()
			return platform.TargetResolution{}, platform.AccessContext{}, ErrIdentity
		}
		resolve := m.referenceResolvers[source.Provider]
		m.mu.Unlock()
		if resolve == nil {
			return platform.TargetResolution{}, platform.AccessContext{}, platform.ErrReference
		}
		return resolve(ctx, id, source)
	})
}
