package room

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func remoteAccessEnabled(c startupConfig) bool {
	if c.Server.RemoteEnabled != nil {
		return *c.Server.RemoteEnabled
	}
	return c.Server.PublicBaseURL != "" // Preserve existing HTTPS deployments.
}

func validateRemoteAccess(c startupConfig, login bool) error {
	if !remoteAccessEnabled(c) {
		if c.Server.PublicBaseURL != "" || len(c.Server.TrustedProxies) > 0 {
			return errors.New("remote access must be enabled before configuring an external origin or proxy")
		}
		return nil
	}
	if !login {
		return errors.New("remote access requires login verification")
	}
	u, err := url.Parse(c.Server.PublicBaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.Contains(c.Server.PublicBaseURL, "#") {
		return errors.New("remote access requires one HTTPS root origin in server.public_base_url")
	}
	return nil
}

func validateAccessListen(c startupConfig, address string) error {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil {
		return errors.New("listen must use an IP and port")
	}
	if !ip.IsLoopback() && !remoteAccessEnabled(c) {
		return errors.New("non-loopback listen requires remote access and login verification")
	}
	if !ip.IsLoopback() && !ip.IsUnspecified() {
		return errors.New("remote listen must use 0.0.0.0 or :: so local Agent connections remain available")
	}
	return validateRemoteAccess(c, c.Access.LoginEnabled == nil || *c.Access.LoginEnabled)
}

func (a *serverAccess) localOrigin(origin string) bool {
	if origin == a.internal {
		return true
	}
	u, err := url.Parse(a.internal)
	return err == nil && origin == "http://"+net.JoinHostPort("localhost", u.Port())
}

// Wildcard listener addresses are never advertised to local clients or Agents.
func internalAccessEndpoint(address string) string {
	host, port, _ := net.SplitHostPort(address)
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		if ip.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

func (a *serverAccess) writeSession(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"operator": a.operator, "installation_id": a.installationID, "login_enabled": !a.disabled})
}

type accessSettingsView struct {
	Version         uint64 `json:"version"`
	LoginEnabled    bool   `json:"login_enabled"`
	RemoteEnabled   bool   `json:"remote_enabled"`
	PublicBaseURL   string `json:"public_base_url"`
	RestartRequired bool   `json:"restart_required"`
	RemoteActive    bool   `json:"remote_active"`
	Writable        bool   `json:"writable"`
}

// Caller holds a.mu. Listener/origin changes are persisted for the next start;
// authentication changes take effect immediately and revoke browser sessions.
func (a *serverAccess) settingsView() accessSettingsView {
	restart := a.public != a.config.Server.PublicBaseURL && (a.public != a.internal || a.config.Server.PublicBaseURL != "")
	restart = restart || !slices.Equal(a.activeProxies, a.config.Server.TrustedProxies)
	if a.config.Server.Listen != nil {
		restart = restart || a.activeListen != *a.config.Server.Listen
	}
	return accessSettingsView{a.version, !a.disabled, remoteAccessEnabled(a.config), a.config.Server.PublicBaseURL,
		restart,
		a.public != a.internal, a.document != nil}
}

type accessSettingsInput struct {
	Caller          Caller `json:"caller"`
	ExpectedVersion uint64 `json:"expected_version"`
	LoginEnabled    bool   `json:"login_enabled"`
	RemoteEnabled   bool   `json:"remote_enabled"`
	PublicBaseURL   string `json:"public_base_url"`
	Token           string `json:"token"`
	CurrentToken    string `json:"current_token"`
}

func (a *serverAccess) confirmToken(token string) bool {
	if a.disabled {
		return true
	}
	if a.now().Before(a.retryAfter) {
		return false
	}
	hash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(hash[:], a.admin[:]) != 1 {
		a.attempts++
		if a.attempts >= 5 {
			a.retryAfter = a.now().Add(time.Minute)
			a.attempts = 0
		}
		return false
	}
	a.attempts = 0
	return true
}

