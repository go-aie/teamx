package room

// Authorization is committed before these local followups. Pending recovery
// never authorizes replaying OAuth or revoking an already adopted credential.
type platformAuthFollowup struct {
	ConnectionView   string   `json:"connection_view,omitempty"` // pending after a committed connection
	Notifications    string   `json:"notifications"`             // not_required, restored, pending
	PendingPlatforms []string `json:"pending_platforms,omitempty"`
	Onboarding       string   `json:"onboarding,omitempty"` // completed, pending
}

func (m *roomManager) authorizationFollowupLocked(ids []string, recoveryErrors map[string]error) *platformAuthFollowup {
	out := &platformAuthFollowup{Notifications: "not_required"}
	for _, id := range ids {
		pending := recoveryErrors[id] != nil
		for _, room := range m.index.Rooms {
			for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
				meta := state.Metadata()
				if meta.PlatformID != id || !meta.Enabled || meta.Retired {
					continue
				}
				if out.Notifications == "not_required" {
					out.Notifications = "restored"
				}
				if state.QueryPaused() {
					pending = true
				}
			}
		}
		if pending {
			out.Notifications = "pending"
			out.PendingPlatforms = append(out.PendingPlatforms, id)
		}
	}
	return out
}
