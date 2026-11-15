package dsh

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// MCPHTTPServer is the standard ACP HTTP MCP declaration, independent of tools.
type MCPHTTPServer struct {
	Type    string      `json:"type"`
	Name    string      `json:"name"`
	URL     string      `json:"url"`
	Headers []MCPHeader `json:"headers"`
}

type MCPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

var mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func checkMCP(servers []MCPHTTPServer) error {
	names := map[string]bool{}
	for _, server := range servers {
		u, err := url.Parse(server.URL)
		if server.Type != "http" || !mcpName.MatchString(server.Name) || names[server.Name] || err != nil ||
			(u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return errors.New("invalid ACP MCP HTTP configuration")
		}
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsUnspecified() {
			return errors.New("ACP MCP HTTP endpoint must be reachable, not an unspecified listen address")
		}
		names[server.Name] = true
		headers := map[string]bool{}
		for _, h := range server.Headers {
			name := http.CanonicalHeaderKey(h.Name)
			if name == "" || headers[name] || strings.ContainsAny(h.Value, "\r\n\x00") {
				return errors.New("invalid ACP MCP HTTP headers")
			}
			for _, c := range h.Value {
				if c == 127 || c < 32 && c != '\t' {
					return errors.New("invalid ACP MCP HTTP headers")
				}
			}
			for _, c := range h.Name {
				if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", c) {
					return errors.New("invalid ACP MCP HTTP headers")
				}
			}
			headers[name] = true
		}
	}
	return nil
}

func cloneMCP(servers []MCPHTTPServer) []MCPHTTPServer {
	servers = slices.Clone(servers)
	for i := range servers {
		servers[i].Headers = append([]MCPHeader{}, servers[i].Headers...)
	}
	return servers
}

// The endpoint, namespace and header names affect session compatibility;
// temporary credential values are deliberately excluded.
func mcpIdentity(servers []MCPHTTPServer) []MCPHTTPServer {
	servers = cloneMCP(servers)
	for i := range servers {
		for j := range servers[i].Headers {
			servers[i].Headers[j].Name = http.CanonicalHeaderKey(servers[i].Headers[j].Name)
			servers[i].Headers[j].Value = ""
		}
	}
	return servers
}
