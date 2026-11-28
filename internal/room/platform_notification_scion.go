package room

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func scionBinding(a registry.Account) scion.Binding {
	return scion.Binding{PlatformID: a.PlatformID, Generation: a.Generation, Account: a.Identity, CredentialVersion: a.CredentialVersion, Fingerprint: a.CredentialFingerprint}
}

// Native output is discarded on failure; stderr never crosses the diagnostics boundary.
type scionQueryError string

func (e scionQueryError) Error() string            { return string(e) }
func (e scionQueryError) NotificationCode() string { return string(e) }

type scionOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (b *scionOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data)+len(p) > b.limit {
		b.overflow = true
		b.cancel()
		return 0, errors.New("output_limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (m *roomManager) executeScionNotification(ctx context.Context, q scion.QueryScope, args ...string) ([]byte, error) {
	m.mu.Lock()
	if m.closing || m.roomBusyLocked(q.Owner) {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	a, _, err := m.resolvePlatformAccountLocked(q.Owner, q.Binding.PlatformID, "scion")
	if err != nil || scionBinding(a) != q.Binding || a.CLIProfile == nil {
		m.mu.Unlock()
		return nil, notification.ErrConflict
	}
	if err = m.checkNotificationTargetLocked(ctx, q.Owner, q.Target); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if q.SubscriptionID != "" {
		key := notification.Key{Owner: q.Owner, Source: scion.WorkitemsSourceID, SubscriptionID: q.SubscriptionID}
		state, ok := m.notifications.StateLocked(key)
		if !ok || !state.Metadata().Enabled || state.Metadata().Target != q.Target {
			m.mu.Unlock()
			return nil, notification.ErrConflict
		}
	}
	execution, err := m.scionProgram(a.CLIProfile)
	if err != nil {
		m.mu.Unlock()
		return nil, scionQueryError("cli_unavailable")
	}
	use, cancel := notification.QueryContext(ctx, 30*time.Second)
	call := &platformCall{owner: q.Owner, tool: "scion", platformID: a.PlatformID, account: a, agentID: q.Target.AgentID, membershipID: q.Target.MembershipID, subscription: notification.Key{Owner: q.Owner, Source: scion.WorkitemsSourceID, SubscriptionID: q.SubscriptionID}}
	call.Call = m.platformCalls.Register(call, cancel)
	root := m.store.dir
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	defer cancel()
	out := &scionOutput{limit: scion.MaxNotificationOutput, cancel: cancel}
	stderr := &scionOutput{limit: 64 << 10, cancel: cancel}
	var preparation error
	p := tool.Process{TempRoot: m.config.platformTempRoot, Definition: execution, Output: out, Error: stderr, Prepare: func(ctx context.Context, private string) (map[string]string, error) {
		values, e := scion.Prepare(ctx, root, a.URL, *a.CLIProfile, a.Identity, private, execution)
		preparation = e
		return values, e
	}}
	code := p.Run(use, args)
	if preparation != nil {
		switch preparation.Error() {
		case "identity_mismatch":
			return nil, notification.ErrConflict
		case "credential_unavailable", "credential_rejected":
			return nil, platform.ErrCredential
		default:
			return nil, scionQueryError(preparation.Error())
		}
	}
	if out.overflow || stderr.overflow {
		return nil, scionQueryError("output_limit")
	}
	if use.Err() != nil {
		return nil, use.Err()
	}
	if code != 0 {
		return nil, scionQueryError(fmt.Sprintf("native_exit_%d", code))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, _, e := m.resolvePlatformAccountLocked(q.Owner, q.Binding.PlatformID, "scion")
	if e != nil || scionBinding(current) != q.Binding || current.PlatformVersion != q.PlatformVersion {
		return nil, notification.ErrConflict
	}
	if e = m.checkNotificationTargetLocked(ctx, q.Owner, q.Target); e != nil {
		return nil, e
	}
	return out.data, nil
}
func (m *roomManager) configureScionSubscription(ctx context.Context, key notification.Key, version uint64, cfg scion.Config) (scion.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkPlatformMemberContextLocked(ctx); err != nil {
		return scion.State{}, err
	}
	if key.Source != scion.WorkitemsSourceID || m.notifications.Source(scion.WorkitemsSourceID) == nil {
		return scion.State{}, ErrRequest
	}
	if m.closing || m.roomBusyLocked(key.Owner) {
		return scion.State{}, ErrBusy
	}
	if err := m.validateNotificationTargetLocked(ctx, key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return scion.State{}, err
	}
	currentState, err := m.notifications.CurrentLocked(key.Owner, scion.WorkitemsSourceID, cfg.PlatformID, cfg.Target)
	if err != nil {
		return scion.State{}, notificationRoomError(err)
	}
	var current *scion.State
	if currentState != nil {
		s, ok := currentState.(scion.State)
		if !ok {
			return scion.State{}, ErrInvalid
		}
		current = &s
	}
	checkVersion := func() error {
		if key.SubscriptionID == "" {
			if version != 0 || current != nil {
				return ErrConflict
			}
		} else if current == nil || current.SubscriptionID != key.SubscriptionID || current.SubscriptionVersion != version {
			return ErrConflict
		}
		return nil
	}
	if err = checkVersion(); err != nil {
		return scion.State{}, err
	}
	var a registry.Account
	var b scion.Binding
	origin := ""
	if current != nil {
		b = current.Binding
		origin = b.Account.Instance
	}
	if cfg.Enabled || current == nil {
		var err error
		a, _, err = m.resolvePlatformAccountLocked(key.Owner, cfg.PlatformID, "scion")
		if err != nil {
			return scion.State{}, notificationRoomError(err)
		}
		b = scionBinding(a)
		origin = a.URL
	}
	cfg.Repository, err = scion.NormalizeRepository(origin, cfg.Repository)
	if err != nil {
		return scion.State{}, notificationRoomError(err)
	}
	cfg, err = scion.NormalizeConfig(cfg)
	if err != nil {
		return scion.State{}, notificationRoomError(err)
	}
	var repo scion.RepositoryEvidence
	if current != nil {
		repo = current.Repository
		if _, err = scion.Configure(key.Owner, current, b, cfg, repo, m.notifications.Now()); err != nil {
			return scion.State{}, notificationRoomError(err)
		}
	} else {
		q := scion.QueryScope{Owner: key.Owner, Binding: b, Profile: *a.CLIProfile, PlatformVersion: a.PlatformVersion, Target: cfg.Target}
		m.mu.Unlock()
		repo, err = scion.ReadRepository(ctx, m.executeScionNotification, q, cfg.Repository)
		m.mu.Lock()
		if err != nil {
			return scion.State{}, notificationRoomError(err)
		}
		latest, _, e := m.resolvePlatformAccountLocked(key.Owner, cfg.PlatformID, "scion")
		if e != nil || scionBinding(latest) != b || latest.PlatformVersion != a.PlatformVersion {
			return scion.State{}, ErrConflict
		}
		active, e := m.notifications.CurrentLocked(key.Owner, scion.WorkitemsSourceID, cfg.PlatformID, cfg.Target)
		if e != nil || active != nil {
			return scion.State{}, ErrConflict
		}
	}
	if err = ctx.Err(); err != nil {
		return scion.State{}, err
	}
	if err = m.validateNotificationTargetLocked(ctx, key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return scion.State{}, err
	}
	if err = m.checkPlatformMemberContextLocked(ctx); err != nil {
		return scion.State{}, err
	}
	next, err := scion.Configure(key.Owner, current, b, cfg, repo, m.notifications.Now())
	if err != nil {
		return next, notificationRoomError(err)
	}
	if err = m.saveNotificationLocked(next, current); err != nil {
		return next, notificationRoomError(err)
	}
	done := m.notifications.ChangedLocked(next.Metadata().Key)
	waits := m.cancelAttachmentCallsLocked(next.Metadata().Key)
	if done != nil {
		waits = append(waits, done)
	}
	m.mu.Unlock()
	err = notification.Wait(ctx, waits)
	m.mu.Lock()
	if err != nil {
		return next, err
	}
	m.notifications.EnsureLocked(next.Metadata().Key)
	return next, nil
}
func (m *roomManager) syncScionSubscription(ctx context.Context, key notification.Key, version uint64, target notification.Target) error {
	m.mu.Lock()
	state, ok := m.notifications.StateLocked(key)
	m.mu.Unlock()
	if !ok {
		return ErrIdentity
	}
	s, ok := state.(scion.State)
	if !ok || s.SubscriptionVersion != version {
		return ErrConflict
	}
	if s.Config.Target != target {
		return ErrIdentity
	}
	if !s.Config.Enabled {
		return ErrRequest
	}
	next, e := m.configureScionSubscription(ctx, key, version, s.Config)
	if e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifications.WakeLocked(next.Metadata().Key)
	return nil
}
func (m *roomManager) verifyScionNotificationRepository(ctx context.Context, owner RoomIdentity, cfg scion.Config) (scion.RepositoryEvidence, error) {
	m.mu.Lock()
	a, _, e := m.resolvePlatformAccountLocked(owner, cfg.PlatformID, "scion")
	if e != nil {
		m.mu.Unlock()
		return scion.RepositoryEvidence{}, e
	}
	if m.notifications.Source(scion.WorkitemsSourceID) == nil {
		m.mu.Unlock()
		return scion.RepositoryEvidence{}, ErrRequest
	}
	if e = m.validateNotificationTargetLocked(ctx, owner, cfg.Target, true); e != nil {
		m.mu.Unlock()
		return scion.RepositoryEvidence{}, e
	}
	name, e := scion.NormalizeRepository(a.URL, cfg.Repository)
	if e != nil {
		m.mu.Unlock()
		return scion.RepositoryEvidence{}, notificationRoomError(e)
	}
	q := scion.QueryScope{Owner: owner, Target: cfg.Target, Binding: scionBinding(a), Profile: *a.CLIProfile, PlatformVersion: a.PlatformVersion}
	m.mu.Unlock()
	return scion.ReadRepository(ctx, m.executeScionNotification, q, name)
}
func (m *roomManager) resumeScionSubscriptionsLocked(id string) error {
	for _, room := range m.index.Rooms {
		if room.Status != "ready" || m.roomBusyLocked(room.RoomIdentity) {
			continue
		}
		for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
			s, ok := state.(scion.State)
			if !ok || !s.Config.Enabled || s.Binding.PlatformID != id || s.Status != "credentials_invalid" {
				continue
			}
			a, _, e := m.resolvePlatformAccountLocked(room.RoomIdentity, id, "scion")
			if e != nil {
				return e
			}
			b := scionBinding(a)
			if b.Generation != s.Binding.Generation || b.Account != s.Binding.Account {
				continue
			}
			next, e := scion.Configure(s.RoomIdentity, &s, b, s.Config, s.Repository, m.notifications.Now())
			if e != nil {
				return notificationRoomError(e)
			}
			if e = m.saveNotificationLocked(next, s); e != nil {
				return e
			}
			m.notifications.ChangedLocked(next.Metadata().Key)
			m.notifications.EnsureLocked(next.Metadata().Key)
		}
	}
	return nil
}
func (m *roomManager) scionNotificationViewLocked(owner RoomIdentity, s *scion.State) (notificationView, error) {
	v := notificationView{RoomIdentity: owner, SourceID: scion.WorkitemsSourceID, Available: true, Status: "disabled", Config: scion.DefaultConfig("")}
	if s == nil {
		return v, nil
	}
	m.setNotificationTargetViewLocked(&v, s.Config.Target)
	v.PlatformID = s.Binding.PlatformID
	v.SubscriptionID = s.SubscriptionID
	v.Version = s.SubscriptionVersion
	v.Config = s.Config
	v.Status = s.Status
	v.Pending = len(s.Inbox)
	v.Account = s.Binding.Account
	v.Instance = s.Binding.Account.Instance
	v.Baseline = s.Baseline
	v.EffectiveAfter = s.Baseline
	policy := m.config.inboxExpiry.Resolved()
	if policy.SkipExpired {
		cutoff := policy.Cutoff(m.notifications.Now())
		if cutoff.After(v.EffectiveAfter) {
			v.EffectiveAfter = cutoff
		}
	}
	if s.ExpiredBefore.After(v.EffectiveAfter) {
		v.EffectiveAfter = s.ExpiredBefore
	}
	v.LastCheck = s.LastCheck
	v.LastFull = s.Discovery.LastFull
	v.RetryAt = s.RetryAt
	v.ErrorCode = s.ErrorCode
	v.DeliveryErrorCode = s.DeliveryErrorCode
	v.Metrics = s.Metrics
	v.Scion = &s.Discovery
	return v, nil
}
