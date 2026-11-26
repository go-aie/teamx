package opencsg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

var ErrGatewayCredentials = errors.New("OpenCSG AI Gateway credential unavailable")

// OAuthGateway reads only the authenticated user's personal namespace key.
// CSGHub's OAuth middleware maps the IAM token to the local Hub user and checks
// namespace permission. No deployment credential or caller-supplied namespace
// is accepted. Construction is offline, including deployment seed validation.
type OAuthGateway struct {
	hub    string
	client http.Client
}

func NewOAuthGateway(hub string, transport http.RoundTripper) (*OAuthGateway, error) {
	hub = strings.TrimRight(hub, "/")
	u, err := url.Parse(hub)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrGatewayCredentials
	}
	return &OAuthGateway{hub: hub, client: http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (g *OAuthGateway) GatewayAPIKey(ctx context.Context, identity authn.Identity, accessToken string) (string, error) {
	if identity.Provider != "opencsg" || identity.Authority == "" || identity.Subject == "" || len(identity.Subject) > 256 || strings.TrimSpace(identity.Subject) != identity.Subject || strings.ContainsAny(identity.Subject, "/\\?#\x00\r\n") || accessToken == "" || len(accessToken) > 64<<10 || strings.ContainsAny(accessToken, "\x00\r\n") {
		return "", ErrGatewayCredentials
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.hub+"/api/v1/namespaces/"+url.PathEscape(identity.Subject)+"/apikeys/builtin", nil)
	if err != nil {
		return "", ErrGatewayCredentials
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "OAuth "+accessToken)
	resp, err := g.client.Do(req)
	if err != nil {
		return "", ErrGatewayCredentials
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", ErrGatewayCredentials
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	var result struct {
		Data struct {
			Token           string `json:"token"`
			Namespace       string `json:"ns_uuid"`
			Application     string `json:"application"`
			OrganizationKey bool   `json:"organization_key"`
			Active          bool   `json:"is_active"`
		} `json:"data"`
	}
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, &result) != nil || result.Data.Namespace != identity.Subject || result.Data.Application != "aigateway" || result.Data.OrganizationKey || !result.Data.Active {
		return "", ErrGatewayCredentials
	}
	key := strings.TrimSpace(result.Data.Token)
	if !strings.HasPrefix(key, "gk_") || len(key) <= 3 || strings.ContainsAny(key, "\x00\r\n") {
		return "", ErrGatewayCredentials
	}
	return key, nil
}
