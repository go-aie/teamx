package room

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"time"
)

type memberActivity struct {
	ServiceInfo
	Member        serviceMember      `json:"member"`
	LastOutput    *backendDiagnostic `json:"last_output,omitempty"`
	CurrentOutput *backendDiagnostic `json:"current_output,omitempty"`
	UpdatedAt     *time.Time         `json:"updated_at,omitempty"`
	OutputError   string             `json:"output_error,omitempty"`
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
	active := result.Member.Execution != nil && result.Member.Execution.Active
	output, err := storedSessionBackend(s.store).activity(id, active)
	if err != nil {
		return memberActivity{}, err
	}
	result.LastOutput, result.CurrentOutput = output.last, output.current
	result.UpdatedAt, result.OutputError = output.updated, output.error
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
		operator := requestOperator(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			ServiceInfo
			settingsView
			SpaceID             string        `json:"space_id,omitempty"`
			InstallationID      string        `json:"installation_id,omitempty"`
			Operator            string        `json:"operator"`
			OperatorDisplayName string        `json:"operator_display_name"`
			Profiles            []string      `json:"profiles"`
			IsDirect            bool          `json:"is_direct"`
			Storage             *storagePaths `json:"storage,omitempty"`
		}{s.info, s.settings.snapshot().settingsView, requestSpaceID(r), requestInstallationID(r), operator, humanOperator(), s.uiProfiles, s.isDirect, s.settings.storagePaths(s.store.dir)})
		return true
	}
	return false
}

// Browser requests stay on this local service. CLI requests have no Origin.
func sameOrigin(r *http.Request) bool {
	if a, ok := r.Context().Value(accessContextKey{}).(*serverAccess); ok {
		origin, valid := a.requestOrigin(r)
		return valid && (r.Header.Get("Origin") == "" || r.Header.Get("Origin") == origin)
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "http" && u.Host == r.Host && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
