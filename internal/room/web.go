package room

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// The operator UI ships in the same binary and uses the existing Room API.
// Static/bootstrap routes expose no private output or backend configuration.
//
//go:embed web/index.html web/style.css web/app.js web/client.mjs web/markdown.mjs web/vendor/marked.mjs web/time.mjs web/vendor/temporal.mjs web/vendor/jsbi.mjs
var webFiles embed.FS

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

func (s *service) serveWeb(w http.ResponseWriter, r *http.Request) bool {
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
	file, contentType := "", ""
	switch r.URL.Path {
	case "/":
		file, contentType = "web/index.html", "text/html; charset=utf-8"
	case "/ui/style.css":
		file, contentType = "web/style.css", "text/css; charset=utf-8"
	case "/ui/app.js":
		file, contentType = "web/app.js", "text/javascript; charset=utf-8"
	case "/ui/client.mjs":
		file, contentType = "web/client.mjs", "text/javascript; charset=utf-8"
	case "/ui/markdown.mjs":
		file, contentType = "web/markdown.mjs", "text/javascript; charset=utf-8"
	case "/ui/time.mjs":
		file, contentType = "web/time.mjs", "text/javascript; charset=utf-8"
	case "/ui/vendor/temporal.mjs":
		file, contentType = "web/vendor/temporal.mjs", "text/javascript; charset=utf-8"
	case "/ui/vendor/jsbi.mjs":
		file, contentType = "web/vendor/jsbi.mjs", "text/javascript; charset=utf-8"
	case "/ui/vendor/marked.mjs":
		file, contentType = "web/vendor/marked.mjs", "text/javascript; charset=utf-8"
	default:
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "GET or HEAD required", http.StatusMethodNotAllowed)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	data, _ := webFiles.ReadFile(file)
	http.ServeContent(w, r, file, time.Time{}, bytes.NewReader(data))
	return true
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
