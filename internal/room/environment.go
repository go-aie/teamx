package room

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// The viewer is a deployment capability, independent of the Agent's MCP URL.
// All paths refer to the environment gateway, never to the bare aiod process.
type environmentConfig struct {
	BaseURL       string          `json:"base_url"`
	Headers       []dsh.MCPHeader `json:"headers,omitempty"`
	VNCPath       string          `json:"vnc_path,omitempty"`
	WebSocketPath string          `json:"websocket_path,omitempty"`
}

func (c *environmentConfig) valid() bool {
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	if c.VNCPath == "" {
		c.VNCPath = "/vnc/"
	}
	if c.WebSocketPath == "" {
		c.WebSocketPath = "/websockify"
	}
	for _, p := range []string{c.VNCPath, c.WebSocketPath} {
		if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "?%#\\\r\n") || path.Clean(p) != strings.TrimSuffix(p, "/") {
			return false
		}
	}
	if !strings.HasSuffix(c.VNCPath, "/") {
		return false
	}
	for _, h := range c.Headers {
		switch http.CanonicalHeaderKey(h.Name) {
		case "Host", "Cookie", "Connection", "Upgrade", "Origin", "Content-Length", "Transfer-Encoding":
			return false
		}
		if strings.HasPrefix(strings.ToLower(h.Name), "sec-") || strings.HasPrefix(strings.ToLower(h.Name), "x-forwarded-") {
			return false
		}
	}
	return dsh.CheckConfig(dsh.Options{BinaryPath: "/environment-config-validation", Model: launcherCheckModel(), MCPServers: []dsh.MCPHTTPServer{{Type: "http", Name: "environment", URL: c.BaseURL, Headers: c.Headers}}}) == nil
}

var environmentStreams = make(chan struct{}, 8)
var environmentTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 5 * time.Second
	return t
}()

func environmentID(epoch string, id MemberIdentity) string {
	b, _ := json.Marshal(struct {
		Epoch string
		MemberIdentity
	}{epoch, id})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (m *roomManager) environmentMemberCurrent(id MemberIdentity) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.workspaceRoom(id.RoomIdentity)
	if m.closing || err != nil || entry.Status != "ready" {
		return false
	}
	config, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return false
	}
	for _, member := range config.Members {
		if member.AgentID == id.AgentID && member.MembershipID == id.MembershipID {
			return true
		}
	}
	return false
}

// Recheck existing browser credentials too: logging out or replacing a member
// must close a long-lived viewer, not just deny its next connection.
func environmentAccessCurrent(r *http.Request) bool {
	a, ok := r.Context().Value(accessContextKey{}).(*serverAccess)
	if !ok {
		return true
	} // In-process handlers; production is access-wrapped.
	a.mu.Lock()
	defer a.mu.Unlock()
	if header := r.Header.Get("Authorization"); header != "" {
		token, found := strings.CutPrefix(header, "Bearer ")
		hash := sha256.Sum256([]byte(token))
		return found && !a.disabled && subtle.ConstantTimeCompare(hash[:], a.admin[:]) == 1
	}
	if a.disabled {
		return true
	}
	cookie, err := r.Cookie(a.cookieName())
	if err != nil {
		return false
	}
	s, exists := a.sessions[sha256.Sum256([]byte(cookie.Value))]
	return exists && a.now().Before(s.expires)
}

func environmentError(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "private, no-store")
	http.Error(w, "环境画面暂不可用，请重新连接。", code)
}

