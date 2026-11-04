package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func readPrivateJSON(path string, out any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 32<<10 {
		return errors.New("invalid private GitLab configuration")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("private GitLab configuration unavailable")
	}
	return decodeJSON(data, out)
}
func ReadWorkspaceBinding(path string) (Binding, error) {
	var b Binding
	if !filepath.IsAbs(path) || readPrivateJSON(path, &b) != nil || !validEventBinding(b) {
		return b, errors.New("invalid GitLab workspace binding")
	}
	return b, nil
}

// WorkspaceCredential resolves only the bound credential. Shared connections
// use the existing private loopback exchange; no platform API or agent tool.
func WorkspaceCredential(ctx context.Context, b Binding, env func(string) string, transport http.RoundTripper) (platform.Secret, error) {
	fail := func() (platform.Secret, error) { return platform.Secret{}, platform.ErrCredential }
	if !validEventBinding(b) {
		return fail()
	}
	token := ""
	if b.ConnectionID != "" {
		token = env("TEAMX_PLATFORM_TOKEN")
		if token == "" {
			endpoint := env("TEAMX_ENDPOINT")
			u, err := url.Parse(endpoint)
			if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
				return fail()
			}
			if env("TEAMX_ROOM_ID") != b.RoomID || env("TEAMX_ROOM_INSTANCE") != b.RoomInstance || env("TEAMX_SERVICE_EPOCH") == "" || env("TEAMX_AGENT_ID") == "" || env("TEAMX_MEMBERSHIP_ID") == "" {
				return fail()
			}
			payload, _ := json.Marshal(map[string]any{"room_id": b.RoomID, "room_instance": b.RoomInstance, "service_epoch": env("TEAMX_SERVICE_EPOCH"), "caller": map[string]string{"kind": "agent", "id": env("TEAMX_AGENT_ID"), "membership_id": env("TEAMX_MEMBERSHIP_ID")}})
			ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v1/workspace/tool-credential", bytes.NewReader(payload))
			if err != nil {
				return fail()
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-TeamX-Tool", "credential")
			if transport == nil {
				transport = &http.Transport{Proxy: nil}
			}
			client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Do(req)
			if err != nil {
				return fail()
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fail()
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, (32<<10)+1))
			if err != nil || len(data) > 32<<10 {
				return fail()
			}
			var result struct {
				Token        string `json:"token"`
				URL          string `json:"url"`
				Project      string `json:"project"`
				ProjectID    int64  `json:"project_id"`
				ConnectionID string `json:"connection_id"`
				Generation   string `json:"connection_generation"`
			}
			if decodeJSON(data, &result) != nil || result.URL != b.URL || result.Project != b.Project || result.ProjectID != b.ProjectID || result.ConnectionID != b.ConnectionID || result.Generation != b.ConnectionGeneration {
				return fail()
			}
			token = result.Token
		}
	} else if b.TokenEnv != "" {
		token = env(b.TokenEnv)
	} else {
		parent, err := os.Lstat(filepath.Dir(b.CredentialFile))
		if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 || parent.Mode()&os.ModeSymlink != 0 {
			return fail()
		}
		var r struct {
			Version  int    `json:"version"`
			RoomID   string `json:"room_id"`
			Instance string `json:"room_instance"`
			ID       string `json:"credential_id"`
			PAT      string `json:"pat"`
		}
		if readPrivateJSON(b.CredentialFile, &r) != nil || r.Version != 1 || r.RoomID != b.RoomID || r.Instance != b.RoomInstance || r.ID != b.CredentialID {
			return fail()
		}
		token = r.PAT
	}
	if !validToken(token) {
		return fail()
	}
	return platform.NewSecret(token), nil
}

var gitPrompt = regexp.MustCompile(`^(Username|Password) for '([^']+)': ?$`)

// Askpass validates the exact repository before resolving any credential.
func Askpass(ctx context.Context, b Binding, prompt string, env func(string) string, transport http.RoundTripper) (string, error) {
	m := gitPrompt.FindStringSubmatch(prompt)
	if m == nil {
		return "", errors.New("unrecognized Git credential target")
	}
	target, err := url.Parse(m[2])
	origin, _ := url.Parse(b.URL)
	if err != nil || !validEventBinding(b) || target.Scheme != "https" || !strings.EqualFold(target.Host, origin.Host) || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || (target.Path != "/"+b.Project && target.Path != "/"+b.Project+".git") {
		return "", errors.New("Git credential target differs from Room project")
	}
	if target.User != nil {
		_, hasPassword := target.User.Password()
		if target.User.Username() != "oauth2" || hasPassword {
			return "", errors.New("invalid Git credential username")
		}
	}
	if m[1] == "Username" {
		return "oauth2", nil
	}
	secret, err := WorkspaceCredential(ctx, b, env, transport)
	if err != nil {
		return "", err
	}
	return secret.Reveal(), nil
}
