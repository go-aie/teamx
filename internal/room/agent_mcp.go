package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Operator-only, optional HTTP MCP bindings. Credentials remain in this private
// startup file and ephemeral Launch values, never Room/model snapshots or UI.
// A binding includes the full persistent member identity; names alone cannot
// grant another Room, replacement membership or recreated Room access.
type agentMCPBinding struct {
	Servers     []dsh.MCPHTTPServer
	Environment *environmentConfig
}

type agentMCPBindings map[MemberIdentity]agentMCPBinding

func loadAgentMCPBindings(path string) (agentMCPBindings, error) {
	if path == "" {
		return nil, nil
	}
	fail := func() (agentMCPBindings, error) {
		return nil, errors.New("invalid agent_mcp_file; require a deployment-owned regular 0600 file with valid unique member HTTP MCP bindings")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fail()
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return fail()
	}
	f, err := os.Open(path)
	if err != nil {
		return fail()
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fail()
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return fail()
	}
	var cfg struct {
		Version  int `json:"version"`
		Bindings []struct {
			MemberIdentity
			Servers     []dsh.MCPHTTPServer `json:"servers"`
			Environment *environmentConfig  `json:"environment,omitempty"`
		} `json:"bindings"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil || dec.Decode(new(any)) != io.EOF || cfg.Version != 1 || len(cfg.Bindings) > maxMembers {
		return fail()
	}
	bindings := agentMCPBindings{}
	for _, binding := range cfg.Bindings {
		if validateMemberHeader(formatVersion, binding.MemberIdentity) != nil || len(binding.Servers) == 0 && binding.Environment == nil || len(binding.Servers) > 8 {
			return fail()
		}
		if _, exists := bindings[binding.MemberIdentity]; exists {
			return fail()
		}
		for _, server := range binding.Servers {
			if server.Name == "teamx" {
				return fail()
			}
		}
		opts := dsh.Options{BinaryPath: "/operator-mcp-validation", Model: launcherCheckModel(), MCPServers: binding.Servers}
		if dsh.CheckConfig(opts) != nil {
			return fail()
		}
		if binding.Environment != nil && !binding.Environment.valid() {
			return fail()
		}
		bindings[binding.MemberIdentity] = agentMCPBinding{Servers: binding.Servers, Environment: binding.Environment}
	}
	return bindings, nil
}

func (bindings agentMCPBindings) servers(id MemberIdentity) []dsh.MCPHTTPServer {
	var result []dsh.MCPHTTPServer
	for _, server := range bindings[id].Servers {
		server.Headers = append([]dsh.MCPHeader{}, server.Headers...)
		result = append(result, server)
	}
	return result
}
