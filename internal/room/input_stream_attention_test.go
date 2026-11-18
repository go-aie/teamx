package room

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
)

func TestInputAttentionMemberConflictIsolationAndLocalSave(t *testing.T) {
	m, svc, member, setup := memberToolFixture(t)
	svc.unifiedInputs = true
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	req := inputAttentionRequest{ServiceInfo: ServiceInfo{RoomIdentity: id.RoomIdentity, ServiceEpoch: m.epoch}, Caller: Caller{Author: Author{Kind: "human", ID: "user"}}, AgentID: id.AgentID, MembershipID: id.MembershipID}
	read := managerHTTP(m, "/v1/member/input-attention", req)
	if read.Code != http.StatusOK {
		t.Fatal(read.Code, read.Body.String())
	}
	var v struct {
		Available bool                 `json:"available"`
		Sources   []inputAttentionView `json:"sources"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &v); err != nil || !v.Available {
		t.Fatal(err)
	}
	var source inputAttentionView
	for _, s := range v.Sources {
		if s.PlatformID == setup.Input.PlatformID {
			source = s
		}
	}
	if source.PlatformID == "" {
		t.Fatal(v)
	}
	before, _ := svc.store.LoadAgent(id)
	req.PlatformID = source.PlatformID
	req.SourceVersion = source.SourceVersion
	req.Attention = "high"
	w := managerHTTP(m, "/v1/member/input-attention", req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	stale := managerHTTP(m, "/v1/member/input-attention", req)
	if stale.Code != http.StatusConflict {
		t.Fatal(stale.Code)
	}
	req.ExpectedVersion = 1
	req.SourceVersion = "stale-account-observation"
	if changed := managerHTTP(m, "/v1/member/input-attention", req); changed.Code != 409 {
		t.Fatal(changed.Code)
	}
	after, _ := svc.store.LoadAgent(id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("settings changed scheduling/turn")
	}
	// The account scope, not platform aliases or credentials, owns preferences.
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "gitlab", Instance: "https://gitlab.test"}, ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9"}}
	key := inputAccountScope(id, account)
	same := account
	if inputAccountScope(id, same) != key {
		t.Fatal("same native account changed key")
	}
	same.ID.Value = "10"
	if inputAccountScope(id, same) == key {
		t.Fatal("account switch reused source")
	}
	other := id
	other.MembershipID = "other-generation"
	if inputAccountScope(other, account) == key {
		t.Fatal("member source shared")
	}
}
func TestInputAttentionSnapshotsAndExposurePersistOnce(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	id := s.storage.id
	receivePlatform(t, s.storage, storedGitLab(id, "a", "events", "7", "42", "a"))
	// Begin intentionally froze no platform partition, so this input appears only
	// in the next legal turn; changing preference cannot expand its bound.
	h.store.mu.Lock()
	v, err := h.store.inputPreferencesLocked(id)
	if err != nil {
		h.store.mu.Unlock()
		t.Fatal(err)
	}
	v.Sources["test-source"] = inputAttention{Version: 1, Attention: "low"}
	err = h.store.saveInputPreferencesLocked(v)
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.catalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, exposure, err := c.Directory()
	if err != nil || directory.Pending != 1 {
		t.Fatal(directory, err)
	}
	if err := s.storage.saveInputExposure(s.execution, exposure); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	saved, _ := h.store.inputPreferencesLocked(id)
	h.store.mu.Unlock()
	if err := s.storage.saveInputExposure(s.execution, inputstream.Exposure{Turn: 1, LastSource: "forged-second-observation"}); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	again, _ := h.store.inputPreferencesLocked(id)
	h.store.mu.Unlock()
	if !reflect.DeepEqual(saved, again) {
		t.Fatal("same-turn retry advanced exposure")
	}
}
