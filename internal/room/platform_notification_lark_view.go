package room

import (
	"github.com/go-aie/teamx/internal/platform/lark"
	"time"
)

type larkChatDiagnostic struct {
	ID           string     `json:"chat_id"`
	Name         string     `json:"name"`
	Mode         string     `json:"mode"`
	LastCheck    time.Time  `json:"last_check,omitzero"`
	Completed    int64      `json:"completed"`
	ErrorCode    string     `json:"error_code,omitempty"`
	RetryAt      time.Time  `json:"retry_at,omitzero"`
	Unlisted     bool       `json:"unlisted,omitempty"`
	Scan         *lark.Scan `json:"scan,omitempty"`
	Relationship string     `json:"relationship"`
}
type larkDiagnostics struct {
	ExcludedItems     int                  `json:"excluded_items"`
	CLIVersion        string               `json:"cli_version"`
	PrivateScope      string               `json:"private_scope"`
	Baseline          time.Time            `json:"baseline,omitzero"`
	DirectoryLastFull time.Time            `json:"directory_last_full,omitzero"`
	DirectoryPages    int                  `json:"directory_pages"`
	DirectoryScanning bool                 `json:"directory_scanning"`
	FailedChats       int                  `json:"failed_chats"`
	UnlistedChats     int                  `json:"unlisted_chats"`
	GapChats          int                  `json:"gap_chats"`
	RequestIntervalMS int                  `json:"request_interval_ms"`
	DirectoryComplete bool                 `json:"directory_complete"`
	Unchecked         int                  `json:"unchecked"`
	OldestLagSeconds  int64                `json:"oldest_lag_seconds"`
	Chats             []larkChatDiagnostic `json:"chats"`
}

func (m *roomManager) larkNotificationViewLocked(owner RoomIdentity, s *lark.State) (notificationView, error) {
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return notificationView{}, ErrIdentity
	}
	v := notificationView{RoomIdentity: owner, SourceID: lark.MessagesSourceID, Available: true, Status: "disabled", Config: lark.DefaultConfig(""), Lark: &larkDiagnostics{CLIVersion: lark.CLIVersion, Chats: []larkChatDiagnostic{}}}
	if s == nil {
		return v, nil
	}
	v.SubscriptionID, v.Version, v.Config, v.Status, v.Pending = s.SubscriptionID, s.SubscriptionVersion, s.Config, s.Status, s.Metadata().Pending
	v.Lark.ExcludedItems = s.ExcludedItems()
	v.LastCheck, v.LastFull, v.RetryAt, v.ErrorCode, v.Metrics, v.DeliveryErrorCode = s.LastCheck, s.LastFull, s.RetryAt, s.ErrorCode, s.Metrics, s.DeliveryErrorCode
	v.Retired = s.Retired
	v.Account = s.Binding.Account
	v.PlatformID = s.Binding.PlatformID
	v.Instance = s.Binding.Account.Instance
	v.Lark.Baseline = s.Baseline
	if code := m.notificationTargetErrorLocked(owner, s.PendingItems()); code != "" {
		v.DeliveryErrorCode = code
	}
	v.Lark.PrivateScope = "specified"
	v.Lark.DirectoryComplete = true
	v.Lark.RequestIntervalMS = int(lark.AccountRequestInterval.Milliseconds())
	if s.Config.DiscoverPrivate() {
		v.Lark.PrivateScope = "all"
		v.Lark.DirectoryComplete = s.Directory != nil && !s.Directory.LastFull.IsZero() && s.Directory.Scan == nil
		if s.Directory != nil {
			v.Lark.DirectoryLastFull = s.Directory.LastFull
			if s.Directory.Scan != nil {
				v.Lark.DirectoryScanning = true
				v.Lark.DirectoryPages = s.Directory.Scan.Pages
			}
		}
	}
	for _, p := range s.Chats {
		if p.Unlisted {
			v.Lark.UnlistedChats++
		}
	}
	for _, id := range s.ChatIDs() {
		p, ok := s.Chats[id]
		d := larkChatDiagnostic{ID: id, Name: p.Chat.Name, Mode: p.Chat.Mode, LastCheck: p.LastCheck, Completed: p.Completed, ErrorCode: p.ErrorCode, RetryAt: p.RetryAt, Unlisted: p.Unlisted}
		d.Relationship = "business"
		if !ok || p.Chat.Mode == "p2p" {
			d.Relationship = "pending"
			if p.Chat.Relation.ValidFor(s.Binding.Account, id) && !p.NeedsVerification {
				d.Relationship = p.Chat.Relation.Kind
			}
		}
		if !ok && !s.Config.DiscoverPrivate() {
			v.Lark.DirectoryComplete = false
		}
		if !ok || p.Completed == 0 || p.Scan != nil || p.NeedsVerification || p.ErrorCode != "" || m.notifications.Now().Unix()-p.Completed >= int64(s.Config.IntervalSeconds) {
			v.Lark.Unchecked++
		}
		if p.Scan != nil {
			x := *p.Scan
			x.Token = ""
			d.Scan = &x
		}
		if p.ErrorCode != "" {
			v.Lark.FailedChats++
		}
		lag := m.notifications.Now().Unix() - max(s.Baseline.Unix(), p.Completed)
		if lag > 86400 {
			v.Lark.GapChats++
		}
		v.Lark.OldestLagSeconds = max(v.Lark.OldestLagSeconds, lag)
		v.Lark.Chats = append(v.Lark.Chats, d)
	}
	// No page tokens, profile details or message bodies are exposed here.
	return v, nil
}
