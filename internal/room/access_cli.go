package room

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
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
			request.Header.Set("Origin", c.endpoint)
		}
		return c.http.Do(request)
	}
	if err := validateAccessEndpoint(c.endpoint); err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(request)
}
func validateAccessEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.Contains(endpoint, "#") {
		return fmt.Errorf("access endpoint must be a root HTTP(S) origin")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("access credentials require HTTPS or a loopback HTTP endpoint")
}
