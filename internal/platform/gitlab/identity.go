package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Never follow redirects or surface remote bodies/errors; credentials go only
// to the saved origin. Project access is intentionally not tested here.
func VerifyIdentity(ctx context.Context, origin string, secret platform.Secret) platform.Validation {
	return VerifyIdentityWithTransport(ctx, origin, secret, http.DefaultTransport)
}

func VerifyIdentityWithTransport(ctx context.Context, origin string, secret platform.Secret, transport http.RoundTripper) platform.Validation {
	return VerifyIdentityWithCredential(ctx, origin, platform.AccessCredential{Kind: "oauth", Token: secret}, transport)
}

func VerifyIdentityWithCredential(ctx context.Context, origin string, credential platform.AccessCredential, transport http.RoundTripper) platform.Validation {
	secret := credential.Token
	result := platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "query_failed"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/v4/user", nil)
	if err != nil {
		return result
	}
	if setAuthentication(req, origin, credential) != nil {
		return result
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		result.ErrorCode = "credential_rejected"
		return result
	}
	if resp.StatusCode != http.StatusOK {
		return result
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (32<<10)+1))
	if err != nil || len(data) > 32<<10 {
		result.ErrorCode = "invalid_response"
		return result
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&user) != nil || dec.Decode(new(any)) != io.EOF || user.ID <= 0 || !platform.ValidUsername(user.Username) || strings.Contains(user.Username, secret.Reveal()) {
		result.ErrorCode = "invalid_response"
		return result
	}
	result.Status, result.Username, result.UserID, result.ErrorCode = "valid", user.Username, user.ID, ""
	return result
}

// LegacyAccountIdentity interprets historical integer/username snapshots at the
// GitLab boundary. Callers retain the original disk version and fields.
func LegacyAccountIdentity(origin string, v platform.Validation) (platform.AccountIdentity, bool) {
	if v.UserID <= 0 || !platform.ValidUsername(v.Username) {
		return platform.AccountIdentity{}, false
	}
	a := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "gitlab", Instance: origin}, ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: strconv.FormatInt(v.UserID, 10)}}
	return a, a.Valid() && (v.Account == (platform.AccountIdentity{}) || v.Account == a)
}