func (m *roomManager) serveEnvironment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && !(r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/control")) {
		w.Header().Set("Allow", "GET")
		environmentError(w, 405)
		return
	}
	if _, execution := r.Context().Value(executionContextKey{}).(executionBinding); execution {
		environmentError(w, 403)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/v1/environments" {
		q := r.URL.Query()
		id := RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
		if q.Get("service_epoch") != m.epoch || q.Get("caller_id") != requestOperator(r) {
			writeAPIError(w, ErrIdentity)
			return
		}
		type view struct {
			ID        string `json:"id"`
			Label     string `json:"label"`
			ViewerURL string `json:"viewer_url"`
		}
		views := []view{}
		for member, binding := range m.config.agentMCP {
			if binding.Environment == nil || member.RoomIdentity != id || !m.environmentMemberCurrent(member) {
				continue
			}
			key := environmentID(m.epoch, member)
			label := m.agentNames.name(member)
			if label == "" {
				label = member.AgentID
			}
			views = append(views, view{key, label, "/v1/environment/" + key + "/viewer"})
		}
		slices.SortFunc(views, func(a, b view) int { return strings.Compare(a.ID, b.ID) })
		_ = json.NewEncoder(w).Encode(struct {
			Environments []view `json:"environments"`
		}{views})
		return
	}
	key, asset, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/environment/"), "/")
	if !ok {
		environmentError(w, 404)
		return
	}
	var member MemberIdentity
	var config *environmentConfig
	for id, binding := range m.config.agentMCP {
		if binding.Environment != nil && environmentID(m.epoch, id) == key {
			member, config = id, binding.Environment
			break
		}
	}
	if config == nil || !m.environmentMemberCurrent(member) {
		environmentError(w, 404)
		return
	}
	if asset == "control" {
		m.serveEnvironmentControl(w, r, member)
		return
	}
	if asset == "viewer" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: blob:; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
		io.WriteString(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>环境画面</title><link rel="stylesheet" href="/ui/styles/environment-viewer.css"><body><div id="screen" aria-label="环境画面"></div><p id="status" role="status">正在连接环境…</p><button id="keyboard-toggle" type="button" hidden>键盘</button><input id="keyboard" type="password" autocomplete="off" aria-label="远端键盘输入" autocapitalize="off" spellcheck="false"><script type="module" src="/ui/environment-viewer.mjs"></script></body></html>`)
		return
	}
	upstreamPath := ""
	stream := asset == "websockify"
	controlID, clientID := r.URL.Query().Get("control_id"), r.URL.Query().Get("client_id")
	interactive := r.URL.Query().Has("control_id") || r.URL.Query().Has("client_id")
	if stream {
		if interactive && !m.environmentInputAllowed(member, controlID, clientID) {
			environmentError(w, 403)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Origin") == "" {
			environmentError(w, 400)
			return
		}
		select {
		case environmentStreams <- struct{}{}:
			defer func() { <-environmentStreams }()
		default:
			environmentError(w, 429)
			return
		}
		upstreamPath = config.WebSocketPath
	} else {
		file := strings.TrimPrefix(asset, "vnc/")
		if file == asset || !fs.ValidPath(file) || strings.ContainsAny(file, "%\\\x00\r\n") || path.Ext(file) != ".js" || !(strings.HasPrefix(file, "core/") || strings.HasPrefix(file, "vendor/")) {
			environmentError(w, 404)
			return
		}
		upstreamPath = config.VNCPath + file
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if stream && interactive {
		input, err := m.registerEnvironmentInput(member, controlID, clientID, cancel)
		if err != nil {
			environmentError(w, 403)
			return
		}
		defer m.releaseEnvironmentInput(member, controlID, input)
	}
	if stream {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if !m.environmentMemberCurrent(member) || !environmentAccessCurrent(r) || interactive && !m.environmentInputAllowed(member, controlID, clientID) {
						cancel()
						return
					}
				}
			}
		}()
	}
	target, _ := url.Parse(config.BaseURL)
	proxy := &httputil.ReverseProxy{
		Transport: environmentTransport,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.Out.URL.Scheme, p.Out.URL.Host, p.Out.Host = target.Scheme, target.Host, target.Host
			p.Out.URL.Path, p.Out.URL.RawPath, p.Out.URL.RawQuery = upstreamPath, "", ""
			// Never forward TeamX login cookies, bearer credentials or identity.
			h := http.Header{}
			if stream {
				for _, name := range []string{"Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Protocol"} {
					if v := p.In.Header.Values(name); len(v) > 0 {
						h[name] = v
					}
				}
				h.Set("Origin", target.Scheme+"://"+target.Host)
			}
			for _, credential := range config.Headers {
				h.Set(credential.Name, credential.Value)
			}
			p.Out.Header = h
		},
		ModifyResponse: func(res *http.Response) error {
			if stream && res.StatusCode != 101 || !stream && res.StatusCode != 200 {
				res.Body.Close()
				return errors.New("environment unavailable")
			}
			for name := range res.Header {
				if name != "Connection" && name != "Upgrade" && name != "Sec-Websocket-Accept" && name != "Sec-Websocket-Protocol" {
					res.Header.Del(name)
				}
			}
			if !stream {
				res.Header.Set("Content-Type", "text/javascript; charset=utf-8")
			}
			res.Header.Set("Cache-Control", "private, no-store")
			res.Header.Set("X-Content-Type-Options", "nosniff")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { environmentError(w, 502) },
	}
	w.Header().Del("Content-Type")
	proxy.ServeHTTP(w, r.WithContext(ctx))
}
