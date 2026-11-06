// Package lark provides bounded, user-only reads through the pinned official CLI.
// It exposes no arbitrary API, shell, credential or external-write entry point.
package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const CLIVersion = "1.0.99"
const maxOutput = 2 << 20

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func ValidProfile(p platform.CLIProfile) bool {
	return p.Version == 1 && filepath.IsAbs(p.Executable) && filepath.Clean(p.Executable) == p.Executable && !strings.ContainsAny(p.Executable, "\x00\r\n") && namePattern.MatchString(p.Profile) && p.Profile != "." && p.Profile != ".." && (p.Brand == "feishu" || p.Brand == "lark") && p.CLIVersion == CLIVersion
}
func Origin(brand string) string {
	if brand == "feishu" {
		return "https://open.feishu.cn"
	}
	if brand == "lark" {
		return "https://open.larksuite.com"
	}
	return ""
}
func ValidAccount(a platform.AccountIdentity, p platform.CLIProfile) bool {
	return a.Valid() && a.Provider == "lark" && a.Instance == Origin(p.Brand) && strings.HasPrefix(a.ID.Namespace, "lark:open_id:cli_") && strings.HasPrefix(a.ID.Value, "ou_") && a.Tenant.Namespace == "lark:tenant_key" && a.Tenant.Value != ""
}

type Error struct{ Code string }

func (e *Error) Error() string  { return "lark read: " + e.Code }
func failure(code string) error { return &Error{Code: code} }
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "query_failed"
}

// Reader is replaceable without changing Room admission or member bindings.
type Reader interface {
	Verify(context.Context) (platform.Validation, error)
	Messages(context.Context, platform.AccountIdentity, MessagesRequest) (MessagePage, error)
	Message(context.Context, platform.AccountIdentity, string) (Message, error)
}
type Client struct {
	profile  platform.CLIProfile
	requests *requestBudget
}

func New(p platform.CLIProfile) (*Client, error) {
	if !ValidProfile(p) {
		return nil, failure("invalid_profile")
	}
	return &Client{profile: p, requests: accountRequests}, nil
}

// Shared gates include independently constructed clients and Rooms. Waiting is
// cancellable; all profile diagnostics and refresh-triggering calls use the gate.
var gates sync.Map

func (c *Client) lock(ctx context.Context) (func(), error) {
	key := c.profile.Profile
	v, _ := gates.LoadOrStore(key, make(chan struct{}, 1))
	gate := v.(chan struct{})
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (b *limitedBuffer) Len() int      { return b.buffer.Len() }
func (b *limitedBuffer) Bytes() []byte { return b.buffer.Bytes() }
func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.overflow = true
		b.cancel()
		return 0, failure("output_limit")
	}
	return b.buffer.Write(p)
}

// run never returns process errors or stderr text, which can contain private
// content, authorization links or tokens. Only parsed error categories escape.
func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	return c.runInDir(ctx, "", args...)
}

func (c *Client) runInDir(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := &limitedBuffer{limit: maxOutput, cancel: cancel}
	stderr := &limitedBuffer{limit: 64 << 10, cancel: cancel}
	argv := append([]string{"--profile", c.profile.Profile}, args...)
	cmd := exec.CommandContext(ctx, c.profile.Executable, argv...)
	cmd.Dir = dir
	for _, k := range []string{"HOME", "USER", "LOGNAME", "PATH", "TMPDIR", "LANG", "LC_ALL", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Env = append(cmd.Env, "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1")
	cmd.Stdout = out
	cmd.Stderr = stderr
	configureProcess(cmd)
	err := cmd.Run()
	settleProcess(cmd)
	if out.overflow || stderr.overflow {
		return nil, failure("output_limit")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return nil, failure("cli_unavailable")
		}
		err := classify(stderr.Bytes())
		if ErrorCode(err) == "rate_limited" {
			c.requests.limited(ctx)
		}
		return nil, err
	}
	return out.Bytes(), nil
}
func classify(data []byte) error {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) != nil {
		return failure("query_failed")
	}
	switch {
	case e.Error.Code == 1020:
		return failure("cursor_expired")
	case e.Error.Code == 99991400 || e.Error.Subtype == "rate_limit":
		return failure("rate_limited")
	case e.Error.Type == "authentication" && e.Error.Subtype == "refresh_server_error":
		return failure("network_error")
	case e.Error.Type == "config" && e.Error.Subtype == "invalid_client":
		return failure("credential_rejected")
	case e.Error.Type == "config":
		return failure("credential_unavailable")
	case e.Error.Type == "authentication":
		return failure("credential_rejected")
	case e.Error.Type == "authorization" || e.Error.Type == "permission":
		return failure("permission_denied")
	case e.Error.Type == "network" && e.Error.Subtype == "timeout":
		return failure("timeout")
	case e.Error.Type == "network":
		return failure("network_error")
	default:
		return failure("query_failed")
	}
}
func decode(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return failure("invalid_response")
	}
	return nil
}
func (c *Client) api(ctx context.Context, path string, params any, v any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return failure("invalid_request")
	}
	release, err := c.requests.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	data, err := c.run(ctx, "api", "GET", path, "--params", string(p), "--as", "user", "--format", "json")
	if err != nil {
		return err
	}
	var env struct {
		OK       bool            `json:"ok"`
		Identity string          `json:"identity"`
		Data     json.RawMessage `json:"data"`
	}
	if decode(data, &env) != nil || !env.OK || env.Identity != "user" || len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return failure("invalid_response")
	}
	return decode(env.Data, v)
}

