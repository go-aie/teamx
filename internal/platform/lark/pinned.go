package lark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/go-aie/teamx/internal/platform"
)

// Pinned owns an ephemeral CLI configuration containing only public identity
// and a keychain reference. Tokens, DPoP keys and app secrets remain CLI-owned.
// On macOS the keychain account is appID:openID independently of configDir.
// Ordinary login/profile switches use a different configuration file; deleting
// that keychain account causes failure, never selection of another account.
// Other storage backends need their own isolation acceptance before enabling.
type Pinned struct {
	client    *Client
	Account   platform.AccountIdentity
	closeOnce sync.Once
}

func Pin(ctx context.Context, p platform.CLIProfile, expected platform.AccountIdentity) (*Pinned, error) {
	return (*ClientFactory)(nil).Pin(ctx, p, expected)
}

func (f *ClientFactory) Pin(ctx context.Context, p platform.CLIProfile, expected platform.AccountIdentity) (*Pinned, error) {
	if runtime.GOOS != "darwin" {
		return nil, failure("account_pinning_unavailable")
	}
	if !ValidAccount(expected, p) {
		return nil, failure("identity_mismatch")
	}
	c, err := f.New(p)
	if err != nil {
		return nil, err
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	// Read non-secret policy only; never copy raw config or resolved credentials.
	data, err := c.run(ctx, "config", "show")
	if err != nil {
		return nil, err
	}
	var config struct {
		Profile  string `json:"profile"`
		AppID    string `json:"appId"`
		Brand    string `json:"brand"`
		DPoPMode string `json:"dpopMode"`
	}
	if decode(data, &config) != nil || config.Profile != p.Profile || config.Brand != p.Brand || "lark:open_id:"+config.AppID != expected.ID.Namespace {
		return nil, failure("identity_mismatch")
	}
	if config.DPoPMode != "required" && config.DPoPMode != "preferred" && config.DPoPMode != "disabled" {
		return nil, failure("account_pinning_unavailable")
	}
	dir, err := os.MkdirTemp("", "teamx-lark-pinned-")
	if err != nil {
		return nil, failure("account_pinning_unavailable")
	}
	pin := &Pinned{client: c, Account: expected}
	c.configDir = dir
	app := map[string]any{"name": p.Profile, "appId": config.AppID, "appSecret": map[string]string{"source": "keychain", "id": "appsecret:" + config.AppID}, "brand": p.Brand, "dpopMode": config.DPoPMode, "defaultAs": "user", "users": []map[string]string{{"userOpenId": expected.ID.Value}}}
	body, _ := json.Marshal(map[string]any{"currentApp": config.AppID, "apps": []any{app}})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0400); err != nil {
		pin.Close()
		return nil, failure("account_pinning_unavailable")
	}
	validation, err := c.verify(ctx)
	if err != nil || validation.Account != expected {
		pin.Close()
		if err != nil {
			return nil, err
		}
		return nil, failure("identity_mismatch")
	}
	return pin, nil
}
func (p *Pinned) Close() {
	if p != nil {
		p.closeOnce.Do(func() { _ = os.RemoveAll(p.client.configDir) })
	}
}
func (p *Pinned) Verify(ctx context.Context) (platform.Validation, error) {
	if p == nil || p.client == nil {
		return platform.Validation{}, failure("account_pinning_unavailable")
	}
	v, err := p.client.Verify(ctx)
	if err == nil && v.Account != p.Account {
		return platform.Validation{}, failure("identity_mismatch")
	}
	return v, err
}

const SendScopes = "im:message.send_as_user im:message"

type SendPermission struct {
	SelfSendEnabled    bool     `json:"self_send_enabled"`
	UserSendEnabled    bool     `json:"user_send_enabled"`
	GroupSendEnabled   bool     `json:"group_send_enabled"`
	SendReason         string   `json:"send_reason"`
	ReplyEnabled       bool     `json:"reply_enabled"`
	AccountFixed       bool     `json:"account_fixed"`
	UserScopes         string   `json:"user_scopes"` // granted, missing, unchecked
	ApplicationScopes  string   `json:"application_scopes"`
	MissingUser        []string `json:"missing_user,omitempty"`
	MissingApplication []string `json:"missing_application,omitempty"`
	AdapterImplemented bool     `json:"adapter_implemented"`
	Reason             string   `json:"reason"`
}

func (p *Pinned) SendPermission(ctx context.Context) (SendPermission, error) {
	result := SendPermission{SendReason: SendDisabledReason, AccountFixed: true, UserScopes: "unchecked", ApplicationScopes: "unchecked", AdapterImplemented: true}
	if _, err := p.Verify(ctx); err != nil {
		return result, err
	}
	c := p.client
	unlock, err := c.lock(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	// auth check exits 1 for missing scopes, so use the public whoami-independent
	// CLI check result, preserving stdout only for this fixed read contract.
	data, err := c.run(ctx, "auth", "scopes", "--json")
	if err != nil {
		return result, err
	}
	var app struct {
		AppID      string   `json:"appId"`
		TokenType  string   `json:"tokenType"`
		Scopes     []string `json:"scopes"`
		UserScopes []string `json:"userScopes"`
	}
	if decode(data, &app) != nil || "lark:open_id:"+app.AppID != p.Account.ID.Namespace {
		return result, failure("invalid_response")
	}
	scopes := app.UserScopes
	if app.TokenType == "" {
		scopes = app.Scopes
	}
	for _, required := range strings.Fields(SendScopes) {
		found := false
		for _, scope := range scopes {
			if scope == required {
				found = true
			}
		}
		if !found {
			result.MissingApplication = append(result.MissingApplication, required)
		}
	}
	result.ApplicationScopes = "granted"
	if len(result.MissingApplication) > 0 {
		result.ApplicationScopes = "missing"
	}
	data, err = c.runCheck(ctx, "auth", "check", "--scope", SendScopes, "--json")
	if err != nil {
		return result, err
	}
	var user struct {
		OK      bool     `json:"ok"`
		Granted []string `json:"granted"`
		Missing []string `json:"missing"`
	}
	if decode(data, &user) != nil || (user.OK && len(user.Missing) != 0) || len(user.Granted)+len(user.Missing) != 2 {
		return result, failure("invalid_response")
	}
	result.MissingUser = user.Missing
	result.UserScopes = "granted"
	if !user.OK {
		result.UserScopes = "missing"
	}
	// These flags describe adapter readiness under the current scopes, not any
	// particular member's saved grants or permission to perform a business action.
	result.ReplyEnabled = result.UserScopes == "granted" && result.ApplicationScopes == "granted"
	result.GroupSendEnabled = result.ReplyEnabled
	result.SelfSendEnabled = result.ReplyEnabled
	return result, nil
}

// SendReader exposes fixed target reads without exposing credentials or arbitrary argv.
func (p *Pinned) SendReader() SendReader { return p.client }
