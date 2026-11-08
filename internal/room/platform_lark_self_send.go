package room

import (
	"context"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// Native verification runs outside manager.mu. The final binding mutation checks
// the same connection version and generation, membership and settings revision.
func (m *roomManager) prepareLarkBindingEvidence(ctx context.Context, req *toolBindingChange) error {
	if req.Binding.Tool != "lark" {
		return ErrRequest
	}
	q := larkChatsRequest{ServiceEpoch: req.ServiceEpoch, RoomIdentity: req.RoomIdentity, Caller: req.Caller, ConnectionID: req.Binding.ConnectionID, ConnectionVersion: req.ConnectionVersion}
	m.mu.Lock()
	a, err := m.checkLarkChatsLocked(ctx, q, nil)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	call := &platformCall{owner: req.RoomIdentity, connectionID: a.ConnectionID, cancel: cancel, done: make(chan struct{})}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	ctx = callCtx
	pin, err := lark.Pin(ctx, *a.CLIProfile, a.Identity)
	if err != nil {
		return err
	}
	defer pin.Close()
	r, err := lark.ResolveSendTarget(ctx, platform.TargetLocator{Self: true}, larkReferenceAccess(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Binding.AgentID, MembershipID: req.Binding.MembershipID}, a, platform.SourceRef{}), pin.SendReader())
	if err != nil {
		return err
	}
	if r.Status != platform.ResolutionResolved || r.Target == nil {
		return conflictReason("self_delivery", "本人自聊关系待核验，未启用投递", "Verify native self membership and target permissions.")
	}
	m.mu.Lock()
	_, err = m.checkLarkChatsLocked(ctx, q, &a)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	req.Binding.CapabilityEvidence = []platform.CapabilityEvidence{{Kind: "self_delivery", AccountOwnerConfirmed: true, CredentialVersion: a.CredentialVersion, Account: a.Identity, Target: *r.Target, Operator: req.Caller.ID}}
	return nil
}