type localIdentity struct {
	Profile    string `json:"profile"`
	AppID      string `json:"appId"`
	Brand      string `json:"brand"`
	Identity   string `json:"identity"`
	Available  bool   `json:"available"`
	OnBehalfOf struct {
		OpenID string `json:"openId"`
	} `json:"onBehalfOf"`
}

func (c *Client) who(ctx context.Context) (localIdentity, error) {
	var v localIdentity
	b, e := c.run(ctx, "whoami", "--as", "user")
	if e != nil {
		return v, e
	}
	if decode(b, &v) != nil {
		return v, failure("invalid_response")
	}
	if !v.Available || v.Identity != "user" {
		return v, failure("credential_unavailable")
	}
	if v.Profile != c.profile.Profile || v.Brand != c.profile.Brand || !strings.HasPrefix(v.AppID, "cli_") || !namePattern.MatchString(v.AppID) || !strings.HasPrefix(v.OnBehalfOf.OpenID, "ou_") {
		return v, failure("identity_mismatch")
	}
	return v, nil
}
func (c *Client) verify(ctx context.Context) (platform.Validation, error) {
	var result platform.Validation
	version, err := c.run(ctx, "--version")
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(string(version)) != "lark-cli version "+c.profile.CLIVersion {
		return result, failure("cli_version_mismatch")
	}
	who, err := c.who(ctx)
	if err != nil {
		return result, err
	}
	ctx = context.WithValue(ctx, accountContextKey{}, Origin(who.Brand)+"|lark:open_id:"+who.AppID+"|"+who.OnBehalfOf.OpenID)
	var user struct {
		OpenID    string `json:"open_id"`
		TenantKey string `json:"tenant_key"`
		Name      string `json:"name"`
	}
	if err = c.api(ctx, "/open-apis/authen/v1/user_info", struct{}{}, &user); err != nil {
		return result, err
	}
	after, err := c.who(ctx)
	if err != nil {
		return result, err
	}
	if after != who || user.OpenID != who.OnBehalfOf.OpenID || user.TenantKey == "" {
		return result, failure("identity_mismatch")
	}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin(who.Brand), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: user.TenantKey}}, ID: platform.ExternalID{Namespace: "lark:open_id:" + who.AppID, Value: user.OpenID}}
	if !ValidAccount(account, c.profile) || len(user.Name) > 1024 || strings.ContainsAny(user.Name, "\x00\r\n") {
		return result, failure("invalid_response")
	}
	return platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: user.Name, Account: account}, nil
}
func (c *Client) Verify(ctx context.Context) (platform.Validation, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := c.lock(ctx)
	if err != nil {
		return platform.Validation{}, err
	}
	defer unlock()
	return c.verify(ctx)
}
func VerifyIdentity(ctx context.Context, origin string, p platform.CLIProfile) platform.Validation {
	c, err := New(p)
	var v platform.Validation
	if err == nil && origin != Origin(p.Brand) {
		err = failure("identity_mismatch")
	}
	if err == nil {
		v, err = c.Verify(ctx)
	}
	if err != nil {
		return platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: ErrorCode(err)}
	}
	return v
}
func (c *Client) guarded(ctx context.Context, expected platform.AccountIdentity, read func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !ValidAccount(expected, c.profile) {
		return failure("identity_mismatch")
	}
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	before, err := c.verify(ctx)
	if err != nil {
		return err
	}
	if before.Account != expected {
		return failure("identity_mismatch")
	}
	ctx = context.WithValue(ctx, accountContextKey{}, expected.Instance+"|"+expected.ID.Namespace+"|"+expected.ID.Value)
	err = read(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	after, checkErr := c.verify(ctx)
	if checkErr != nil {
		return checkErr
	}
	if after.Account != expected {
		return failure("identity_mismatch")
	}
	return err
}
func (c *Client) String() string { return fmt.Sprintf("lark CLI reader %s", c.profile.CLIVersion) }
