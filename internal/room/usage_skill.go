package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

const usageSkillHelp = `Usage: teamx skill usage
Read embedded TeamX interface, prerequisites and execution guidance.
Requires current bound Agent identity; no platform connection or tool binding.
This read-only resource changes no settings and grants no management authority.
`

type usageSkillRequest struct {
	ServiceInfo
	Caller Caller `json:"caller"`
}

func runUsageSkillCLI(ctx context.Context, args []string, stdout io.Writer, env func(string) string) error {
	if len(args) == 0 || len(args) == 1 && (args[0] == "--help" || args[0] == "-h") || len(args) == 2 && args[0] == "usage" && (args[1] == "--help" || args[1] == "-h") {
		return writeOutput(stdout, []byte(usageSkillHelp))
	}
	if len(args) != 1 || args[0] != "usage" {
		return ErrRequest
	}
	endpoint := strings.TrimRight(env("TEAMX_ENDPOINT"), "/")
	if endpoint == "" {
		return fmt.Errorf("usage skill requires bound Agent context")
	}
	client := cliClient{endpoint: endpoint, http: &http.Client{Timeout: 5 * time.Second}}
	identity, err := client.identity(ctx, "", env)
	if err != nil {
		return err
	}
	if identity.Caller.Kind != "agent" {
		return ErrIdentity
	}
	data, err := json.Marshal(usageSkillRequest{ServiceInfo: identity.ServiceInfo, Caller: identity.Caller})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/skills/usage", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return toolResponseError(response)
	}
	_, err = io.Copy(stdout, io.LimitReader(response.Body, 64<<10))
	return err
}

func (m *roomManager) serveUsageSkill(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var req usageSkillRequest
	if !decodeToolRequest(w, r, &req) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkToolMemberLocked(r.Context(), toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}); err != nil {
		writeAPIError(w, err)
		return
	}
	data, err := tool.ReadSkill(tool.Definition{Skill: "skills/usage/SKILL.md"})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write(data)
}
