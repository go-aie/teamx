package lark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

type SendPermission struct {
	SelfSendEnabled           bool         `json:"self_send_enabled"`
	UserSendEnabled           bool         `json:"user_send_enabled"`
	GroupSendEnabled          bool         `json:"group_send_enabled"`
	SendReason                string       `json:"send_reason"`
	ReplyEnabled              bool         `json:"reply_enabled"`
	AccountFixed              bool         `json:"account_fixed"`
	UserScopes                string       `json:"user_scopes"` // granted, missing, unchecked
	ApplicationScopes         string       `json:"application_scopes"`
	MissingUser               []string     `json:"missing_user,omitempty"`
	MissingApplication        []string     `json:"missing_application,omitempty"`
	AdapterImplemented        bool         `json:"adapter_implemented"`
	Reason                    string       `json:"reason"`
	TargetVerificationEnabled bool         `json:"target_verification_enabled"`
	PeopleSearchEnabled       bool         `json:"people_search_enabled"`
	Capabilities              []Capability `json:"capabilities"`
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
	required := strings.Fields(SendScopes + " " + SendTargetScopes + " " + PeopleSearchScopes)
	app, user := c.inspectScopes(ctx, p.Account, required)
	if app.ReasonCode == "identity_mismatch" {
		return result, failure("identity_mismatch")
	}
	// The legacy aggregate fields continue to describe reply scopes only.
	// Optional search/target dependencies cannot disable ordinary replies.
	result.ApplicationScopes, result.UserScopes = app.State, user.State
	for _, scope := range strings.Fields(SendScopes) {
		if slices.Contains(app.Missing, scope) {
			result.MissingApplication = append(result.MissingApplication, scope)
		}
		if slices.Contains(user.Missing, scope) {
			result.MissingUser = append(result.MissingUser, scope)
		}
	}
	if app.State != "unchecked" {
		result.ApplicationScopes = "granted"
		if len(result.MissingApplication) > 0 {
			result.ApplicationScopes = "missing"
		}
	}
	if user.State != "unchecked" {
		result.UserScopes = "granted"
		if len(result.MissingUser) > 0 {
			result.UserScopes = "missing"
		}
	}
	if app.State == "unchecked" {
		result.Reason = app.ReasonCode
	} else if user.State == "unchecked" {
		result.Reason = user.ReasonCode
	}
	// These flags describe adapter readiness under the current scopes, not any
	// particular member's saved grants or permission to perform a business action.
	result.ReplyEnabled = result.UserScopes == "granted" && result.ApplicationScopes == "granted"
	result.TargetVerificationEnabled = app.State != "unchecked" && user.State != "unchecked" && !slices.Contains(app.Missing, SendTargetScopes) && !slices.Contains(user.Missing, SendTargetScopes)
	result.PeopleSearchEnabled = app.State != "unchecked" && user.State != "unchecked" && !slices.Contains(app.Missing, PeopleSearchScopes) && !slices.Contains(user.Missing, PeopleSearchScopes)
	result.GroupSendEnabled = result.ReplyEnabled && result.TargetVerificationEnabled
	result.SelfSendEnabled = result.GroupSendEnabled
	for _, v := range permissionCapabilities(PermissionCheck{Application: app, User: user, AccountFixed: true}, "darwin", "") {
		if v.Name == "people.search" || strings.HasPrefix(v.Name, "message.") && v.Name != "message.read" {
			result.Capabilities = append(result.Capabilities, v)
		}
	}
	return result, nil
}

// SendReader exposes fixed target reads without exposing credentials or arbitrary argv.
func (p *Pinned) SendReader() SendReader { return p.client }
