package room

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/web"
	"github.com/pelletier/go-toml/v2"
)

type accessConfig struct {
	LoginEnabled    *bool  `toml:"login_enabled"`
	SettingsVersion uint64 `toml:"settings_version,omitempty"`
	OperatorID      string `toml:"operator_id"`
	AdminTokenFile  string `toml:"admin_token_file"`
}
type accessSession struct {
	expires time.Time
	Subject userSubject
	renewal *renewableSession // immutable after publication; private host state only
}
type executionBinding struct {
	Subject userSubject `json:"-"`
	SpaceContext
	ServiceInfo
	Caller          Caller
	WorkspaceRootID string `json:"-"`
}
type accessContextKey struct{}
type executionContextKey struct{}

func requestWorkspaceRoot(r *http.Request, info ServiceInfo, caller Caller) string {
	binding, ok := r.Context().Value(executionContextKey{}).(executionBinding)
	if !ok || binding.ServiceInfo != info || binding.Caller != caller || !validMessageWorkspaceRoot(binding.WorkspaceRootID, caller.Author, caller.MembershipID) {
		return ""
	}
	return binding.WorkspaceRootID
}

type serverAccess struct {
	users                 *hostedUsers
	login                 *externalLogin
	privateLark           bool          // immutable Space assembly capability; no default-profile fallback
	revoked               atomic.Bool   // fences an archived runtime without mutating its immutable identity
	control               *serverAccess // shared human authentication; execution credentials remain Space private
	spaces                *spaceServer
	identity              *personalRegistration
	space                 SpaceContext
	inputBindings         map[[32]byte]*inputMCPBinding
	inputMCP              map[bool]*mcpHTTP                 // HTTP Inbox: four tools; CLI Inbox: History only.
	updateLeases          map[*uiUpdateAccessLease]struct{} // guarded by mu
	mcp                   *mcpHTTP
	mcpOnce               sync.Once
	mcpExecutions         map[[32]byte]*mcpExecution
	internal, public      string
	executionRequiresAuth bool
	executionOrigin       string // immutable, authenticated execution transport only
	installationID        string
	managed               bool // Versioned system-service deployment; HTTP boundary only.
	setupRequired         bool
	admin                 [32]byte
	disabled              bool // Only anonymous human access; Agent credentials remain mandatory.
	config                startupConfig
	document              *configDocument
	configFenced          atomic.Bool // Deployment config writes pause while its owner is being replaced.
	version               uint64
	activeListen          string
	activeMode            string
	lanHosts              []string
	activeProxies         []string
	proxies               map[netip.Addr]bool
	mu                    sync.Mutex
	sessions              map[[32]byte]accessSession
	sessionStore          snapshotWriter
	renewal               *accessSessionRenewal
	executions            map[[32]byte]executionBinding
	attempts              int
	retryAfter            time.Time
	now                   func() time.Time
}

func randomAccessToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func validAccessToken(value string) bool {
	b, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == value
}
func validAdminToken(value string) bool {
	if len(value) < accessPasswordPolicy.MinLength || len(value) > accessPasswordPolicy.MaxLength || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}
func readAdminToken(path string) (string, error) {
	return readAdminTokenForUID(path, uint32(os.Geteuid()))
}

