// Package opencsg implements the SSO and built-in Gateway key exchanges used
// by CSGClaw. The host owns credential persistence and local users.
package opencsg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

var ErrAuthentication = errors.New("OpenCSG authentication failed")

type SSO struct {
	login, hub string
	client     http.Client
}

var _ authn.Provider = (*SSO)(nil)

// New pins the two deployment-controlled origins and forbids credential-bearing
// redirects. Transport is injectable for tests; URLs always require HTTPS.
func New(loginOrigin, hubOrigin string, transport http.RoundTripper) (*SSO, error) {
	for _, raw := range []string{loginOrigin, hubOrigin} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return nil, errors.New("OpenCSG requires fixed HTTPS root origins")
		}
	}
	return &SSO{login: loginOrigin, hub: hubOrigin, client: http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (s *SSO) LoginURL(callback string) (string, error) {
	u, err := url.Parse(callback)
	if err != nil || u.Scheme != "https" && !authn.LoopbackHTTP(u) || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("OpenCSG callback requires HTTPS or literal HTTP loopback")
	}
	return s.login + "/sso/login?" + url.Values{"redirect_url": {callback}}.Encode(), nil
}

func (s *SSO) Complete(ctx context.Context, values url.Values) (authn.Identity, error) {
	token := values.Get("jwt_token")
	if len(values["jwt_token"]) > 1 || len(values["jwt"]) > 1 || token != "" && values.Get("jwt") != "" {
		return authn.Identity{}, ErrAuthentication
	}
	if token == "" {
		token = values.Get("jwt")
	}
	// This payload is an untrusted routing hint only. Identity comes exclusively
	// from the authenticated, owner-checked CSGHub response below.
	parts := strings.Split(token, ".")
	if len(token) > 16<<10 || len(parts) != 3 {
		return authn.Identity{}, ErrAuthentication
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	var hint struct {
		User    string `json:"current_user"`
		Expires int64  `json:"exp"`
	}
	if err != nil || json.Unmarshal(data, &hint) != nil || hint.User == "" || len(hint.User) > 256 || strings.ContainsAny(hint.User, "/\\?#\r\n") || hint.User == "." || hint.User == ".." {
		return authn.Identity{}, ErrAuthentication
	}
	// An untrusted expiry may only shorten a local session. Acceptance still
	// requires the authenticated owner-checked exchange below.
	if hint.Expires <= time.Now().Unix() {
		return authn.Identity{}, ErrAuthentication
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.hub+"/api/v1/user/"+url.PathEscape(hint.User)+"/tokens?app=git", nil)
	if err != nil {
		return authn.Identity{}, ErrAuthentication
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	// Do not return transport errors: url.Error contains request URLs and a
	// custom transport may include the credential in its error text.
	if err != nil {
		return authn.Identity{}, ErrAuthentication
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return authn.Identity{}, ErrAuthentication
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return authn.Identity{}, ErrAuthentication
	}
	var result struct {
		Data []struct {
			Username string `json:"user_name"`
			UUID     string `json:"user_uuid"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &result) != nil || len(result.Data) == 0 {
		return authn.Identity{}, ErrAuthentication
	}
	var subject string
	for _, item := range result.Data {
		if item.Username != hint.User || item.UUID == "" || len(item.UUID) > 256 || strings.TrimSpace(item.UUID) != item.UUID || strings.ContainsAny(item.UUID, "\x00\r\n") || subject != "" && subject != item.UUID {
			return authn.Identity{}, ErrAuthentication
		}
		subject = item.UUID
	}
	return authn.Identity{Provider: "opencsg", Authority: s.hub, Subject: subject, DisplayName: hint.User, ValidUntil: time.Unix(hint.Expires, 0)}, nil
}
