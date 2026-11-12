package lark

import (
	"context"
	"encoding/base64"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// ProfileOption contains display metadata only, never CLI credentials.
type ProfileOption struct {
	Name        string `json:"name"`
	Brand       string `json:"brand"`
	AppID       string `json:"appId,omitempty"`
	User        string `json:"user,omitempty"`
	TokenStatus string `json:"tokenStatus,omitempty"`
}

type Discovery struct {
	Executable   string          `json:"executable"`
	Profiles     []ProfileOption `json:"profiles"`
	SetupProfile string          `json:"setup_profile"`
}

func Discover(ctx context.Context, executable, brand string) (Discovery, error) {
	result := Discovery{Profiles: []ProfileOption{}}
	if Origin(brand) == "" {
		return result, failure("invalid_profile")
	}
	if executable == "" {
		var err error
		executable, err = exec.LookPath("lark-cli")
		if err != nil {
			return result, failure("cli_unavailable")
		}
	}
	// Use the same path validation as saved profiles. Discovery never changes
	// the CLI's default profile or inherits workspace/profile environment flags.
	c, err := New(platform.CLIProfile{Version: 1, Executable: executable, Profile: "discovery", Brand: brand, CLIVersion: CLIVersion})
	if err != nil {
		return result, err
	}
	c.profile.Profile = ""
	version, err := c.run(ctx, "--version")
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(string(version)) != "lark-cli version "+CLIVersion {
		return result, failure("cli_version_mismatch")
	}
	data, err := c.run(ctx, "profile", "list")
	if err != nil {
		return result, err
	}
	var profiles []ProfileOption
	if decode(data, &profiles) != nil || len(profiles) > 128 {
		return result, failure("invalid_response")
	}
	seen := map[string]bool{}
	allNames := map[string]bool{}
	for _, p := range profiles {
		allNames[p.Name] = true
		if p.Brand != brand {
			continue
		}
		if !namePattern.MatchString(p.Name) || p.Name == "." || p.Name == ".." || seen[p.Name] || len(p.User) > 1024 || strings.ContainsAny(p.User, "\x00\r\n") {
			return result, failure("invalid_response")
		}
		seen[p.Name] = true
		// App IDs are display metadata; never pass through arbitrary config fields.
		if p.AppID != "" && !validAppID(p.AppID) {
			return result, failure("invalid_response")
		}
		switch p.TokenStatus {
		case "valid", "expired", "refreshable", "no_token", "error", "":
		default:
			p.TokenStatus = "unknown"
		}
		result.Profiles = append(result.Profiles, p)
	}
	result.SetupProfile = "teamx-" + brand
	for n := 2; allNames[result.SetupProfile]; n++ {
		result.SetupProfile = "teamx-" + brand + "-" + strconv.Itoa(n)
	}
	result.Executable = executable
	return result, nil
}

type LoginResult struct {
	Validation         platform.Validation
	Permissions        *PermissionCheck
	RenewalUnavailable bool
	ExpiresAt          time.Time
}

// Login keeps the profile gate until the CLI has settled. The caller owns the
// lifetime independently of an HTTP request and publishes only the ready link
// and QR image; device codes and token output never leave this function.
func Login(ctx context.Context, p platform.CLIProfile, ready func(string, string)) (LoginResult, error) {
	writeUnlock, err := lockConfiguration(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer writeUnlock()
	c, err := New(p)
	if err != nil {
		return LoginResult{}, err
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	defer unlock()
	if err := c.authConfig(ctx); err != nil {
		return LoginResult{}, err
	}
	// Read the application's actual business permissions before asking the user.
	// OAuth controls are independent; unknown application state is not absence.
	data, err := c.run(ctx, "auth", "scopes", "--json")
	if err != nil {
		return LoginResult{}, failure("application_scopes_unchecked")
	}
	var app struct {
		AppID string `json:"appId"`
	}
	if decode(data, &app) != nil || !validAppID(app.AppID) {
		return LoginResult{}, failure("invalid_response")
	}
	application, err := parseApplicationScopes(data, platform.AccountIdentity{ID: platform.ExternalID{Namespace: "lark:open_id:" + app.AppID}}, DefaultScopes())
	if err != nil {
		return LoginResult{}, err
	}
	requested, err := (PermissionCheck{Application: application}).RequestScopes()
	if err != nil {
		return LoginResult{}, err
	}
	requestedAppID := app.AppID
	scopes := strings.Join(requested, " ")
	data, err = c.run(ctx, "auth", "login", "--scope", scopes, "--no-wait", "--json")
	if err != nil {
		return LoginResult{}, err
	}
	var start struct {
		URL       string `json:"verification_url"`
		Code      string `json:"device_code"`
		ExpiresIn int    `json:"expires_in"`
	}
	if decode(data, &start) != nil || !validVerificationURL(start.URL, p.Brand) || start.Code == "" || len(start.Code) > 8192 || start.ExpiresIn <= 0 || start.ExpiresIn > 3600 {
		return LoginResult{}, failure("invalid_response")
	}
	qr, err := c.qrcode(ctx, start.URL)
	if err != nil {
		return LoginResult{}, err
	}
	ready(start.URL, qr)
	data, err = c.runCommand(ctx, "", time.Duration(start.ExpiresIn)*time.Second, "auth", "login", "--device-code", start.Code, "--json")
	if err != nil && ErrorCode(err) != "permission_denied" {
		return LoginResult{}, err
	}
	var done struct {
		Event         string    `json:"event"`
		UserOpenID    string    `json:"user_open_id"`
		Missing       []string  `json:"missing"`
		RefreshIssued *bool     `json:"refresh_token_issued"`
		ExpiresAt     time.Time `json:"expires_at"`
	}
	// Auth completion is a CLI event (not an OpenAPI ok/data envelope).
	if decode(data, &done) != nil || done.Event != "authorization_complete" || !strings.HasPrefix(done.UserOpenID, "ou_") || done.RefreshIssued == nil || done.ExpiresAt.IsZero() {
		return LoginResult{}, failure("invalid_response")
	}
	// The CLI's success response alone is not verified TeamX account evidence.
	validation, err := c.verify(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	if requestedAppID != "" && validation.Account.ID.Namespace != "lark:open_id:"+requestedAppID {
		return LoginResult{}, failure("identity_mismatch")
	}
	if validation.Account.ID.Value != done.UserOpenID {
		return LoginResult{}, failure("identity_mismatch")
	}
	application, user := c.inspectScopes(ctx, validation.Account, DefaultScopes())
	renewal := c.inspectRenewal(ctx, validation.Account)
	if application.ReasonCode == "identity_mismatch" || renewal.ReasonCode == "identity_mismatch" {
		return LoginResult{}, failure("identity_mismatch")
	}
	check := PermissionCheck{Account: validation.Account, CheckedAt: time.Now().UTC(), Application: application, User: user, Renewal: renewal}
	check.Capabilities = permissionCapabilities(check, runtime.GOOS, "check_missing")
	return LoginResult{Validation: validation, Permissions: &check, RenewalUnavailable: renewal.State != "granted", ExpiresAt: done.ExpiresAt}, nil
}

func Logout(ctx context.Context, p platform.CLIProfile) error {
	writeUnlock, err := lockConfiguration(ctx)
	if err != nil {
		return err
	}
	defer writeUnlock()
	c, err := New(p)
	if err != nil {
		return err
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := c.authConfig(ctx); err != nil {
		return err
	}
	data, err := c.run(ctx, "auth", "logout", "--json")
	if err != nil {
		return err
	}
	var result struct {
		OK        bool   `json:"ok"`
		LoggedOut bool   `json:"loggedOut"`
		Reason    string `json:"reason"`
	}
	if decode(data, &result) != nil || !result.OK || !result.LoggedOut && result.Reason != "not_logged_in" {
		return failure("invalid_response")
	}
	return nil
}

func (c *Client) authConfig(ctx context.Context) error {
	version, err := c.run(ctx, "--version")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(version)) != "lark-cli version "+CLIVersion {
		return failure("cli_version_mismatch")
	}
	data, err := c.run(ctx, "config", "show")
	if err != nil {
		return err
	}
	var config struct {
		Profile string `json:"profile"`
		Brand   string `json:"brand"`
	}
	if decode(data, &config) != nil || config.Profile != c.profile.Profile || config.Brand != c.profile.Brand {
		return failure("identity_mismatch")
	}
	return nil
}

func validVerificationURL(raw, brand string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 8192 || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" || strings.ContainsAny(raw, "\x00\r\n") {
		return false
	}
	domain := "feishu.cn"
	if brand == "lark" {
		domain = "larksuite.com"
	}
	host := strings.ToLower(u.Hostname())
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func (c *Client) qrcode(ctx context.Context, link string) (string, error) {
	dir, err := os.MkdirTemp("", "teamx-lark-auth-")
	if err != nil {
		return "", failure("query_failed")
	}
	defer os.RemoveAll(dir)
	if _, err = c.runInDir(ctx, dir, "auth", "qrcode", link, "--output", "code.png"); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "code.png")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", failure("invalid_response")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 8 || string(b[:8]) != "\x89PNG\r\n\x1a\n" {
		return "", failure("invalid_response")
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b), nil
}
