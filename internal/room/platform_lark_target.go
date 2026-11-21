package room

import (
	"context"
	"encoding/base64"

	larkruntime "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	"github.com/go-aie/teamx/internal/app/platformtools"
	"github.com/go-aie/teamx/internal/platform"
)

type larkTargetRequest = platformtools.ReadRequest
type larkTargetPage = larkruntime.TargetPage

func (m *roomManager) readLarkTarget(ctx context.Context, r larkReadRequest) (any, error) {
	req := toolsRequest{ServiceInfo: r.ServiceInfo, Caller: r.Caller, Tool: "lark"}
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(r.RoomIdentity)
	if svc == nil {
		m.mu.Unlock()
		return nil, ErrIdentity
	}
	if err := m.checkToolMemberLocked(ctx, req); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: r.Caller.ID, MembershipID: r.Caller.MembershipID}
	// Recover the original platform even when captured notification evidence has
	// no live access stamp. Explicit entry constraints are checked on admission.
	svc.store.mu.Lock()
	err := svc.store.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if e.TargetRef != r.Target.TargetRef && e.ObjectRef != r.Target.TargetRef {
			return nil
		}
		p := e.Access.PlatformID
		if p == "" {
			var err error
			p, err = m.sourcePlatformLocked(r.RoomIdentity, e.Source)
			if err != nil {
				return err
			}
		}
		if req.platformID != "" && req.platformID != p {
			return ErrIdentity
		}
		req.platformID = p
		return nil
	})
	svc.store.mu.Unlock()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if req.platformID == "" {
		m.mu.Unlock()
		return nil, ErrIdentity
	}
	a, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer m.finishPlatformCall(call)
	if a.account.CLIProfile == nil {
		return nil, ErrConflict
	}
	svc.store.mu.Lock()
	target, err := svc.store.loadTargetLocked(id, r.Target.ObjectRef, r.Target.TargetRef)
	svc.store.mu.Unlock()
	if err != nil {
		return nil, err
	}
	reader, err := m.larkClients.New(*a.account.CLIProfile)
	if err != nil {
		return nil, err
	}
	access := larkReferenceAccess(id, a.account, platform.SourceRef{})
	q := platform.ReadTargetRequest{Target: target.Target, Scope: r.Target.Scope, Limits: platform.ReadLimits{Items: 25, Bytes: 64 << 10, BodyBytes: 8 << 10}}
	if r.Target.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(r.Target.Cursor)
		if e != nil || len(b) > 16384 {
			return nil, ErrRequest
		}
		var c platform.ReadCursor
		if strictToolJSON(b, &c) != nil {
			return nil, ErrRequest
		}
		q.Cursor = &c
	}
	page, err := larkruntime.ReadTarget(callCtx, q, access, reader)
	m.mu.Lock()
	check := m.checkLarkReadLocked(callCtx, req, a.account)
	m.mu.Unlock()
	if check != nil {
		return nil, check
	}
	if err != nil {
		return nil, ErrRequest
	}
	return page, nil
}