func readAdminTokenForUID(path string, uid uint32) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("admin token unavailable; run teamx access init --config <config.toml>: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", errors.New("admin token must be a regular, non-symlink 0600 file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return "", errors.New("admin token must belong to the deployment account")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", ErrConflict
	}
	b, err := io.ReadAll(io.LimitReader(f, 258))
	if err != nil {
		return "", err
	}
	token := strings.TrimSuffix(string(b), "\n")
	if !validAdminToken(token) {
		return "", errors.New("invalid admin token; expected 12 to 256 printable ASCII characters without surrounding whitespace")
	}
	return token, nil
}
func adminTokenPath(c startupConfig) string {
	path := c.Access.AdminTokenFile
	if path == "" {
		path = "auth/admin-token"
	}
	return configPath(filepath.Dir(c.path), path)
}
func newServerAccess(c startupConfig, endpoint string) (*serverAccess, error) {
	if err := checkDeploymentRoot(c); err != nil {
		return nil, err
	}
	hosted := c.Deployment.Mode == "multi-user"
	if !hosted && !validOperatorID(c.Access.OperatorID) {
		return nil, errors.New("access.operator_id missing or invalid; run teamx access init --config <config.toml>")
	}

	disabled := c.Access.LoginEnabled != nil && !*c.Access.LoginEnabled
	if hosted && disabled {
		return nil, errors.New("multi-user authentication requires enabled login")
	}
	if disabled && c.execution != nil {
		return nil, errors.New("isolated execution requires authenticated access")
	}
	if err := validateRemoteAccess(c, !disabled); err != nil {
		return nil, err
	}
	var token string
	if !disabled {
		var err error
		token, err = readAdminToken(adminTokenPath(c))
		if err != nil {
			return nil, err
		}
	}
	a := &serverAccess{internal: endpoint, public: endpoint, admin: sha256.Sum256([]byte(token)), proxies: map[netip.Addr]bool{}, sessions: map[[32]byte]accessSession{}, executions: map[[32]byte]executionBinding{}, now: time.Now}
	if router, ok := c.execution.(agent.EndpointRouter); ok {
		routed, err := router.RouteEndpoint(endpoint)
		if err != nil {
			return nil, err
		}
		if routed != endpoint {
			u, err := url.Parse(routed)
			if err != nil {
				return nil, err
			}
			a.executionOrigin = u.Scheme + "://" + u.Host
		}
	}
	a.executionRequiresAuth = c.execution != nil
	a.disabled, a.config, a.version = disabled, c, 1
	a.sessionStore.ops = defaultSnapshotOps()
	a.activeMode = configuredAccessMode(c)
	a.lanHosts = localLANHosts()
	a.managed = c.installationID != ""
	a.activeListen = strings.TrimPrefix(endpoint, "http://")
	if c.Server.Listen != nil {
		a.activeListen = *c.Server.Listen
	}
	a.activeProxies = append([]string(nil), c.Server.TrustedProxies...)
	if c.Access.SettingsVersion > 0 {
		a.version = c.Access.SettingsVersion
	}
	if c.Server.PublicBaseURL != "" {
		u, err := url.Parse(c.Server.PublicBaseURL)
		if err != nil || strings.Contains(c.Server.PublicBaseURL, "#") || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return nil, errors.New("server.public_base_url must be one HTTPS root origin")
		}
		a.public = u.String()
	}
	for _, ip := range c.Server.TrustedProxies {
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return nil, errors.New("trusted_proxies requires explicit IP addresses")
		}
		a.proxies[addr.Unmap()] = true
	}
	a.installationID = c.installationID
	if a.installationID == "" {
		base := filepath.Dir(c.path)
		if c.Server.DataDir != nil {
			base = *c.Server.DataDir
		}
		if err := makeDurableDir(base); err != nil {
			return nil, err
		}
		path := filepath.Join(base, "installation-id")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			for _, name := range []string{"personal-space-registration.json", "users.json", "spaces.json"} {
				if _, err := os.Lstat(filepath.Join(filepath.Dir(c.path), name)); !errors.Is(err, os.ErrNotExist) {
					return nil, errors.New("installation identity missing beside registered Space; repair required")
				}
			}
		}
		id, err := newOpaqueID()
		if err != nil {
			return nil, err
		}
		if err := installMissing(path, []byte(id)); err != nil {
			return nil, err
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(saved) != 32 {
			return nil, errors.New("invalid installation identity")
		}
		if _, err := hex.DecodeString(string(saved)); err != nil {
			return nil, errors.New("invalid installation identity")
		}
		a.installationID = string(saved)
	}
	if hosted {
		users, err := registerHostedUsers(c, a.installationID)
		if err != nil {
			return nil, err
		}
		a.users = users
		// Restore after selecting the optional renewable provider in the host.
		return a, nil
	}
	registration, err := registerPersonalSpace(c, a.installationID, &snapshotWriter{ops: defaultSnapshotOps()})
	if err != nil {
		return nil, fmt.Errorf("register personal Space: %w", err)
	}
	epoch, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	a.identity = registration
	a.space = SpaceContext{registration.Space.ID, registration.User.ID, epoch}
	if err := a.restoreSessions(); err != nil {
		return nil, fmt.Errorf("restore browser sessions: %w", err)
	}
	return a, nil
}
func requestInstallationID(r *http.Request) string {
	if a, ok := r.Context().Value(accessContextKey{}).(*serverAccess); ok {
		return a.installationID
	}
	return ""
}
func (a *serverAccess) cookieName() string {
	a = a.authority()
	if strings.HasPrefix(a.public, "https:") {
		return "__Host-teamx-session"
	}
	if a.activeMode == accessLAN {
		return "teamx-lan-session"
	}
	return "teamx-local-session"
}
func (a *serverAccess) cookie(value string, age int) *http.Cookie {
	a = a.authority()
	return &http.Cookie{Name: a.cookieName(), Value: value, Path: "/", MaxAge: age, Secure: strings.HasPrefix(a.public, "https:"), HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

// Internal Agent requests remain loopback; browsers use exactly the configured origin.
func (a *serverAccess) requestOrigin(r *http.Request) (string, bool) {
	a = a.authority()
	a.mu.Lock()
	defer a.mu.Unlock()
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	addr, e := netip.ParseAddr(peer)
	if err != nil || e != nil {
		return "", false
	}
	forwarded := r.Header.Values("X-Forwarded-Proto")
	if len(forwarded) > 0 {
		if !a.proxies[addr.Unmap()] || len(forwarded) != 1 || forwarded[0] != "https" {
			return "", false
		}
	}
	if r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("Forwarded") != "" {
		return "", false
	}
	scheme := "http"
	if r.TLS != nil || len(forwarded) > 0 {
		scheme = "https"
	}
	origin := scheme + "://" + r.Host
	if !a.disabled && a.executionOrigin != "" && origin == a.executionOrigin && addr.IsLoopback() && len(forwarded) == 0 && r.Header.Get("Origin") == "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return origin, true
	}
	if a.disabled {
		if !addr.IsLoopback() || len(forwarded) != 0 || !a.localOrigin(origin) {
			return "", false
		}
		return origin, true
	}
	if a.activeMode != accessProxy && addr.IsLoopback() && len(forwarded) == 0 && a.localOrigin(origin) {
		return origin, true
	}
	if a.activeMode == accessLAN && len(forwarded) == 0 && scheme == "http" && (addr.IsPrivate() || addr.IsLoopback()) {
		host, port, err := net.SplitHostPort(r.Host)
		ip, parseErr := netip.ParseAddr(host)
		if err == nil && parseErr == nil && port == portFromAddress(strings.TrimPrefix(a.internal, "http://")) && slices.Contains(a.lanHosts, ip.Unmap().String()) && r.Host == net.JoinHostPort(ip.Unmap().String(), port) {
			return origin, true
		}
	}
	if origin == a.public && (a.activeMode == accessProxy || addr.IsLoopback()) {
		return origin, true
	}
	if origin == a.internal && addr.IsLoopback() && len(forwarded) == 0 && r.Header.Get("Origin") == "" && (strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.URL.Path == "/healthz" && r.Method == http.MethodGet) {
		return origin, true
	}
	return "", false
}
func (a *serverAccess) issueExecution(binding executionBinding) (string, func(), error) {
	binding, err := a.bindExecution(binding)
	if err != nil {
		return "", nil, err
	}
	token, err := randomAccessToken()
	if err != nil {
		return "", nil, err
	}
	hash := sha256.Sum256([]byte(token))
	a.mu.Lock()
	a.executions[hash] = binding
	a.mu.Unlock()
	return token, func() { a.mu.Lock(); delete(a.executions, hash); a.mu.Unlock() }, nil
}
func (a *serverAccess) wrap(next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	a.mcpOnce.Do(func() {
		if a.mcp == nil {
			a.mcp = newMCPHTTP()
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := a.authority()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		origin, ok := a.requestOrigin(r)
		if !ok {
			accessError(w, 403, "invalid_origin")
			return
		}
		if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin {
			accessError(w, 403, "invalid_origin")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), accessContextKey{}, a))
		if auth.users != nil {
			if strings.HasPrefix(r.URL.Path, "/v1/deployment/") {
				auth.serveUserManagement(w, r)
				return
			}
			if r.URL.Path == "/v1/access/method" && r.Method == http.MethodGet {
				_ = writeJSON(w, map[string]string{"method": "external", "provider": auth.config.Deployment.Authentication.Provider, "login_url": externalLoginPath})
				return
			}
			if r.URL.Path == externalLoginPath || r.URL.Path == externalCallbackPath {
				if auth.login == nil {
					accessError(w, 503, "login_unavailable")
					return
				}
				auth.login.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/v1/access/login" || strings.HasPrefix(r.URL.Path, "/v1/access/settings") {
				accessError(w, 403, "deployment_managed")
				return
			}
		} else if r.URL.Path == "/v1/access/method" && r.Method == http.MethodGet {
			_ = writeJSON(w, map[string]string{"provider": "token"})
			return
		}
		if auth.serveRenewableControl(w, r, origin) {
			return
		}
		if a.unavailableSpacePlatform("lark") && strings.HasPrefix(r.URL.Path, "/v1/platforms/lark/") || a.unavailableSpacePlatform("scion") && strings.HasPrefix(r.URL.Path, "/v1/platforms/scion/") {
			accessError(w, 403, "space_capability_unavailable")
			return
		}
		if r.URL.Path == "/mcp" {
			a.serveMCP(w, r)
			return
		}
		if r.URL.Path == "/healthz" && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			status := map[string]any{"status": "running"}
			a.mu.Lock()
			setupRequired := a.setupRequired
			a.mu.Unlock()
			if setupRequired {
				status["status"] = "setup_required"
				status["ready"] = false
			}
			_ = json.NewEncoder(w).Encode(status)
			return
		}
		if r.URL.Path == "/access" || r.URL.Path == "/ui/access.mjs" || r.URL.Path == "/ui/session.mjs" || r.URL.Path == "/ui/access.css" || r.URL.Path == "/ui/styles/controls.css" || r.URL.Path == "/ui/model-form.mjs" || r.URL.Path == "/ui/random-id.mjs" || r.URL.Path == "/ui/space-context.mjs" || r.URL.Path == "/ui/space-reset.mjs" || r.URL.Path == "/ui/styles/spaces.css" || r.URL.Path == "/ui/brand-mark.svg" {
			if r.URL.Path == "/access" {
				// Native SSO form navigation needs its same-origin proof.
				// no-referrer makes Chromium send Origin: null on that POST.
				w.Header().Set("Referrer-Policy", "same-origin")
			}
			web.Serve(w, r)
			return
		}
		if r.URL.Path == gitlabCallbackPath || r.URL.Path == gitlabCompletePath {
			subject := a.subject()
			if auth.users != nil {
				session, err := auth.authenticateBrowserHTTP(w, r)
				if err != nil {
					writeAuthenticationError(w, err)
					return
				}
				subject = session.Subject
			}
			r, err := a.bindSpaceRequest(r, subject)
			if err != nil {
				accessError(w, 403, "identity_mismatch")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/v1/access/login" {
			if r.Method != "POST" || r.Header.Get("Origin") != origin {
				accessError(w, 403, "invalid_origin")
				return
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			if a.disabled {
				accessError(w, 400, "login_disabled")
				return
			}
			now := a.now()
			if now.Before(a.retryAfter) {
				accessError(w, 429, "login_rate_limited")
				return
			}
			var input struct {
				Token string `json:"token"`
			}
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			dec.DisallowUnknownFields()
			if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
				accessError(w, 400, "invalid_request")
				return
			}
			hash := sha256.Sum256([]byte(input.Token))
			input.Token = ""
			if subtle.ConstantTimeCompare(hash[:], a.admin[:]) != 1 {
				a.attempts++
				if a.attempts >= 5 {
					a.retryAfter = now.Add(time.Minute)
					a.attempts = 0
				}
				accessError(w, 401, "unauthorized")
				return
			}
			if err := a.authorizeSubject(a.subject()); err != nil {
				accessError(w, 403, "identity_mismatch")
				return
			}
			a.attempts = 0
			sessions := maps.Clone(a.sessions)
			for key, s := range sessions {
				if !now.Before(s.expires) {
					delete(sessions, key)
				}
			}
			if len(sessions) >= maxAccessSessions {
				var oldest [32]byte
				var expiry time.Time
				for key, s := range sessions {
					if expiry.IsZero() || s.expires.Before(expiry) {
						oldest, expiry = key, s.expires
					}
				}
				delete(sessions, oldest)
			}
			token, err := randomAccessToken()
			if err != nil {
				accessError(w, 500, "unavailable")
				return
			}
			sessions[sha256.Sum256([]byte(token))] = accessSession{expires: now.Add(accessSessionLifetime), Subject: a.subject()}
			if err := a.replaceSessions(sessions); err != nil {
				accessError(w, 500, "session_unavailable")
				return
			}
			http.SetCookie(w, a.cookie(token, int(accessSessionLifetime/time.Second)))
			a.writeSession(w)
			return
		}
		header := r.Header.Get("Authorization")
		var binding *executionBinding
		authenticated := false
		subject := a.subject()
		cookieAuth := header == ""
		if header != "" {
			token, found := strings.CutPrefix(header, "Bearer ")
			if found && validAdminToken(token) {
				hash := sha256.Sum256([]byte(token))
				auth.mu.Lock()
				authenticated = auth.users == nil && subtle.ConstantTimeCompare(hash[:], auth.admin[:]) == 1
				if auth.disabled {
					authenticated = false
				}
				if !authenticated {
					auth.mu.Unlock()
					a.mu.Lock()
					b, exists := a.executions[hash]
					a.mu.Unlock()
					auth.mu.Lock()
					if exists {
						binding = &b
						authenticated = true
					}
				}
				auth.mu.Unlock()
			}
		} else if c, err := r.Cookie(a.cookieName()); err == nil {
			if auth.renewal != nil {
				session, err := auth.authenticateBrowserHTTP(w, r)
				if err != nil {
					writeAuthenticationError(w, err)
					return
				}
				subject, authenticated = session.Subject, true
			} else {
				hash := sha256.Sum256([]byte(c.Value))
				auth.mu.Lock()
				s, exists := auth.sessions[hash]
				authenticated = exists && auth.now().Before(s.expires)
				subject = s.Subject
				auth.mu.Unlock()
			}
		}
		if r.Context().Err() != nil {
			return
		}
		auth.mu.Lock()
		if auth.disabled && header == "" {
			subject = a.subject()
			authenticated = true
		}
		auth.mu.Unlock()
		if !authenticated {
			if r.URL.Path == "/" && r.Method == "GET" {
				http.Redirect(w, r, "/access", http.StatusSeeOther)
				return
			}
			if auth.renewal != nil && cookieAuth {
				accessError(w, 401, "login_required")
			} else {
				accessError(w, 401, "unauthorized")
			}
			return
		}
		if origin == auth.executionOrigin && auth.executionOrigin != "" && binding == nil {
			accessError(w, 403, "execution_credential_required")
			return
		}
		if binding != nil {
			subject = binding.Subject
		}
		if binding != nil && binding.SpaceContext != a.space {
			accessError(w, 403, "identity_mismatch")
			return
		}
		var scopeErr error
		r, scopeErr = a.bindSpaceRequest(r, subject)
		if scopeErr != nil {
			accessError(w, 403, "identity_mismatch")
			return
		}

		if binding == nil && r.URL.Query().Get("caller_id") != "" && r.URL.Query().Get("caller_id") != requestOperator(r) {
			accessError(w, 403, "identity_mismatch")
			return
		}
		if cookieAuth && r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != origin {
			accessError(w, 403, "invalid_origin")
			return
		}
		if a.spaces != nil && (r.URL.Path == "/v1/spaces" || strings.HasPrefix(r.URL.Path, "/v1/spaces/")) {
			if binding != nil {
				accessError(w, 403, "forbidden")
				return
			}
			a.spaces.serveDirectory(w, r)
			return
		}
		if a.spaces == nil && r.URL.Path == "/v1/spaces" {
			w.Header().Set("Content-Type", "application/json")
			if binding != nil || r.Method != http.MethodGet {
				accessError(w, 403, "forbidden")
				return
			}
			_ = json.NewEncoder(w).Encode(spaceDirectory{1, a.space.SpaceID, []SpaceRecord{a.identity.Space}})
			return
		}
		if r.URL.Path == "/v1/access/session" && r.Method == "GET" && binding == nil {
			auth.mu.Lock()
			a.writeSubjectSession(w, subject)
			auth.mu.Unlock()
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/access/settings") && binding == nil {
			a.serveSettings(w, r)
			return
		}
		if r.URL.Path == "/v1/access/logout" && r.Method == "POST" && binding == nil {
			if c, err := r.Cookie(a.cookieName()); err == nil {
				auth.mu.Lock()
				sessions := maps.Clone(auth.sessions)
				delete(sessions, sha256.Sum256([]byte(c.Value)))
				err := auth.replaceSessions(sessions)
				auth.mu.Unlock()
				if err != nil {
					accessError(w, 500, "session_unavailable")
					return
				}
			}
			http.SetCookie(w, a.cookie("", -1))
			writeAccessSession(w, "")
			return
		}
		if binding != nil && (r.Method != "POST" || !executionPath(r.URL.Path)) {
			accessError(w, 403, "forbidden")
			return
		}
		if r.Method == "POST" && r.URL.Path == "/v1/attachments/upload" {
			// Uploads carry their human identity in the query and stream multipart
			// bytes directly to the Room handler. Never feed them to the JSON
			// envelope inspector below. The Room revalidates the full service
			// identity, operator and attachment ID before accepting file bytes.
			callers := r.URL.Query()["caller_id"]
			if binding != nil || len(callers) != 1 || callers[0] != requestOperator(r) {
				accessError(w, 403, "identity_mismatch")
				return
			}
		} else if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1/setup/model" {
			// Inspect the existing identity envelope, then replay the exact bytes to the
			// original handler. tools/run continues streaming stdin after its JSON header.
			if r.URL.Path == "/v1/tools/run" {
				_ = http.NewResponseController(w).EnableFullDuplex()
			}
			var captured bytes.Buffer
			dec := json.NewDecoder(io.TeeReader(io.LimitReader(r.Body, 1<<20), &captured))
			var envelope struct {
				ServiceInfo
				Caller   Caller `json:"caller"`
				Provider string `json:"provider"`
				Input    struct {
					Provider string `json:"provider"`
				} `json:"input"`
				UserID  string `json:"user_id"`
				SpaceID string `json:"space_id"`
			}
			if dec.Decode(&envelope) != nil {
				accessError(w, 400, "invalid_request")
				return
			}
			if a.unavailableSpacePlatform(envelope.Provider) || a.unavailableSpacePlatform(envelope.Input.Provider) {
				accessError(w, 403, "space_capability_unavailable")
				return
			}
			if (envelope.UserID != "" && envelope.UserID != subject.UserID) || (envelope.SpaceID != "" && envelope.SpaceID != a.space.SpaceID) {
				accessError(w, 403, "identity_mismatch")
				return
			}
			if binding == nil {
				if envelope.Caller.Kind != "human" || envelope.Caller.ID != requestOperator(r) || envelope.Caller.MembershipID != "" {
					accessError(w, 403, "identity_mismatch")
					return
				}
			} else if envelope.ServiceInfo != binding.ServiceInfo || envelope.Caller != binding.Caller {
				accessError(w, 403, "identity_mismatch")
				return
			}
			r.Body = &replayedAccessBody{Reader: io.MultiReader(bytes.NewReader(captured.Bytes()), r.Body), closer: r.Body}
		}
		if binding != nil {
			r = r.WithContext(context.WithValue(r.Context(), executionContextKey{}, *binding))
		}
		next.ServeHTTP(w, r)
	})
}

