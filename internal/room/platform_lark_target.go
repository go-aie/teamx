package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

type larkTargetRequest struct {
	ObjectRef string `json:"object_ref"`
	TargetRef string `json:"target_ref"`
	Scope     string `json:"scope"`
	Cursor    string `json:"cursor,omitempty"`
}
type larkTargetPage struct {
	Status     platform.ResolutionStatus `json:"status"`
	ReasonCode string                    `json:"reason_code,omitempty"`
	Contents   []platform.TargetContent  `json:"contents"`
	Complete   bool                      `json:"complete"`
	Next       string                    `json:"next,omitempty"`
}

func (m *roomManager) readLarkTarget(ctx context.Context, r larkReadRequest) (any, error) {
	req := toolsRequest{ServiceInfo: r.ServiceInfo, Caller: r.Caller, Tool: "lark"}
	m.mu.Lock()
	a, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	svc, _ := m.runtimeServiceLocked(r.RoomIdentity)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer m.finishPlatformCall(call)
	if svc == nil || a.account.CLIProfile == nil {
		return nil, ErrConflict
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: r.Caller.ID, MembershipID: r.Caller.MembershipID}
	svc.store.mu.Lock()
	target, err := svc.store.loadTargetLocked(id, r.Target.ObjectRef, r.Target.TargetRef)
	svc.store.mu.Unlock()
	if err != nil {
		return nil, err
	}
	reader, err := lark.New(*a.account.CLIProfile)
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
	page, err := lark.ReadTarget(callCtx, q, access, reader)
	m.mu.Lock()
	check := m.checkLarkReadLocked(callCtx, req, a.account)
	m.mu.Unlock()
	if check != nil {
		return nil, check
	}
	if err != nil {
		return nil, ErrRequest
	}
	out := larkTargetPage{Status: page.Status, ReasonCode: page.ReasonCode, Contents: page.Contents, Complete: page.Complete}
	if page.Next != nil {
		b, _ := json.Marshal(page.Next)
		out.Next = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}
