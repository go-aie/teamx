package room

import (
	"context"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func larkReferenceAccess(id MemberIdentity, account connection.Account, source platform.SourceRef) platform.AccessContext {
	a := platform.AccessContext{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: id.MembershipID, Account: account.Identity, ConnectionID: account.ConnectionID, Generation: account.Generation, ConnectionVersion: account.ConnectionVersion, CredentialVersion: account.CredentialVersion}
	if source.Reference.RecipientID.Valid() {
		a.Receiver = platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: source.Instance, Tenant: source.Reference.AccountTenant}, ID: source.Reference.RecipientID}
	}
	return a
}
func (m *roomManager) checkLarkReferenceAccessLocked(ctx context.Context, req toolsRequest, access platform.AccessContext) error {
	a, _, err := m.resolvePlatformAccountLocked(req.RoomIdentity, access.ConnectionID, "lark")
	if err != nil {
		return err
	}
	if a.Identity != access.Account || a.Generation != access.Generation || a.ConnectionVersion != access.ConnectionVersion || a.CredentialVersion != access.CredentialVersion {
		return ErrConflict
	}
	return m.checkLarkReadLocked(ctx, req, a)
}
func (m *roomManager) resolveLarkFollowup(ctx context.Context, _ MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
	if err := ctx.Err(); err != nil {
		return platform.TargetResolution{}, platform.AccessContext{}, err
	}
	// The caller loads this envelope from the member's immutable Inbox entry.
	// Member/service checks still guard admission and commit; remote reads remain
	// exclusively behind readLark's selected connection and call lifecycle.
	return lark.ResolveNotification(source, time.Now().UTC()), platform.AccessContext{}, nil
}

func validateNotificationResolution(e InboxEntry, r platform.TargetResolution, a platform.AccessContext) error {
	want := lark.ResolveNotification(e.Item.Source, r.CheckedAt)
	if a != (platform.AccessContext{}) || r.EvidenceSource != lark.NotificationEvidence ||
		r.Status != want.Status || r.ReasonCode != want.ReasonCode || r.Account != want.Account ||
		!reflect.DeepEqual(r.Target, want.Target) || r.ObjectVerified != want.ObjectVerified ||
		r.DiscussionVerified != want.DiscussionVerified || r.MessageVerified != want.MessageVerified ||
		r.Complete != want.Complete || len(r.Aliases) != 0 || r.URL != "" {
		return platform.ErrReference
	}
	return nil
}