type replayedAccessBody struct {
	io.Reader
	closer io.Closer
}

func (r *replayedAccessBody) Close() error { return r.closer.Close() }

// Workspace directory browsing is human-only, including on loopback. Never
// add /v1/workspace/directories to these execution credential capabilities.
func executionPath(path string) bool {
	if agentRuntimePath(path) {
		return true
	}
	switch path {
	case "/v1/inputs", "/v1/tools/list", "/v1/tools/run", "/v1/tools/skill", "/v1/skills/usage",
		"/v1/actions/execute", "/v1/actions/resolve", "/v1/actions/read", "/v1/actions/list",
		"/v1/platform/lark/messages", "/v1/platform/lark/message", "/v1/platform/lark/target",
		"/v1/platforms/list", "/v1/platforms/skill", "/v1/platforms/chats", "/v1/platforms/projects",
		"/v1/platforms/connect/start", "/v1/platforms/connect/authorize", "/v1/platforms/connect/status", "/v1/platforms/connect/cancel", "/v1/platforms/connect/notify", "/v1/platforms/connect/acknowledge",
		"/v1/platforms/selection/start", "/v1/platforms/selection/update", "/v1/platforms/selection/status", "/v1/platforms/selection/cancel", "/v1/platforms/selection/notify", "/v1/platforms/selection/acknowledge",
		"/v1/platforms/member/get", "/v1/platforms/member/notifications", "/v1/platforms/member/tool", "/v1/platforms/member/status":
		return true
	}
	return false
}

