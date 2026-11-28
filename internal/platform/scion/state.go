package scion

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

var WorkitemsSourceID = notification.SourceID{Provider: "scion", Channel: "workitems"}

type Config struct {
	Enabled         bool                `json:"enabled"`
	PlatformID      string              `json:"platform_id"`
	Target          notification.Target `json:"target"`
	Repository      string              `json:"repository"`
	ObjectTypes     []string            `json:"object_types"`
	IntervalSeconds int                 `json:"interval_seconds"`
	BackfillSeconds int                 `json:"backfill_seconds"`
	BackfillStart   time.Time           `json:"backfill_start,omitzero"`
}

func DefaultConfig(id string) Config {
	return Config{Enabled: true, PlatformID: id, ObjectTypes: []string{"issue"}, IntervalSeconds: 60}
}
func NormalizeConfig(c Config) (Config, error) {
	if c.IntervalSeconds == 0 {
		c.IntervalSeconds = 60
	}
	var e error
	c.BackfillStart, e = notification.NormalizeBackfill(c.BackfillSeconds, c.BackfillStart)
	// Review and edits of the initial range are deliberately outside the first delivery.
	if e != nil || !tool.OpaqueID(c.PlatformID) || !c.Target.Valid() || c.Target.Mode != "inbox" || !validRepository(c.Repository) || !slices.Equal(c.ObjectTypes, []string{"issue"}) || c.IntervalSeconds < 30 || c.IntervalSeconds > 3600 {
		return c, notification.ErrRequest
	}
	return c, nil
}

type Binding struct {
	PlatformID        string                   `json:"platform_id"`
	Generation        string                   `json:"generation"`
	Account           platform.AccountIdentity `json:"account"`
	CredentialVersion uint64                   `json:"credential_version"`
	Fingerprint       string                   `json:"fingerprint"`
}

func (b Binding) Valid() bool {
	return tool.OpaqueID(b.PlatformID) && tool.OpaqueID(b.Generation) && b.Account.Valid() && b.Account.Provider == "scion" && validInstance(b.Account.Instance) && b.Account.Tenant == (platform.ExternalID{}) && b.Account.ID.Namespace == "scion:subject" && b.CredentialVersion > 0 && len(b.Fingerprint) == 64 && strings.Trim(b.Fingerprint, "0123456789abcdef") == ""
}

type QueryScope struct {
	Binding         Binding               `json:"binding"`
	Profile         platform.CLIProfile   `json:"profile"`
	PlatformVersion uint64                `json:"platform_version"`
	Owner           platform.RoomIdentity `json:"owner"`
	SubscriptionID  string                `json:"subscription_id"`
	Target          notification.Target   `json:"target"`
}
type DiscoveryProgress struct {
	ExpiredSkipped int       `json:"expired_skipped"`
	Limit          int       `json:"limit"`
	Next           time.Time `json:"next,omitzero"`
	LastFull       time.Time `json:"last_full,omitzero"`
	Truncated      bool      `json:"truncated"`
	ErrorCode      string    `json:"error_code,omitempty"`
	Verify         bool      `json:"verify"`
}
type Observation struct {
	Created time.Time          `json:"created"`
	Route   notification.Route `json:"route"`
}
type State struct {
	platform.RoomIdentity
	SubscriptionID      string                       `json:"subscription_id"`
	SubscriptionVersion uint64                       `json:"subscription_version"`
	Binding             Binding                      `json:"binding"`
	Config              Config                       `json:"config"`
	Repository          RepositoryEvidence           `json:"repository"`
	Baseline            time.Time                    `json:"baseline"`
	Discovery           DiscoveryProgress            `json:"discovery"`
	Observed            map[string]Observation       `json:"observed"`
	Inbox               map[string]notification.Item `json:"inbox"`
	LastCheck           time.Time                    `json:"last_check,omitzero"`
	notification.RuntimeState
}

