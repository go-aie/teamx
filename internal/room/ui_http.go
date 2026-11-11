package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

type memberActivity struct {
	ServiceInfo
	Member        serviceMember  `json:"member"`
	LastOutput    *dshDiagnostic `json:"last_output,omitempty"`
	CurrentOutput *dshDiagnostic `json:"current_output,omitempty"`
	UpdatedAt     *time.Time     `json:"updated_at,omitempty"`
	OutputError   string         `json:"output_error,omitempty"`
}

// Called only after the HTTP operator check. The live membership and Store
// identity check prevent old or guessed generations from reading orphan files.
func (s *service) memberActivity(ctx context.Context, req apiRequest) (memberActivity, error) {
	list, err := s.members(ctx)
	if err != nil {
		return memberActivity{}, err
	}
	var result memberActivity
	found := false
	for _, member := range list.Members {
		if member.AgentID == req.AgentID && member.MembershipID == req.MembershipID {
			result = memberActivity{ServiceInfo: s.info, Member: member}
			found = true
			break
		}
	}
	if !found {
		return memberActivity{}, ErrIdentity
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	path, err := s.store.dshPath(id, "dsh-turn.json", false)
	if err != nil {
		return memberActivity{}, err
	}
	if live, ok := s.store.activeDSH.Load(id); ok {
		result.CurrentOutput = &dshDiagnostic{}
		if reader, ok := live.(*dshSession).component.(interface{ Activity() (dsh.TurnResult, bool) }); ok {
			if output, active := reader.Activity(); active {
				result.CurrentOutput = &dshDiagnostic{Result: output}
			}
		}
		return result, nil
	}
	if result.Member.Execution != nil && result.Member.Execution.Active {
		// Opening a backend can take time before a component is available.
		// Do not mislabel the prior completed turn as the current activity.
		result.CurrentOutput = &dshDiagnostic{}
		return result, nil
	}
	var output dshDiagnostic
	if err := readSnapshot(path, &output, maxPrivateFileBytes); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			result.OutputError = "私有输出暂不可读：" + err.Error()
		}
		return result, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		result.OutputError = "私有输出时间不可读：" + err.Error()
		return result, nil
	}
	updated := info.ModTime().UTC()
	result.LastOutput, result.UpdatedAt = &output, &updated
	return result, nil
}

func humanOperator() string {
	operator := os.Getenv("USER")
	if !validText(operator) {
		return "operator"
	}
	return operator
}

func (s *service) serveUI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/v1/ui" && r.Method == http.MethodGet {
		operator := humanOperator()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			ServiceInfo
			settingsView
			Operator string   `json:"operator"`
			Profiles []string `json:"profiles"`
			IsDirect bool     `json:"is_direct"`
		}{s.info, s.settings.snapshot().settingsView, operator, s.uiProfiles, s.isDirect})
		return true
	}
	return false
}

// Browser requests stay on this local service. CLI requests have no Origin.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "http" && u.Host == r.Host && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