func accessError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "code": code})
}
func writeAccessSession(w http.ResponseWriter, operator string, installation ...string) {
	w.Header().Set("Content-Type", "application/json")
	result := map[string]string{"operator": operator}
	if len(installation) > 0 {
		result["installation_id"] = installation[0]
	}
	_ = json.NewEncoder(w).Encode(result)
}
func requestOperator(r *http.Request) string {
	if scope, ok := r.Context().Value(spaceContextKey{}).(authorizedSpace); ok {
		return scope.Subject.UserID
	}
	return ""
}

// Only this explicit deployment-machine command creates credentials. Start never
// regenerates a missing token or changes an existing operator identity.
func runAccessInit(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("access init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "private config.toml path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return ErrRequest
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return err
	}
	lock, err := lockBundleHome(filepath.Dir(abs))
	if err != nil {
		return err
	}
	defer unlockDirectory(lock)
	startup, loadErr := readStartupConfig(abs, false, false)
	if loadErr != nil {
		return loadErr
	}
	if startup.Deployment.Mode == "multi-user" {
		if err := prepareFirstAccess(startup); err != nil {
			return err
		}
		_, err := fmt.Fprintf(stdout, "Deployment access initialized. Private token file: %s\n", adminTokenPath(startup))
		return err
	}
	data, _, err := readSettingsFile(abs)
	if err != nil {
		return err
	}
	values := map[string]any{}
	if len(data) > 0 {
		if toml.Unmarshal(data, &values) != nil {
			return errors.New("invalid config")
		}
	}
	section, _ := values["access"].(map[string]any)
	if section == nil {
		section = map[string]any{}
		values["access"] = section
	}
	section["login_enabled"] = true
	operator, _ := section["operator_id"].(string)
	if operator == "" {
		id, err := newOpaqueID()
		if err != nil {
			return err
		}
		section["operator_id"] = "owner_" + id
	}
	savedOperator, _ := section["operator_id"].(string)
	if !validOperatorID(savedOperator) {
		return errors.New("invalid access.operator_id; preserve or repair the fixed owner ID")
	}
	name, _ := section["admin_token_file"].(string)
	if name == "" {
		name = "auth/admin-token"
		section["admin_token_file"] = name
	}
	tokenPath := configPath(filepath.Dir(abs), name)
	if err = os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
		return err
	}
	token, err := randomAccessToken()
	if err != nil {
		return err
	}
	if err = installMissing(tokenPath, []byte(token+"\n")); err != nil {
		return err
	}
	if _, err = readAdminToken(tokenPath); err != nil {
		return err
	}
	next, err := toml.Marshal(values)
	if err != nil {
		return err
	}
	if err = writePrivateConfig(abs, data, next); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Access initialized. Private token file: %s\n", tokenPath)
	return err
}
func writePrivateConfig(path string, previous, next []byte) error {
	source, file, err := readSettingsFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(source, previous) {
		return ErrConflict
	}
	document := &configDocument{path: path, source: source, file: file, ops: defaultSnapshotOps(), private: true}
	document.mu.Lock()
	defer document.mu.Unlock()
	return document.commitLocked(func([]byte) ([]byte, func(), error) { return next, func() {}, nil })
}

func validOperatorID(value string) bool {
	if !strings.HasPrefix(value, "owner_") || len(value) != 38 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "owner_"))
	return err == nil
}
