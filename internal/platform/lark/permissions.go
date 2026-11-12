package lark

import (
	"context"
	"encoding/json"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// These closed sets describe today's supported features, never CLI domains or
// every action in the registry. Login requests the app-supported intersection.
const (
	readerScopes       = "offline_access im:chat:read im:message:readonly im:message.p2p_msg:get_as_user im:message.group_msg:get_as_user contact:user.basic_profile:readonly"
	SendScopes         = "im:message.send_as_user im:message"
	SendTargetScopes   = "im:chat.members:read"
	PeopleSearchScopes = "contact:user:search"
)

func DefaultScopes() []string {
	s := strings.Fields(readerScopes + " " + SendScopes + " " + SendTargetScopes + " " + PeopleSearchScopes)
	slices.Sort(s)
	return slices.Compact(s)
}

type CapabilityDependency struct {
	Name        string   `json:"name"`
	Scopes      []string `json:"scopes"`
	RequiresPin bool     `json:"requires_pin"`
	Evidence    string   `json:"evidence,omitempty"`
}

// Each read branch is independent: missing names/search/renewal must not hide
// messages. Target access and speaking restrictions are checked at resolution.
func CapabilityDependencies() []CapabilityDependency {
	return []CapabilityDependency{
		{Name: "chat.read", Scopes: []string{"im:chat:read"}},
		{Name: "message.read", Scopes: []string{"im:message:readonly"}},
		{Name: "notification.private", Scopes: []string{"im:message.p2p_msg:get_as_user"}},
		{Name: "notification.group", Scopes: []string{"im:message.group_msg:get_as_user"}},
		{Name: "token.renew", Scopes: []string{"offline_access"}},
		{Name: "sender.name", Scopes: []string{"contact:user.basic_profile:readonly"}},
		{Name: "people.search", Scopes: strings.Fields(PeopleSearchScopes)},
		{Name: "message.reply", Scopes: strings.Fields(SendScopes), RequiresPin: true},
		{Name: "message.send", Scopes: strings.Fields(SendScopes + " " + SendTargetScopes), RequiresPin: true},
		{Name: "message.send_self", Scopes: strings.Fields(SendScopes + " " + SendTargetScopes), RequiresPin: true, Evidence: "self_delivery"},
	}
}

// ScopeCheck has no credential material. An unsuccessful probe is unchecked,
// never an empty granted set (which would falsely mean all permissions absent).
type ScopeCheck struct {
	State      string   `json:"state"` // granted | missing | unchecked
	Granted    []string `json:"granted,omitempty"`
	Missing    []string `json:"missing,omitempty"`
	ReasonCode string   `json:"reason_code,omitempty"`
}

type Capability struct {
	Name                     string   `json:"name"`
	Configured               *bool    `json:"configured,omitempty"` // absent in platform-only checks
	State                    string   `json:"state"`                // ready | blocked | unchecked | unsupported
	ReasonCode               string   `json:"reason_code,omitempty"`
	MissingApplicationScopes []string `json:"missing_application_scopes,omitempty"`
	MissingUserScopes        []string `json:"missing_user_scopes,omitempty"`
	EvidenceState            string   `json:"evidence_state"`        // not_required | current | missing | stale
	NextAction               string   `json:"next_action,omitempty"` // closed recovery type; never shell text
}

type PermissionCheck struct {
	Account      platform.AccountIdentity `json:"account"`
	CheckedAt    time.Time                `json:"checked_at"`
	Application  ScopeCheck               `json:"application"`
	User         ScopeCheck               `json:"user"`
	Renewal      ScopeCheck               `json:"renewal"`
	AccountFixed bool                     `json:"account_fixed"`
	Capabilities []Capability             `json:"capabilities"`
}

// RequestScopes keeps OAuth controls independent of app business permissions.
// Unknown app permissions must be retried, not treated as an empty scope list.
func (p PermissionCheck) RequestScopes() ([]string, error) {
	if p.Application.State != "granted" && p.Application.State != "missing" {
		return nil, failure("application_scopes_unchecked")
	}
	s := []string{}
	for _, scope := range DefaultScopes() {
		if scope == "offline_access" || slices.Contains(p.Application.Granted, scope) {
			s = append(s, scope)
		}
	}
	return s, nil
}

func CheckPermissions(ctx context.Context, profile platform.CLIProfile, expected platform.AccountIdentity) (PermissionCheck, error) {
	return (*ClientFactory)(nil).CheckPermissions(ctx, profile, expected)
}

// CheckPermissions performs bounded reads on the original profile even on
// hosts without sending support. It never logs in, grants, or sends a message.
func (f *ClientFactory) CheckPermissions(ctx context.Context, profile platform.CLIProfile, expected platform.AccountIdentity) (PermissionCheck, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, err := f.New(profile)
	if err != nil {
		return PermissionCheck{}, err
	}
	p := PermissionCheck{Account: expected}
	err = c.guarded(ctx, expected, func(ctx context.Context) error {
		p.Application, p.User = c.inspectScopes(ctx, expected, DefaultScopes())
		p.Renewal = c.inspectRenewal(ctx, expected)
		if p.Application.ReasonCode == "identity_mismatch" || p.Renewal.ReasonCode == "identity_mismatch" {
			return failure("identity_mismatch")
		}
		return nil
	})
	if err != nil {
		return PermissionCheck{}, err
	}
	pinReason := "account_pinning_unavailable"
	if runtime.GOOS == "darwin" {
		pin, pinErr := f.Pin(ctx, profile, expected)
		if pinErr == nil {
			p.AccountFixed = true
			pin.Close()
			pinReason = ""
		} else {
			pinReason = ErrorCode(pinErr)
			if pinReason == "identity_mismatch" {
				return PermissionCheck{}, pinErr
			}
		}
	}
	if ctx.Err() != nil {
		return PermissionCheck{}, ctx.Err()
	}
	p.CheckedAt = time.Now().UTC()
	p.Capabilities = permissionCapabilities(p, runtime.GOOS, pinReason)
	return p, nil
}

func (c *Client) inspectScopes(ctx context.Context, account platform.AccountIdentity, required []string) (ScopeCheck, ScopeCheck) {
	app := ScopeCheck{State: "unchecked"}
	user := ScopeCheck{State: "unchecked"}
	data, err := c.run(ctx, "auth", "scopes", "--json")
	if err == nil {
		app, err = parseApplicationScopes(data, account, required)
	}
	if err != nil {
		app = ScopeCheck{State: "unchecked", ReasonCode: ErrorCode(err)}
	}
	data, err = c.runCheck(ctx, "auth", "check", "--scope", strings.Join(required, " "), "--json")
	if err == nil {
		user, err = parseUserScopes(data, required)
	}
	if err != nil {
		user = ScopeCheck{State: "unchecked", ReasonCode: ErrorCode(err)}
	}
	return app, user
}

func parseApplicationScopes(data []byte, account platform.AccountIdentity, required []string) (ScopeCheck, error) {
	var wire struct {
		AppID      string          `json:"appId"`
		TokenType  string          `json:"tokenType"`
		Scopes     json.RawMessage `json:"scopes"`
		UserScopes json.RawMessage `json:"userScopes"`
	}
	if decode(data, &wire) != nil || !validAppID(wire.AppID) {
		return ScopeCheck{}, failure("invalid_response")
	}
	if "lark:open_id:"+wire.AppID != account.ID.Namespace {
		return ScopeCheck{}, failure("identity_mismatch")
	}
	scopes := wire.Scopes
	if wire.TokenType == "user" {
		scopes = wire.UserScopes
	} else if wire.TokenType != "" {
		return ScopeCheck{}, failure("invalid_response")
	}
	var granted []string
	if len(scopes) == 0 || decode(scopes, &granted) != nil {
		return ScopeCheck{}, failure("invalid_response")
	}
	p := ScopeCheck{State: "granted"}
	for _, scope := range required {
		if scope == "offline_access" {
			continue
		}
		if slices.Contains(granted, scope) {
			p.Granted = append(p.Granted, scope)
		} else {
			p.Missing = append(p.Missing, scope)
		}
	}
	if len(p.Missing) > 0 {
		p.State = "missing"
	}
	return p, nil
}

func parseUserScopes(data []byte, required []string) (ScopeCheck, error) {
	var wire struct {
		OK      *bool    `json:"ok"`
		Granted []string `json:"granted"`
		Missing []string `json:"missing"`
		Error   string   `json:"error"`
	}
	if decode(data, &wire) != nil || wire.OK == nil {
		return ScopeCheck{}, failure("invalid_response")
	}
	if wire.Error == "not_logged_in" || wire.Error == "no_token" {
		return ScopeCheck{}, failure("credential_unavailable")
	}
	if wire.Error != "" || *wire.OK != (len(wire.Missing) == 0) {
		return ScopeCheck{}, failure("invalid_response")
	}
	seen := map[string]bool{}
	for _, scope := range append(slices.Clone(wire.Granted), wire.Missing...) {
		if seen[scope] || !slices.Contains(required, scope) {
			return ScopeCheck{}, failure("invalid_response")
		}
		seen[scope] = true
	}
	if len(seen) != len(required) {
		return ScopeCheck{}, failure("invalid_response")
	}
	p := ScopeCheck{State: "granted", Granted: wire.Granted, Missing: wire.Missing}
	if !*wire.OK {
		p.State = "missing"
	}
	return p, nil
}

func (c *Client) inspectRenewal(ctx context.Context, account platform.AccountIdentity) ScopeCheck {
	data, err := c.run(ctx, "auth", "status", "--json")
	if err != nil {
		return ScopeCheck{State: "unchecked", ReasonCode: ErrorCode(err)}
	}
	var wire struct {
		AppID      string `json:"appId"`
		Identities struct {
			User struct {
				OpenID           string `json:"openId"`
				TokenStatus      string `json:"tokenStatus"`
				RefreshExpiresAt string `json:"refreshExpiresAt"`
			} `json:"user"`
		} `json:"identities"`
	}
	if decode(data, &wire) != nil {
		return ScopeCheck{State: "unchecked", ReasonCode: "invalid_response"}
	}
	u := wire.Identities.User
	if !validAppID(wire.AppID) || !strings.HasPrefix(u.OpenID, "ou_") {
		return ScopeCheck{State: "unchecked", ReasonCode: "invalid_response"}
	}
	if "lark:open_id:"+wire.AppID != account.ID.Namespace || u.OpenID != account.ID.Value {
		return ScopeCheck{State: "unchecked", ReasonCode: "identity_mismatch"}
	}
	if u.TokenStatus != "valid" && u.TokenStatus != "needs_refresh" {
		return ScopeCheck{State: "unchecked", ReasonCode: "credential_unavailable"}
	}
	// v1.0.99 emits refreshExpiresAt only when a refresh token actually exists.
	if u.RefreshExpiresAt == "" {
		return ScopeCheck{State: "missing", ReasonCode: "refresh_token_missing"}
	}
	expires, err := time.Parse(time.RFC3339, u.RefreshExpiresAt)
	if err != nil {
		return ScopeCheck{State: "unchecked", ReasonCode: "invalid_response"}
	}
	if !expires.After(time.Now()) {
		return ScopeCheck{State: "missing", ReasonCode: "refresh_token_expired"}
	}
	return ScopeCheck{State: "granted"}
}

func permissionCapabilities(p PermissionCheck, goos, pinReason string) []Capability {
	result := []Capability{}
	for _, d := range CapabilityDependencies() {
		v := Capability{Name: d.Name, State: "ready", EvidenceState: "not_required"}
		for _, scope := range d.Scopes {
			if slices.Contains(p.Application.Missing, scope) {
				v.MissingApplicationScopes = append(v.MissingApplicationScopes, scope)
			}
			if slices.Contains(p.User.Missing, scope) {
				v.MissingUserScopes = append(v.MissingUserScopes, scope)
			}
		}
		switch {
		case d.RequiresPin && goos != "darwin":
			v.State, v.ReasonCode = "unsupported", "account_pinning_unavailable"
		case len(v.MissingApplicationScopes) > 0:
			v.State, v.ReasonCode, v.NextAction = "blocked", "application_scopes_missing", "configure_application"
		case p.Application.State != "granted" && p.Application.State != "missing" && d.Name != "token.renew":
			v.State, v.ReasonCode, v.NextAction = "unchecked", p.Application.ReasonCode, "retry_check"
		case len(v.MissingUserScopes) > 0:
			v.State, v.ReasonCode, v.NextAction = "blocked", "user_scopes_missing", "authorize_user"
		case p.User.State != "granted" && p.User.State != "missing":
			v.State, v.ReasonCode, v.NextAction = "unchecked", p.User.ReasonCode, "retry_check"
		case d.Name == "token.renew" && p.Renewal.State != "granted":
			v.State, v.ReasonCode, v.NextAction = "unchecked", p.Renewal.ReasonCode, "retry_check"
			if p.Renewal.State == "missing" {
				v.State, v.NextAction = "blocked", "authorize_user"
			}
		case d.RequiresPin && !p.AccountFixed:
			v.State, v.ReasonCode, v.NextAction = "unchecked", pinReason, "revalidate_account"
		}
		result = append(result, v)
	}
	return result
}
