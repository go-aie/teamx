package room

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

func cliTokenFile(args []string, env func(string) string) ([]string, func(string) string, error) {
	var out []string
	file := env("TEAMX_ADMIN_TOKEN_FILE")
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			out = append(out, args[i:]...)
			break
		}
		if args[i] == "--token-file" {
			i++
			if i == len(args) || args[i] == "" {
				return nil, env, ErrRequest
			}
			file = args[i]
			continue
		}
		if strings.HasPrefix(args[i], "--token-file=") {
			file = strings.TrimPrefix(args[i], "--token-file=")
			if file == "" {
				return nil, env, ErrRequest
			}
			continue
		}
		out = append(out, args[i])
	}
	if env("TEAMX_AGENT_ID") != "" && file != "" {
		return nil, env, fmt.Errorf("Agent tools cannot use admin credentials")
	}
	original := env
	return out, func(key string) string {
		if key == "TEAMX_ADMIN_TOKEN_FILE" {
			return file
		}
		return original(key)
	}, nil
}
func (c *cliClient) loadCredential(env func(string) string) error {
	if env("TEAMX_AGENT_ID") != "" || env("TEAMX_MEMBERSHIP_ID") != "" {
		c.token = env("TEAMX_EXECUTION_TOKEN")
		if c.token == "" {
			c.token = env("TEAMX_EXECUTION_AUTH")
		}
		if c.token != "" && !validAccessToken(c.token) {
			return ErrIdentity
		}
		if c.token != "" && isExecutionAlias(c.endpoint) {
			rawProxy := env("DSH_TEAMX_EXECUTION_PROXY")
			if rawProxy == "" {
				rawProxy = env("HTTP_PROXY")
			}
			if rawProxy == "" {
				return fmt.Errorf("execution callback proxy is unavailable")
			}
			proxy, err := url.Parse(rawProxy)
			if err != nil || proxy.Scheme != "http" || proxy.Port() == "" || (proxy.Path != "" && proxy.Path != "/") || proxy.RawQuery != "" || proxy.Fragment != "" {
				return fmt.Errorf("execution callback requires its local HTTP proxy")
			}
			ip := net.ParseIP(proxy.Hostname())
			if proxy.Hostname() == "localhost" {
				// SRT spells its namespace-local listener as localhost. Pin it
				// directly, rather than delegating its meaning to host DNS.
				ip = net.ParseIP("127.0.0.1")
				proxy.Host = net.JoinHostPort(ip.String(), proxy.Port())
			}
			if ip == nil || !ip.IsLoopback() {
				return fmt.Errorf("execution callback proxy must be loopback")
			}
			c.executionProxy = proxy
		}
		return nil
	}
	if file := env("TEAMX_ADMIN_TOKEN_FILE"); file != "" {
		token, err := readAdminToken(file)
		if err != nil {
			return err
		}
		c.token = token
	}
	return nil
}

func privateRoomAccessEnv(name string) bool {
	switch name {
	case "TEAMX_ADMIN_TOKEN", "TEAMX_ADMIN_TOKEN_FILE", "TEAMX_EXECUTION_TOKEN", "TEAMX_EXECUTION_AUTH", "TEAMX_SESSION", "TEAMX_SESSION_COOKIE":
		return true
	}
	return false
}

// Never forward access credentials through redirects or cleartext remote URLs.
func (c cliClient) do(request *http.Request) (*http.Response, error) {
	if c.token == "" {
		if c.anonymousHuman {
			origin, err := url.Parse(c.endpoint)
			if err != nil {
				return nil, err
			}
			request.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
		}
		return c.http.Do(request)
	}
	if err := validateAccessEndpointFor(c.endpoint, c.executionProxy != nil); err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	client := *c.http
	if c.executionProxy != nil {
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		original, ok := transport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("execution callback requires the standard HTTP transport")
		}
		selected := original.Clone()
		selected.Proxy = http.ProxyURL(c.executionProxy) // never resolve/dial the alias directly
		defer selected.CloseIdleConnections()
		client.Transport = selected
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(request)
}
func validateAccessEndpoint(endpoint string) error {
	return validateAccessEndpointFor(endpoint, false)
}

var executionAliasHost = regexp.MustCompile(`^tx-[0-9a-f]{40}\.execution\.invalid$`)

func isExecutionAlias(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "http" && u.Port() != "" && executionAliasHost.MatchString(u.Hostname())
}

func validateAccessEndpointFor(endpoint string, execution bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Host == "" || (u.Path != "" && (u.Path != spaceRoute(strings.TrimPrefix(u.Path, "/s/")) || !validSpaceID(strings.TrimPrefix(u.Path, "/s/")))) || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.Contains(endpoint, "#") {
		return fmt.Errorf("access endpoint must be an HTTP(S) origin with an optional /s/<space-id> route")
	}
	if u.Scheme == "https" {
		return nil
	}
	if execution && isExecutionAlias(endpoint) {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("access credentials require HTTPS or a loopback HTTP endpoint")
}
