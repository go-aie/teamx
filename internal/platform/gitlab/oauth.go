package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func setAuthentication(req *http.Request, origin string, credential platform.AccessCredential) error {
	if !validOrigin(origin) || !validToken(credential.Token.Reveal()) || req.URL.Scheme+"://"+req.URL.Host != origin || req.URL.User != nil {
		return platform.ErrCredential
	}
	// Caller-supplied headers cannot select a second credential source.
	for key := range req.Header {
		if strings.EqualFold(key, "PRIVATE-TOKEN") || strings.EqualFold(key, "Authorization") {
			delete(req.Header, key)
		}
	}
	switch credential.Kind {
	case "oauth":
		req.Header.Set("Authorization", "Bearer "+credential.Token.Reveal())
	default:
		return platform.ErrCredential
	}
	return nil
}

// OAuthRefreshError carries only a fixed safe code and the required local
// disposition. It never wraps remote response bodies or transport errors.
type OAuthRefreshError struct{ Code, State string }

func (e *OAuthRefreshError) Error() string { return "GitLab OAuth refresh failed (" + e.Code + ")" }
func OAuthRefreshState(err error) string {
	var e *OAuthRefreshError
	if errors.As(err, &e) {
		return e.State
	}
	return "refresh_unknown"
}
func refreshError(code, state string) error { return &OAuthRefreshError{Code: code, State: state} }

type OAuthRefreshRequest struct {
	Origin, ClientID, RedirectURI string
	ClientSecret, RefreshToken    platform.Secret
	UserID                        int64
	Scopes                        string
}

// RefreshOAuth makes one destructive POST and never retries it. After exchange
// it verifies actual grants and the native user with the candidate token.
func RefreshOAuth(ctx context.Context, input OAuthRefreshRequest, transport http.RoundTripper) (platform.OAuthTokens, error) {
	var tokens platform.OAuthTokens
	if !validOrigin(input.Origin) || !validToken(input.ClientID) || !validToken(input.ClientSecret.Reveal()) || !validToken(input.RefreshToken.Reveal()) || input.UserID <= 0 || input.Scopes == "" {
		return tokens, refreshError("invalid_configuration", "reauthorization_required")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {input.ClientID}, "client_secret": {input.ClientSecret.Reveal()}, "refresh_token": {input.RefreshToken.Reveal()}, "redirect_uri": {input.RedirectURI}}
	return exchangeOAuth(ctx, input, form, transport, nil)
}

type OAuthLoginRequest struct {
	Origin, ClientID, RedirectURI string
	ClientSecret, Code, Verifier  platform.Secret
	UserID                        int64
	Scopes                        string
}

