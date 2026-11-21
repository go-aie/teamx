package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
)

type sharedInboxRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	RoomIdentity
	Caller  Caller           `json:"caller"`
	Version *uint64          `json:"version"`
	Members []MemberIdentity `json:"members"`
}
type sharedGrantView struct {
	GrantID      string `json:"grant_id"`
	MembershipID string `json:"membership_id"`
	Status       string `json:"status"`
	Start        uint64 `json:"start"`
}
type sharedInboxView struct {
	ReclamationReason string            `json:"reclamation_reason,omitempty"`
	SourceRelease     string            `json:"source_release,omitempty"`
	BlockedReasons    map[string]string `json:"blocked_reasons"`
	CapacityFull      bool              `json:"capacity_full"`
	InboxID           string            `json:"inbox_id"`
	Version           uint64            `json:"version"`
	Grants            []sharedGrantView `json:"grants"`
	Records           int               `json:"records"`
	Bytes             uint64            `json:"bytes"`
	FirstProtected    uint64            `json:"first_protected,omitempty"`
	Blocked           []string          `json:"blocked_members"`
	Protections       int               `json:"protections"`
	Entries           []inboxEntryView  `json:"entries"`
}

func (s *service) sharedInboxView() (sharedInboxView, error) {
	out := sharedInboxView{InboxID: sharedProjectInbox, Grants: []sharedGrantView{}, Blocked: []string{}, Entries: []inboxEntryView{}, BlockedReasons: map[string]string{}}
	p, err := s.store.sharedInputs(false)
	if err != nil || p == nil {
		return out, err
	}
	v := p.view.Load()
	out.Version = v.root.ConfigVersion
	out.Records = len(v.records)
	usage, err := p.repository.InspectUsage()
	if err != nil {
		return out, err
	}
	out.Bytes = usage.Bytes
	out.CapacityFull = usage.Records >= inputstream.MaxRecords || usage.Bytes >= inputstream.MaxBytes
	for _, g := range v.root.Grants {
		out.Grants = append(out.Grants, sharedGrantView{GrantID: g.ID, MembershipID: g.Member.MembershipID, Status: g.Status, Start: g.Start})
	}
	sort.Slice(out.Grants, func(i, j int) bool { return out.Grants[i].GrantID < out.Grants[j].GrantID })
	for _, page := range v.root.Pages {
		if page.State == "protected" {
			out.Protections++
		}
	}
	seqs := make([]uint64, 0, len(v.records))
	for seq := range v.records {
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	if len(seqs) > 0 {
		first := seqs[0]
		out.FirstProtected = first
		rec := v.records[first]
		for _, gid := range rec.Recipients {
			g := v.root.Grants[gid]
			if g.Status == "closed" {
				continue
			}
			c, err := inputstream.LoadSharedConsumption(s.store.dir, g.Member, v.root.Inbox, g.ID)
			if err != nil || c.Through[v.streams[first]] < first && c.Skipped[first] == "" {
				out.Blocked = append(out.Blocked, g.Member.MembershipID)
				reason := "unread"
				if err != nil {
					reason = "evidence_unavailable"
				}
				if g.Status == "closing" {
					reason = "grant_closing"
				}
				out.BlockedReasons[g.Member.MembershipID] = reason
			}
		}

		out.SourceRelease = "unknown"
		if status := p.maintenanceStatus.Load(); status != nil && status.Partition == v.snapshot.Partition && status.First == first {
			out.SourceRelease = status.Source
		}
		switch {
		case time.Now().Before(rec.CreatedAt.Add(time.Duration(s.inboxExpiry.Resolved().AfterDays) * 24 * time.Hour)):
			out.ReclamationReason = "retention"
		case out.SourceRelease != "released":
			out.ReclamationReason = "source_" + out.SourceRelease
		case len(out.Blocked) > 0:
			out.ReclamationReason = "member_held"
		case out.Protections > 0:
			out.ReclamationReason = "page_confirmation"
		}
	}
	// Operator preview does not create member source associations or consumption.
	for _, seq := range seqs[max(0, len(seqs)-25):] {
		rec := v.records[seq]
		item, err := s.store.notificationCapabilities("gitlab").Input.DecodeStoredInput(rec)
		if err != nil {
			return out, err
		}
		out.Entries = append(out.Entries, s.store.projectInboxEntry(InboxEntry{Version: formatVersion, Seq: seq, Location: &rec.Location, Item: item, ReceivedAt: rec.ReceivedAt}, sourceSummary(followupEvent{})))
	}
	return out, nil
}
func (s *service) configureSharedMembers(ctx context.Context, version uint64, members []MemberIdentity) (sharedInboxView, error) {
	if !s.unifiedInputs || s.isDirect || len(members) > maxMembers {
		return sharedInboxView{}, ErrRequest
	}
	if !s.management.TryLock() {
		return sharedInboxView{}, ErrBusy
	}
	defer s.management.Unlock()
	list, err := s.room.Call[MemberList](ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return sharedInboxView{}, err
	}
	desired := map[string]MemberIdentity{}
	for _, id := range members {
		if id.RoomIdentity != s.info.RoomIdentity || !memberExists(memberStates(list), id.AgentID, id.MembershipID) {
			return sharedInboxView{}, ErrIdentity
		}
		if _, ok := desired[id.MembershipID]; ok {
			return sharedInboxView{}, ErrRequest
		}
		desired[id.MembershipID] = id
	}
	p, err := s.store.sharedInputs(true)
	if err != nil {
		return sharedInboxView{}, err
	}
	p.service.Store(s)
	current := p.view.Load()
	expected := current.root.ConfigVersion
	if version == 0 && expected == 1 && len(current.root.Grants) == 0 {
		expected = 0
	}
	if expected != version {
		return sharedInboxView{}, ErrConflict
	}
	for _, g := range current.root.Grants {
		if g.Status == "closed" {
			continue
		}
		if _, ok := desired[g.Member.MembershipID]; !ok {
			var id MemberIdentity
			for _, m := range list.Members {
				if m.MembershipID == g.Member.MembershipID {
					id = MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
				}
			}
			if id.MembershipID == "" {
				return sharedInboxView{}, ErrIdentity
			}
			if err := s.closeSharedMember(ctx, id); err != nil {
				return sharedInboxView{}, err
			}
		}
	}
	for _, id := range desired {
		g, err := sharedOwnerValue(ctx, p, func() (inputstream.SharedGrant, error) { return p.repository.PrepareSharedGrant(inputMember(id)) })
		if err != nil {
			return sharedInboxView{}, err
		}
		member, err := s.inputStorage(id)
		if err != nil {
			return sharedInboxView{}, err
		}
		if err := member.run(ctx, "shared_join", func(context.Context) error {
			// Rejoining a fully closed Grant starts a new ledger at the new Grant's
			// start. Old ledger files cannot authorize the new generation.
			existing, loadErr := inputstream.LoadSharedConsumption(s.store.dir, inputMember(id), p.view.Load().root.Inbox, g.ID)
			if loadErr == nil {
				_ = existing
				return nil
			}
			if g.Status != "joining" {
				return loadErr
			}
			c := inputstream.NewSharedConsumption(p.view.Load().root.Inbox, g)
			return inputstream.SaveSharedConsumption(s.store.dir, c, inputPersistence{s.store})
		}); err != nil {
			return sharedInboxView{}, err
		}

		if _, err := sharedOwnerValue(ctx, p, func() (struct{}, error) { return struct{}{}, p.repository.ActivateSharedGrant(g.ID) }); err != nil {
			return sharedInboxView{}, err
		}
	}
	s.sharedInputsChanged(p)
	return s.sharedInboxView()
}
func memberStates(list MemberList) []MemberState {
	out := []MemberState{}
	for _, m := range list.Members {
		out = append(out, m.MemberState)
	}
	return out
}
func (m *roomManager) serveSharedInbox(w http.ResponseWriter, r *http.Request) {
	var owner RoomIdentity
	var caller string
	var epoch string
	var req sharedInboxRequest
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		owner = RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
		caller = q.Get("caller_id")
		epoch = q.Get("service_epoch")
	} else if r.Method == http.MethodPost {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Members == nil || req.Caller.Kind != "human" || req.Caller.MembershipID != "" || req.Version == nil {
			writeAPIError(w, ErrRequest)
			return
		}
		owner = req.RoomIdentity
		caller = req.Caller.ID
		epoch = req.ServiceEpoch
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if epoch != m.epoch || !validText(caller) {
		writeAPIError(w, ErrIdentity)
		return
	}
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(owner)
	available := !m.closing && !m.roomBusyLocked(owner) && svc != nil && svc.changes.TryRLock()
	m.mu.Unlock()
	if !available {
		writeAPIError(w, ErrBusy)
		return
	}
	defer svc.changes.RUnlock()
	if svc.closing.Load() || svc.restarting.Load() {
		writeAPIError(w, ErrClosing)
		return
	}
	var out sharedInboxView
	var err error
	if r.Method == http.MethodGet {
		out, err = svc.sharedInboxView()
	} else {
		out, err = svc.configureSharedMembers(r.Context(), *req.Version, req.Members)
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