func (a *serverAccess) serveSettings(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/access/settings" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(a.settingsView())
		return
	}
	if r.Method != http.MethodPost {
		accessError(w, 405, "method_not_allowed")
		return
	}
	var input accessSettingsInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		accessError(w, 400, "invalid_request")
		return
	}
	if input.Caller.Kind != "human" || input.Caller.ID != a.operator || input.Caller.MembershipID != "" {
		accessError(w, 403, "identity_mismatch")
		return
	}
	switch r.URL.Path {
	case "/v1/access/settings/generate":
		// An authenticated candidate has no authority until the save below.
		token, err := randomAccessToken()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
		return
	case "/v1/access/settings":
	default:
		accessError(w, 404, "not_found")
		return
	}
	if !a.confirmToken(input.CurrentToken) {
		accessError(w, 403, "current_token_required")
		return
	}
	if input.ExpectedVersion != a.version {
		writeAPIError(w, ErrConflict)
		return
	}
	if a.document == nil {
		writeAPIError(w, ErrRequest)
		return
	}
	if !input.LoginEnabled && a.public != a.internal {
		writeAPIError(w, invalidField("login_enabled", "远程入口仍在运行。请先关闭远程访问并重启，再关闭登录验证。"))
		return
	}
	next := a.config
	next.Access.SettingsVersion = a.version + 1
	next.Access.LoginEnabled = &input.LoginEnabled
	next.Server.RemoteEnabled = &input.RemoteEnabled
	next.Server.PublicBaseURL = strings.TrimSpace(input.PublicBaseURL)
	if !input.RemoteEnabled {
		next.Server.PublicBaseURL = ""
		next.Server.TrustedProxies = nil
	}
	if err := validateRemoteAccess(next, input.LoginEnabled); err != nil {
		writeAPIError(w, invalidField("remote_enabled", err.Error()))
		return
	}
	if input.Token != "" && !validAdminToken(input.Token) {
		writeAPIError(w, invalidField("token", "访问令牌需为 16–256 个可打印的英文字符，不含首尾空白；可以使用多个英文单词组成的长口令。"))
		return
	}
	token := input.Token
	if input.LoginEnabled && token == "" {
		var err error
		token, err = readAdminToken(adminTokenPath(a.config))
		if err != nil {
			writeAPIError(w, invalidField("token", "请先生成或输入访问令牌。"))
			return
		}
	}
	if token != "" {
		id, err := newOpaqueID()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		next.Access.AdminTokenFile = filepath.Join("auth", "admin-token-"+id)
		if err := makeDurableDir(filepath.Dir(adminTokenPath(next))); err != nil {
			writeAPIError(w, err)
			return
		}
		// Install an immutable credential first. The atomic config replacement
		// is the sole activation point; failures cannot destroy the old token.
		if err := installMissing(adminTokenPath(next), []byte(token+"\n")); err != nil {
			writeAPIError(w, err)
			return
		}
	}
	doc := a.document
	doc.mu.Lock()
	err := doc.commitLocked(func(source []byte) ([]byte, func(), error) {
		if input.ExpectedVersion != a.version {
			return nil, nil, ErrConflict
		}
		var values map[string]any
		if err := toml.Unmarshal(source, &values); err != nil {
			return nil, nil, err
		}
		server, _ := values["server"].(map[string]any)
		if server == nil {
			server = map[string]any{}
		}
		server["remote_enabled"], server["public_base_url"] = input.RemoteEnabled, next.Server.PublicBaseURL
		if input.RemoteEnabled {
			if len(next.Server.TrustedProxies) == 0 {
				next.Server.TrustedProxies = []string{"127.0.0.1", "::1"}
			}
			server["trusted_proxies"] = next.Server.TrustedProxies
		} else {
			delete(server, "trusted_proxies")
		}
		// Preserve the port when switching between loopback and all interfaces.
		address, _ := server["listen"].(string)
		if address == "" {
			address = strings.TrimPrefix(a.internal, "http://")
		}
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, nil, err
		}
		if input.RemoteEnabled != remoteAccessEnabled(a.config) {
			host := "127.0.0.1"
			if input.RemoteEnabled {
				host = "0.0.0.0"
			}
			address = net.JoinHostPort(host, port)
		}
		server["listen"] = address
		next.Server.Listen = &address
		section, err := toml.Marshal(struct {
			Server map[string]any `toml:"server"`
			Access accessConfig   `toml:"access"`
		}{server, next.Access})
		if err != nil {
			return nil, nil, err
		}
		base, err := removeConfigSubtrees(source, []string{"server"}, []string{"access"})
		if err != nil {
			return nil, nil, err
		}
		return append(append(base, '\n'), section...), func() {
			changed := a.disabled == input.LoginEnabled || token != "" && sha256.Sum256([]byte(token)) != a.admin
			a.config, a.disabled = next, !input.LoginEnabled
			if input.LoginEnabled {
				a.admin = sha256.Sum256([]byte(token))
			}
			if changed {
				a.sessions = map[[32]byte]accessSession{}
			}
			a.version++
		}, nil
	})
	doc.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	http.SetCookie(w, a.cookie("", -1))
	_ = json.NewEncoder(w).Encode(a.settingsView())
}
