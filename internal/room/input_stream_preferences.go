package room

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

type inputAttention struct {
	Version   uint64 `json:"version"`
	Attention string `json:"attention"`
}
type inputPreferences struct {
	Version     int                       `json:"version"`
	Owner       MemberIdentity            `json:"owner"`
	Sources     map[string]inputAttention `json:"sources"`
	Exposure    inputstream.Exposure      `json:"exposure"`
	LastExposed map[string]uint64         `json:"last_exposed"`
	Checksum    string                    `json:"checksum"`
}

func (s *Store) inputPreferencesLocked(id MemberIdentity) (inputPreferences, error) {
	v := inputPreferences{Version: 1, Owner: id, Sources: map[string]inputAttention{}, LastExposed: map[string]uint64{}}
	path, _, err := s.memberPath(id, "input-preferences.json", false)
	if err != nil {
		return v, err
	}
	err = readSnapshot(path, &v, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	sum := v.Checksum
	v.Checksum = ""
	b, _ := json.Marshal(v)
	if v.Version != 1 || v.Owner != id || v.Sources == nil || len(v.Sources) > inputstream.MaxRecords || sum != inputstream.Digest(b) {
		return v, ErrInvalid
	}
	v.Checksum = sum
	for key, a := range v.Sources {
		if key == "" || a.Version == 0 || a.Attention != "high" && a.Attention != "normal" && a.Attention != "low" {
			return v, ErrInvalid
		}
	}
	return v, nil
}
func (s *Store) saveInputPreferencesLocked(v inputPreferences) error {
	v.Checksum = ""
	b, _ := json.Marshal(v)
	v.Checksum = inputstream.Digest(b)
	return s.save(filepath.Join(s.dir, "agents", v.Owner.MembershipID, "input-preferences.json"), v, maxPrivateFileBytes)
}
func (s *Store) inputObservationLocked(id MemberIdentity) (inputstream.Preferences, error) {
	v, err := s.inputPreferencesLocked(id)
	if err != nil {
		return inputstream.Preferences{}, err
	}
	out := inputstream.Preferences{Attention: map[string]string{}, LastSource: v.Exposure.LastSource, LastExposed: v.LastExposed}
	for key, a := range v.Sources {
		out.Attention[key] = a.Attention
		out.Version += a.Version
	}
	return out, nil
}
func (p *platformInputStorage) saveInputExposure(e inputstream.Execution, exposure inputstream.Exposure) error {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return err
	}
	if exposure.Turn != e.Turn {
		return ErrIdentity
	}
	v, err := p.store.inputPreferencesLocked(p.id)
	if err != nil {
		return err
	}
	if v.Exposure.Turn == e.Turn {
		return nil
	}
	v.Exposure = exposure
	if v.LastExposed == nil {
		v.LastExposed = map[string]uint64{}
	}
	for _, source := range exposure.Sources {
		v.LastExposed[source] = e.Turn
	}
	return p.store.saveInputPreferencesLocked(v)
}
func inputAccountScope(id MemberIdentity, a platform.AccountIdentity) string {
	g := inputstream.Group{Instance: a.Instance, Tenant: notification.NativeID(a.Tenant), Account: notification.NativeID(a.ID)}
	source := g.SourceKey(a.Provider, "")
	return inputstream.Ref("scope_", inputstream.Member{RoomID: id.RoomID, RoomInstance: id.RoomInstance, MembershipID: id.MembershipID}, a.Provider, source, source)
}

type inputAttentionRequest struct {
	ServiceInfo
	Caller          Caller `json:"caller"`
	AgentID         string `json:"agent_id"`
	MembershipID    string `json:"membership_id"`
	PlatformID      string `json:"platform_id,omitempty"`
	SourceVersion   string `json:"source_version,omitempty"`
	Attention       string `json:"attention,omitempty"`
	ExpectedVersion uint64 `json:"expected_version"`
}
type inputAttentionView struct {
	PlatformID    string `json:"platform_id"`
	SourceVersion string `json:"source_version"`
	Attention     string `json:"attention"`
	Version       uint64 `json:"version"`
}

func (m *roomManager) supportsNotifications(provider string) bool {
	for id, a := range m.notificationAdapters {
		if id.Provider == provider && a.Source != nil {
			return true
		}
	}
	return false
}
func (m *roomManager) attentionSourceLocked(file registry.Snapshot, id MemberIdentity, c registry.Record) (string, string, error) {
	a, _, err := registry.ResolveAccessAccount(file, c)
	if err == nil {
		return inputAccountScope(id, a.Identity), inputstream.Ref("source_observation_", c.ID, a.Identity), nil
	}
	// Missing native account evidence stays isolated by connection; it is never
	// merged with all unknown accounts of a provider.
	key := inputstream.Ref("scope_", id, c.Provider, c.URL, c.ID)
	return key, inputstream.Ref("source_observation_", c.ID, c.Version, c.URL), nil
}
func (m *roomManager) serveInputAttention(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req inputAttentionRequest
	if !decodeToolRequest(w, r, &req) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	if svc == nil || req.RoomIdentity != svc.info.RoomIdentity || req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	if !svc.unifiedInputs {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": false, "sources": []inputAttentionView{}})
		return
	}
	file, err := m.platforms.Load()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	svc.store.mu.Lock()
	defer svc.store.mu.Unlock()
	prefs, err := svc.store.inputPreferencesLocked(id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if req.Attention != "" {
		if req.Attention != "high" && req.Attention != "normal" && req.Attention != "low" {
			writeAPIError(w, ErrRequest)
			return
		}
		found := false
		for _, c := range file.Platforms {
			if c.ID != req.PlatformID {
				continue
			}
			found = true
			if !m.supportsNotifications(c.Provider) {
				writeAPIError(w, ErrRequest)
				return
			}
			key, version, e := m.attentionSourceLocked(file, id, c)
			if e != nil {
				writeAPIError(w, e)
				return
			}
			old := prefs.Sources[key]
			if version != req.SourceVersion || old.Version != req.ExpectedVersion {
				writeAPIError(w, ErrConflict)
				return
			}
			prefs.Sources[key] = inputAttention{Version: old.Version + 1, Attention: req.Attention}
		}
		if !found {
			writeAPIError(w, ErrIdentity)
			return
		}
		if err := svc.store.saveInputPreferencesLocked(prefs); err != nil {
			writeAPIError(w, err)
			return
		}
	} else if req.PlatformID != "" || req.SourceVersion != "" || req.ExpectedVersion != 0 {
		writeAPIError(w, ErrRequest)
		return
	}
	views := []inputAttentionView{}
	for _, c := range file.Platforms {
		if !m.supportsNotifications(c.Provider) {
			continue
		}
		key, version, e := m.attentionSourceLocked(file, id, c)
		if e != nil {
			writeAPIError(w, e)
			return
		}
		v := prefs.Sources[key]
		if v.Attention == "" {
			v.Attention = "normal"
		}
		views = append(views, inputAttentionView{PlatformID: c.ID, SourceVersion: version, Version: v.Version, Attention: v.Attention})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"available": true, "sources": views})
}
