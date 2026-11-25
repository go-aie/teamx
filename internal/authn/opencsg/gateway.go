package opencsg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-aie/teamx/internal/authn"
)

var ErrGatewayCredentials = errors.New("OpenCSG AI Gateway credential unavailable")

// GatewayAPIKey uses the authenticated UUID, never callback namespace or gateway
// hints. The same pinned client forbids redirects and bounds the exchange.
func (s *SSO) GatewayAPIKey(ctx context.Context, identity authn.Identity, values url.Values) (string, error) {
	if identity.Provider != "opencsg" || identity.Authority != s.hub || identity.Subject == "" || identity.DisplayName == "" {
		return "", ErrGatewayCredentials
	}
	token := values.Get("jwt_token")
	if token == "" {
		token = values.Get("jwt")
	}
	if token == "" {
		return "", ErrGatewayCredentials
	}
	endpoint := s.hub + "/api/v1/namespaces/" + url.PathEscape(identity.Subject) + "/apikeys/builtin?" + url.Values{"current_user": {identity.DisplayName}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", ErrGatewayCredentials
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
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
			Token string `json:"token"`
		} `json:"data"`
	}
	if err != nil || len(body) > 64<<10 || json.Unmarshal(body, &result) != nil {
		return "", ErrGatewayCredentials
	}
	key := strings.TrimSpace(result.Data.Token)
	if !strings.HasPrefix(key, "gk_") || len(key) <= 3 || strings.ContainsAny(key, "\x00\r\n") {
		return "", ErrGatewayCredentials
	}
	return key, nil
}
