package room

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// Published only while holding manager.mu (or during exclusive startup).
// Everything reachable from this snapshot is immutable, including names and
// ServiceInfo. Readers never retain a mutable runtime or index entry.
type roomsCatalogSnapshot struct {
	rooms     []roomRuntimeSummary
	teams     []teamSummary
	assistant *defaultAssistantRecord
	busy      bool
	names     *agentNameRegistry
}

func (m *roomManager) publishRoomsCatalogLocked() {
	view := &roomsCatalogSnapshot{rooms: make([]roomRuntimeSummary, 0, len(m.index.Rooms)), teams: m.teamSummaries(), busy: m.busy, names: &agentNameRegistry{}}
	if m.agentNames != nil {
		view.names.value.Store(m.agentNames.value.Load())
	}
	if m.index.DefaultAssistant != nil {
		record := *m.index.DefaultAssistant
		view.assistant = &record
	}
	for _, entry := range m.index.Rooms {
		summary := roomRuntimeSummary{roomSummary: entry.roomSummary, RuntimeStatus: "closed", Busy: m.roomBusyLocked(entry.RoomIdentity)}
		if entry.LastMessageAt != nil {
			stamp := *entry.LastMessageAt
			summary.LastMessageAt = &stamp
		}
		if svc, rt := m.runtimeServiceLocked(entry.RoomIdentity); rt != nil {
			summary.RuntimeStatus, summary.RuntimeError = rt.status, rt.err
			if rt.status == "ready" && svc != nil && !svc.closing.Load() {
				info := svc.info
				summary.Service = &info
			}
		}
		view.rooms = append(view.rooms, summary)
	}
	m.catalog.Store(view)
}

// Install the readable view before notifying clients. Observers only signal;
// they never rebuild the catalog or call into a producer that holds its lock.
func (m *roomManager) catalogChangedLocked() {
	m.publishRoomsCatalogLocked()
	m.updates.mark(uiUpdate{Topic: "catalog"})
}

func (m *roomManager) serveRoomsCatalog(w http.ResponseWriter, r *http.Request) {
	if err := m.store.check(false); err != nil {
		writeAPIError(w, err)
		return
	}
	view := m.catalog.Load()
	rooms := make([]roomRuntimeSummary, 0, len(view.rooms))
	for _, base := range view.rooms {
		summary := base
		summary.SummaryStatus = "ready"
		dir := m.roomDir(base.RoomID)
		if info, err := os.Stat(filepath.Join(dir, "room.json")); err == nil && info.ModTime().After(summary.UpdatedAt) {
			summary.UpdatedAt = info.ModTime().UTC()
		}
		// Both files are committed snapshots. No Room/Store/manager gate is
		// acquired, and receipts are projected for this request's operator only.
		snapshot, snapshotErr, reading := readHumanReadCatalog(dir, base.RoomIdentity, requestOperator(r), humanOperator())
		summary.HumanRead = reading
		if snapshotErr != nil {
			summary.SummaryStatus, summary.SummaryError = "unavailable", snapshotErr.Error()
		} else if len(snapshot.Messages) > 0 {
			last := snapshot.Messages[len(snapshot.Messages)-1]
			summary.LastMessageAt = &last.CreatedAt
			summary.LastMessagePreview = summarizeRoomMessage(last)
			if last.Author.Kind == "agent" {
				summary.LastMessagePreview.AgentName = view.names.name(MemberIdentity{RoomIdentity: base.RoomIdentity, AgentID: last.Author.ID, MembershipID: last.AuthorMembershipID})
			}
			if len(last.Mentions) > 0 {
				last.Text = renderMentionText(last, func(at MessageMention) string {
					return view.names.name(MemberIdentity{RoomIdentity: base.RoomIdentity, AgentID: at.ID, MembershipID: at.MembershipID})
				})
				summary.LastMessagePreview.Text = summarizeRoomMessage(last).Text
			}
		}
		var saved managedRoomConfig
		err := readSnapshot(filepath.Join(dir, "room-config.json"), &saved, maxPrivateFileBytes)
		if err == nil && saved.RoomIdentity != base.RoomIdentity {
			err = ErrIdentity
		}
		if err != nil {
			summary.SummaryStatus, summary.SummaryError = "unavailable", err.Error()
		} else {
			for _, member := range saved.Members {
				summary.Members = append(summary.Members, teamMemberSummary{ID: member.AgentID, Name: member.Name, AgentName: view.names.name(MemberIdentity{RoomIdentity: base.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID})})
			}
		}
		rooms = append(rooms, summary)
	}
	response := roomsResponse{settingsView: m.settings.snapshot().settingsView, ServiceEpoch: m.epoch, Rooms: rooms,
		Operator: requestOperator(r), Busy: view.busy, Teams: view.teams, AgentRoles: builtinAgentRoleSummaries(), DefaultAssistant: view.assistant}
	if err := m.store.check(true); err == ErrCommitUnknown {
		response.WriteError = err.Error()
	}
	_ = json.NewEncoder(w).Encode(response)
}