func hash(v any) string                        { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func repositoryID(r RepositoryEvidence) string { b, _ := json.Marshal(r); return string(b) }
func creationKey(instance string, repo RepositoryEvidence, number int64) string {
	return hash([]any{instance, repo, "issue", number, "created"})
}
func (s State) Metadata() notification.Metadata {
	return notification.Metadata{Key: notification.Key{Owner: s.RoomIdentity, Source: WorkitemsSourceID, SubscriptionID: s.SubscriptionID}, Target: s.Config.Target, Version: s.SubscriptionVersion, Enabled: s.Config.Enabled, PlatformID: s.Binding.PlatformID, Observed: len(s.Observed), Pending: len(s.Inbox), DeliveryError: s.DeliveryErrorCode}
}
func (s State) Validate(owner platform.RoomIdentity) error {
	c, e := NormalizeConfig(s.Config)
	if e != nil || !reflect.DeepEqual(c, s.Config) || owner != s.RoomIdentity || owner.RoomID == "" || owner.RoomInstance == "" || !tool.OpaqueID(s.SubscriptionID) || s.SubscriptionVersion == 0 || !s.Binding.Valid() || s.Binding.PlatformID != c.PlatformID || !s.Repository.Valid() || s.Repository.Name != c.Repository || s.Baseline.IsZero() && (c.Enabled || len(s.Observed)+len(s.Inbox) > 0 || !s.Discovery.LastFull.IsZero()) || s.Observed == nil || s.Inbox == nil || s.Discovery.Limit < 200 || s.Discovery.Limit > MaxDiscoveryLimit || s.RuntimeState.Validate(c.Enabled) != nil || !s.Baseline.IsZero() && !c.BackfillStart.IsZero() && !c.BackfillStart.Equal(s.Baseline) {
		return notification.ErrInvalid
	}
	for k, o := range s.Observed {
		if len(k) != 64 || strings.Trim(k, "0123456789abcdef") != "" || o.Created.IsZero() || !o.Route.Valid(s.SubscriptionVersion) || o.Route.Target != c.Target {
			return notification.ErrInvalid
		}
	}
	for k, item := range s.Inbox {
		o, ok := s.Observed[k]
		if !ok || k != item.Key || item.DeliveryID != item.Route.DeliveryID(notification.DeliveryID(s.Metadata().Key, k)) || o.Route != item.Route || !o.Created.Equal(item.CreatedAt) || item.Source.SubscriptionID != s.SubscriptionID || item.Source.Reference.RecipientID != s.Binding.Account.ID || item.Source.Reference.ProjectID.Value != repositoryID(s.Repository) || validateItem(item) != nil || item.Source.Instance != s.Binding.Account.Instance || item.Source.Project != s.Repository.Name || item.Source != sourceRef(s, Issue{Number: nativeNumber(item)}, k) || k != creationKey(s.Binding.Account.Instance, s.Repository, nativeNumber(item)) {
			return notification.ErrInvalid
		}
	}
	return nil
}
func (s State) Clone() State {
	b, _ := json.Marshal(s)
	var n State
	_ = json.Unmarshal(b, &n)
	return n
}
func (s State) CloneState() notification.State { return s.Clone() }
func (s State) PendingItems() []notification.Item {
	out := []notification.Item{}
	for _, i := range s.Inbox {
		out = append(out, i)
	}
	return out
}
func (s State) Notification(k string) (notification.Item, bool) { i, ok := s.Inbox[k]; return i, ok }
func (s State) Acknowledge(k string) notification.State {
	n := s.Clone()
	delete(n.Inbox, k)
	n.ResumeCapacity()
	n.DeliveryErrorCode = ""
	return n
}
func (s State) WithDeliveryError(code string) notification.State {
	n := s.Clone()
	n.DeliveryErrorCode = code
	return n
}
func (s State) InvalidateCredential() (notification.State, error) {
	n := s.Clone()
	if n.SubscriptionVersion == ^uint64(0) {
		return n, notification.ErrCapacity
	}
	n.SubscriptionVersion++
	n.RuntimeState.InvalidateCredential(n.Config.Enabled)
	return n, nil
}
func (s State) ScopeFailed(e error) notification.State {
	n := s.Clone()
	n.RuntimeState.ScopeFailed(e)
	return n
}
func Configure(owner platform.RoomIdentity, current *State, b Binding, c Config, repo RepositoryEvidence, now time.Time) (State, error) {
	c, e := NormalizeConfig(c)
	if e != nil {
		return State{}, e
	}
	if c.BackfillStart.After(now) {
		return State{}, notification.ErrRequest
	}
	var n State
	if current == nil {
		baseline, e := notification.BackfillBaseline(c.BackfillSeconds, c.BackfillStart, now)
		if e != nil {
			return n, e
		}
		var id [16]byte
		if _, e = rand.Read(id[:]); e != nil {
			return n, e
		}
		if !c.Enabled {
			baseline = time.Time{}
		}
		n = State{RoomIdentity: owner, SubscriptionID: hex.EncodeToString(id[:]), Baseline: baseline, Repository: repo, Discovery: DiscoveryProgress{Limit: 200, Verify: true}, Observed: map[string]Observation{}, Inbox: map[string]notification.Item{}}
	} else {
		n = current.Clone()
		if b.PlatformID != n.Binding.PlatformID || b.Generation != n.Binding.Generation || b.Account != n.Binding.Account || repo != n.Repository {
			return n, notification.ErrConflict
		}
		if c.Repository != n.Config.Repository || !slices.Equal(c.ObjectTypes, n.Config.ObjectTypes) || c.BackfillSeconds != n.Config.BackfillSeconds || !c.BackfillStart.Equal(n.Config.BackfillStart) || c.Target != n.Config.Target {
			return n, fmt.Errorf("%w: repository, object types and start time are fixed for this subscription", notification.ErrRequest)
		}
	}
	if n.Baseline.IsZero() && c.Enabled {
		baseline, e := notification.BackfillBaseline(c.BackfillSeconds, c.BackfillStart, now)
		if e != nil {
			return n, e
		}
		n.Baseline = baseline
	}
	if n.SubscriptionVersion == ^uint64(0) {
		return n, notification.ErrCapacity
	}
	n.SubscriptionVersion++
	n.Binding = b
	n.Config = c
	n.Discovery.Next = time.Time{}
	n.Discovery.Verify = true
	n.RuntimeState.Reset(c.Enabled, n.Discovery.LastFull.IsZero())
	return n, n.Validate(owner)
}
func (s State) Expire(cutoff time.Time) notification.State {
	n := s.Clone()
	if cutoff.After(n.ExpiredBefore) {
		n.ExpiredBefore = cutoff
	}
	for k, o := range n.Observed {
		if notification.Expired(o.Created, n.ExpiredBefore) {
			if _, ok := n.Inbox[k]; ok {
				n.Metrics.ExpiredSkipped++
			}
			delete(n.Inbox, k)
			delete(n.Observed, k)
		}
	}
	if len(n.Inbox) < len(s.Inbox) || len(n.Observed) < len(s.Observed) {
		n.ResumeCapacity()
	}
	return n
}
func (s State) SourceTerminal(item notification.Item) (string, bool) {
	if validateItem(item) != nil || item.Source.SubscriptionID != s.SubscriptionID || item.Source.Instance != s.Binding.Account.Instance || item.Source.Reference.RecipientID != s.Binding.Account.ID || item.Source.Reference.ProjectID.Value != repositoryID(s.Repository) {
		return "", false
	}
	if o, ok := s.Observed[item.Key]; ok && o.Route == item.Route {
		return "committed_observation", true
	}
	return "committed_expiry_floor", notification.Expired(item.CreatedAt, s.ExpiredBefore)
}
