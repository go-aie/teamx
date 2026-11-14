package room

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Display names are catalog metadata, never part of runtime identity. The
// immutable published view lets active services observe renames without taking
// the manager lock or changing their Sessions. A direct conversation's Name is
// the sole authority for its Agent name; group names are keyed by membership.
type agentDisplayName struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
}
type roomAgentNames struct {
	DirectID, DirectName string
	Members              map[string]agentDisplayName
}
type agentNameRegistry struct {
	value atomic.Pointer[map[RoomIdentity]roomAgentNames]
}

func (n *agentNameRegistry) name(id MemberIdentity) string {
	if n != nil {
		if view := n.value.Load(); view != nil {
			room := (*view)[id.RoomIdentity]
			if room.DirectID == id.AgentID && room.DirectName != "" {
				return room.DirectName
			}
			if member, ok := room.Members[id.MembershipID]; ok && member.AgentID == id.AgentID {
				return member.AgentName
			}
		}
	}
	return id.AgentID
}
func (m *roomManager) publishAgentNames() {
	if m.agentNames == nil {
		m.agentNames = &agentNameRegistry{}
	}
	view := make(map[RoomIdentity]roomAgentNames, len(m.index.Rooms))
	for _, entry := range m.index.Rooms {
		item := roomAgentNames{Members: maps.Clone(entry.AgentNames)}
		if entry.IsDirect {
			item.DirectID, item.DirectName = entry.AgentRoleID, entry.Name
		}
		view[entry.RoomIdentity] = item
	}
	m.agentNames.value.Store(&view)
}
func (s *service) agentName(agent, membership string) string {
	return s.agentNames.name(MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: agent, MembershipID: membership})
}

// The expected display name makes unknown responses safe to reconcile, and the
// exact membership prevents a stale editor from renaming a replacement Agent.
func (m *roomManager) serveAgentName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ServiceEpoch string `json:"service_epoch"`
		Caller       Caller `json:"caller"`
		MemberIdentity
		AgentName    string `json:"agent_name"`
		ExpectedName string `json:"expected_name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if !m.mu.TryLock() {
		writeAPIError(w, ErrBusy)
		return
	}
	defer m.mu.Unlock()
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	if m.closing {
		writeAPIError(w, ErrClosing)
		return
	}
	name := strings.TrimSpace(req.AgentName)
	if !validAgentName(name) || !validAgentName(req.ExpectedName) {
		writeAPIError(w, ErrRequest)
		return
	}
	next := m.copyIndex()
	for i := range next.Rooms {
		e := &next.Rooms[i]
		if e.RoomIdentity != req.RoomIdentity {
			continue
		}
		if e.Status != "ready" || m.roomBusyLocked(e.RoomIdentity) {
			writeAPIError(w, ErrBusy)
			return
		}
		var config managedRoomConfig
		if err := readSnapshot(filepath.Join(m.roomDir(e.RoomID), "room-config.json"), &config, maxPrivateFileBytes); err != nil {
			writeAPIError(w, err)
			return
		}
		found := false
		for _, member := range config.Members {
			if member.AgentID == req.AgentID && member.MembershipID == req.MembershipID {
				found = true
				break
			}
		}
		if !found || config.RoomIdentity != req.RoomIdentity {
			writeAPIError(w, ErrIdentity)
			return
		}
		current := m.agentNames.name(req.MemberIdentity)
		if current != req.ExpectedName {
			writeAPIError(w, ErrConflict)
			return
		}
		if current != name {
			if e.IsDirect {
				e.Name = name
			} else {
				if e.AgentNames == nil {
					e.AgentNames = map[string]agentDisplayName{}
				}
				e.AgentNames[req.MembershipID] = agentDisplayName{AgentID: req.AgentID, AgentName: name}
			}
			e.UpdatedAt = time.Now().UTC()
			if err := m.saveIndex(next); err != nil {
				writeAPIError(w, err)
				return
			}
		}
		json.NewEncoder(w).Encode(struct {
			AgentName string `json:"agent_name"`
		}{name})
		return
	}
	writeAPIError(w, ErrIdentity)
}
func validAgentName(name string) bool {
	return validText(name) && strings.TrimSpace(name) == name && len(name) <= 256 && !strings.ContainsAny(name, "\r\n\t")
}