// ExchangeOAuthCode consumes a code once, with confidential client authentication
// and PKCE. UserID zero discovers a new account; reauthorization always pins its user.
func ExchangeOAuthCode(ctx context.Context, in OAuthLoginRequest, transport http.RoundTripper) (platform.OAuthTokens, platform.Validation, error) {
	input := OAuthRefreshRequest{Origin: in.Origin, ClientID: in.ClientID, ClientSecret: in.ClientSecret, RedirectURI: in.RedirectURI, UserID: in.UserID, Scopes: in.Scopes}
	if !validOrigin(in.Origin) || !validToken(in.ClientID) || !validToken(in.ClientSecret.Reveal()) || !validToken(in.Code.Reveal()) || len(in.Verifier.Reveal()) < 43 || len(in.Verifier.Reveal()) > 128 || (in.Scopes != "api" && in.Scopes != "api write_repository") {
		return platform.OAuthTokens{}, platform.Validation{}, refreshError("invalid_configuration", "reauthorization_required")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {in.ClientID}, "client_secret": {in.ClientSecret.Reveal()}, "redirect_uri": {in.RedirectURI}, "code": {in.Code.Reveal()}, "code_verifier": {in.Verifier.Reveal()}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var identity platform.Validation
	tokens, err := exchangeOAuth(ctx, input, form, transport, &identity)
	if err != nil {
		return tokens, identity, err
	}
	return tokens, identity, nil
}

func exchangeOAuth(ctx context.Context, input OAuthRefreshRequest, form url.Values, transport http.RoundTripper, identityOut *platform.Validation) (platform.OAuthTokens, error) {
	var tokens platform.OAuthTokens
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, input.Origin+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokens, refreshError("invalid_configuration", "reauthorization_required")
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := boundedHTTPClient(transport)
	// GitLab reports expires_in relative to issuance. Starting before the POST
	// conservatively includes network time; no fixed two-hour assumption is used.
	started := time.Now().UTC()
	resp, err := client.Do(req)
	if err != nil {
		return tokens, refreshError("transport_failed", "refresh_unknown")
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	resp.Body.Close()
	if readErr != nil || len(data) > 64<<10 {
		return tokens, refreshError("invalid_response", "refresh_unknown")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if (resp.StatusCode == 400 || resp.StatusCode == 401) && (failure.Error == "invalid_grant" || failure.Error == "invalid_client" || failure.Error == "invalid_scope") {
			return tokens, refreshError(failure.Error, "reauthorization_required")
		}
		return tokens, refreshError("exchange_unconfirmed", "refresh_unknown")
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	// Limit arithmetic to a representable duration, without assuming a particular
	// deployment's configured token lifetime.
	decodeErr := decodeJSON(data, &result)
	if validToken(result.AccessToken) {
		tokens.AccessToken = platform.NewSecret(result.AccessToken)
	}
	if validToken(result.RefreshToken) {
		tokens.RefreshToken = platform.NewSecret(result.RefreshToken)
	}
	if decodeErr != nil || !validToken(result.AccessToken) || !validToken(result.RefreshToken) || !strings.EqualFold(result.TokenType, "bearer") || result.ExpiresIn <= 0 || result.ExpiresIn > int64((1<<63-1)/int64(time.Second)) {
		return tokens, refreshError("invalid_response", "refresh_unknown")
	}
	tokens.AccessToken, tokens.RefreshToken = platform.NewSecret(result.AccessToken), platform.NewSecret(result.RefreshToken)
	tokens.ExpiresAt = started.Add(time.Duration(result.ExpiresIn) * time.Second)
	credential := platform.AccessCredential{Kind: "oauth", Token: tokens.AccessToken}
	// Token responses may omit scope. token/info reports the actual granted set.
	infoReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, input.Origin+"/oauth/token/info", nil)
	if setAuthentication(infoReq, input.Origin, credential) != nil {
		return tokens, refreshError("invalid_response", "refresh_unknown")
	}
	infoResp, err := client.Do(infoReq)
	if err != nil {
		return tokens, refreshError("grant_unconfirmed", "refresh_unknown")
	}
	infoData, infoErr := io.ReadAll(io.LimitReader(infoResp.Body, (32<<10)+1))
	infoResp.Body.Close()
	var info struct {
		UserID      int64    `json:"resource_owner_id"`
		Scope       []string `json:"scope"`
		Application struct {
			UID string `json:"uid"`
		} `json:"application"`
	}
	if infoErr != nil || len(infoData) > 32<<10 || infoResp.StatusCode != 200 || decodeJSON(infoData, &info) != nil {
		return tokens, refreshError("grant_unconfirmed", "refresh_unknown")
	}
	scopes, ok := platform.ScopeString(info.Scope)
	if !ok || scopes != input.Scopes || info.Application.UID != input.ClientID {
		return tokens, refreshError("authorization_changed", "reauthorization_required")
	}
	if info.UserID <= 0 || input.UserID != 0 && info.UserID != input.UserID {
		return tokens, refreshError("identity_mismatch", "reauthorization_required")
	}
	tokens.Scopes, _ = platform.NormalizeScopes(info.Scope)
	identity := VerifyIdentityWithCredential(ctx, input.Origin, credential, transport)
	if identity.Status != "valid" {
		return tokens, refreshError("identity_unconfirmed", "refresh_unknown")
	}
	if identity.UserID != info.UserID {
		return tokens, refreshError("authorization_changed", "reauthorization_required")
	}
	if identityOut != nil {
		*identityOut = identity
	}
	return tokens, nil
}

// RevokeOAuth never retries and returns only fixed diagnostics. A failed POST is
// unconfirmed: it does not imply that the remote authorization remains usable.
func RevokeOAuth(ctx context.Context, origin, clientID string, secret, token platform.Secret, transport http.RoundTripper) error {
	if !validOrigin(origin) || !validToken(clientID) || !validToken(secret.Reveal()) || !validToken(token.Reveal()) {
		return refreshError("invalid_configuration", "refresh_unknown")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	form := url.Values{"client_id": {clientID}, "client_secret": {secret.Reveal()}, "token": {token.Reveal()}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/oauth/revoke", strings.NewReader(form.Encode()))
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := boundedHTTPClient(transport).Do(req)
	if err != nil {
		return refreshError("revocation_unconfirmed", "refresh_unknown")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	var receipt map[string]any
	if err != nil || len(data) > 64<<10 || decodeJSON(data, &receipt) != nil || receipt == nil || len(receipt) != 0 || resp.StatusCode != http.StatusOK {
		return refreshError("revocation_unconfirmed", "refresh_unknown")
	}
	return nil
}
