package lark

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Config init, login and logout all rewrite the CLI's shared configuration.
// A per-profile gate alone would let different profiles lose each other's writes.
var configurationGate = make(chan struct{}, 1)

func lockConfiguration(ctx context.Context) (func(), error) {
	select {
	case configurationGate <- struct{}{}:
		return func() { <-configurationGate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SetupResult contains public metadata only. Found does not mean OAuth succeeded.
// A failed/cancelled CLI may already have saved a configuration; callers must
// offer rediscovery instead of automatically starting another registration.
type SetupResult struct {
	Profile platform.CLIProfile
	AppID   string
	AppName string
	Found   bool
}

type SetupProgress struct {
	Profile platform.CLIProfile
	URL     string
	QRCode  string
}

type configurationProfile struct {
	Name   string `json:"name"`
	AppID  string `json:"appId"`
	Brand  string `json:"brand"`
	Active bool   `json:"active"`
	User   string `json:"user"`
}

func (c *Client) configurationProfiles(ctx context.Context) ([]configurationProfile, error) {
	data, err := c.run(ctx, "profile", "list")
	if err != nil {
		return nil, err
	}
	var items []configurationProfile
	if decode(data, &items) != nil || items == nil || len(items) > 128 {
		return nil, failure("invalid_response")
	}
	seen := map[string]bool{}
	for _, item := range items {
		if !namePattern.MatchString(item.Name) || item.Name == "." || item.Name == ".." || seen[item.Name] || !validAppID(item.AppID) || Origin(item.Brand) == "" {
			return nil, failure("invalid_response")
		}
		seen[item.Name] = true
	}
	return items, nil
}

func validAppID(id string) bool { return strings.HasPrefix(id, "cli_") && namePattern.MatchString(id) }

// ConfigureApplication invokes only the official pinned CLI. It never uses a shell or
// accepts a caller-selected profile to overwrite. Lark remains an explicit
// fallback until its official page has the same real acceptance as Feishu.
func ConfigureApplication(ctx context.Context, executable, brand string, ready func(SetupProgress)) (SetupResult, error) {
	var result SetupResult
	if Origin(brand) == "" {
		return result, failure("invalid_profile")
	}
	if brand != "feishu" {
		return result, failure("setup_brand_unverified")
	}
	// Reject duplicate setup rather than enqueue another remote registration.
	select {
	case configurationGate <- struct{}{}:
		defer func() { <-configurationGate }()
	default:
		return result, failure("configuration_busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	discovery, err := Discover(ctx, executable, brand)
	if err != nil {
		return result, err
	}
	c, err := New(platform.CLIProfile{Version: 1, Executable: discovery.Executable, Profile: "setup", Brand: brand, CLIVersion: CLIVersion})
	if err != nil {
		return result, err
	}
	c.profile.Profile = "" // Discovery and init do not select a possibly missing app.
	before, err := c.configurationProfiles(ctx)
	if err != nil {
		return result, err
	}
	if len(before) >= 128 {
		return result, failure("configuration_capacity")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return result, failure("query_failed")
	}
	name := "teamx-" + brand + "-" + hex.EncodeToString(nonce[:])
	for _, item := range before {
		if item.Name == name || item.AppID == name {
			return result, failure("profile_conflict")
		}
	}
	result.Profile = platform.CLIProfile{Version: 1, Executable: discovery.Executable, Profile: name, Brand: brand, CLIVersion: CLIVersion}
	ready(SetupProgress{Profile: result.Profile})
	// Recheck after reserving the service session and immediately before argv
	// execution. Outside CLI users do not participate in our configuration gate.
	checked, err := c.configurationProfiles(ctx)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(before, checked) {
		return result, failure("configuration_changed")
	}
	stdout, runErr := c.runRegistration(ctx, name, brand, func(link, qr string) {
		ready(SetupProgress{Profile: result.Profile, URL: link, QRCode: qr})
	})
	// Always rediscover after settling the process, even if cancellation or a
	// post-save credential probe made it exit nonzero. Never roll back CLI writes.
	recovery, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	after, discoverErr := c.configurationProfiles(recovery)
	if discoverErr != nil {
		if runErr != nil {
			return result, runErr
		}
		return result, failure("configuration_unconfirmed")
	}
	var found *configurationProfile
	var existing []configurationProfile
	for i := range after {
		if after[i].Name == name {
			found = &after[i]
		} else {
			existing = append(existing, after[i])
		}
	}
	if found != nil {
		result.Found, result.AppID = true, found.AppID
	}
	if !sameConfigurationProfiles(before, existing) {
		return result, failure("configuration_changed")
	}
	if found != nil && found.Brand != brand {
		return result, failure("identity_mismatch")
	}
	if runErr != nil {
		return result, runErr
	}
	var completed struct {
		AppID string `json:"appId"`
		Brand string `json:"brand"`
	}
	if decode(stdout, &completed) != nil || !validAppID(completed.AppID) || completed.Brand != brand || found == nil || completed.AppID != found.AppID {
		return result, failure("configuration_unconfirmed")
	}
	c.profile = result.Profile
	data, err := c.run(recovery, "config", "show")
	var config struct {
		Profile string `json:"profile"`
		AppID   string `json:"appId"`
		Brand   string `json:"brand"`
	}
	if err != nil || decode(data, &config) != nil || config.Profile != name || config.AppID != result.AppID || config.Brand != brand {
		return result, failure("configuration_unconfirmed")
	}
	// Name lookup is optional: app configuration must remain recoverable even
	// when the bot capability or a metadata endpoint is temporarily unavailable.
	data, err = c.run(recovery, "api", "GET", "/open-apis/bot/v3/info", "--as", "bot", "--format", "json")
	var info struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     struct {
			Name string `json:"app_name"`
		} `json:"data"`
	}
	if err == nil && decode(data, &info) == nil && info.OK && info.Identity == "bot" && len(info.Data.Name) <= 1024 && !strings.ContainsAny(info.Data.Name, "\x00\r\n") {
		result.AppName = info.Data.Name
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

func sameConfigurationProfiles(before, after []configurationProfile) bool {
	// CLI list order is meaningful (Apps[0] is the implicit default).
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i] != after[i] {
			return false
		}
	}
	return true
}

// The only accepted textual output is a complete official session URL line.
// Raw output, device codes and credentials stay inside these bounded buffers.
type registrationOutput struct {
	buffer  limitedBuffer
	pending []byte
	brand   string
	link    string
	err     error
	cancel  context.CancelFunc
	ready   func(string) error
}

func (o *registrationOutput) Write(p []byte) (int, error) {
	n, err := o.buffer.Write(p)
	if err != nil {
		return n, err
	}
	o.pending = append(o.pending, p...)
	for {
		i := bytes.IndexByte(o.pending, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(o.pending[:i]))
		o.pending = o.pending[i+1:]
		if strings.HasPrefix(line, "https://") {
			if !validRegistrationURL(line, o.brand) || (o.link != "" && line != o.link) {
				o.err = failure("invalid_response")
			} else if o.link == "" {
				o.link = line
				o.err = o.ready(line)
			}
			if o.err != nil {
				o.cancel()
				return n, o.err
			}
		}
	}
	return n, nil
}

func validRegistrationURL(raw, brand string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || u.Scheme != "https" || u.Host != strings.TrimPrefix(Origin(brand), "https://") || u.Path != "/page/cli" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "\x00\r\n\t ") {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 4 || !namePattern.MatchString(q.Get("user_code")) || q.Get("lpv") != CLIVersion || q.Get("ocv") != CLIVersion || q.Get("from") != "cli" {
		return false
	}
	for _, values := range q {
		if len(values) != 1 {
			return false
		}
	}
	return true
}

func (c *Client) runRegistration(ctx context.Context, name, brand string, ready func(string, string)) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := &limitedBuffer{limit: 64 << 10, cancel: cancel}
	stderr := &registrationOutput{buffer: limitedBuffer{limit: 64 << 10, cancel: cancel}, brand: brand, cancel: cancel}
	stderr.ready = func(link string) error {
		qr, err := c.qrcode(ctx, link)
		if err == nil && ctx.Err() == nil {
			ready(link, qr)
		}
		return err
	}
	cmd := c.command(ctx, "", "config", "init", "--new", "--brand", brand, "--name", name, "--lang", "zh")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	settleProcess(cmd)
	if stdout.overflow || stderr.buffer.overflow {
		return nil, failure("output_limit")
	}
	if stderr.err != nil {
		return nil, stderr.err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return nil, failure("cli_unavailable")
		}
		// CLI text may precede its terminal error envelope. Never return text.
		data := bytes.TrimSpace(stderr.buffer.Bytes())
		if i := bytes.LastIndex(data, []byte("\n{")); i >= 0 {
			data = data[i+1:]
		}
		var terminal struct {
			Error struct {
				Subtype string `json:"subtype"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &terminal) == nil && terminal.Error.Subtype == "token_expired" {
			return nil, failure("timeout")
		}
		if category := classify(data); ErrorCode(category) != "query_failed" {
			return nil, category
		}
		return nil, failure("configuration_failed")
	}
	if stderr.link == "" {
		return nil, failure("configuration_unconfirmed")
	}
	return stdout.Bytes(), nil
}
