package runtime

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// closeBrowser requests Chromium's orderly shutdown so recently written cookies
// are flushed. SIGTERM alone does not guarantee that. This fixed management
// command is private to Stop; it is not an Agent tool or an arbitrary CDP proxy.
// Stop still verifies the full UID/port range and kills stragglers afterward.
func closeBrowser(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	origin := fmt.Sprintf("127.0.0.1:%d", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+origin+"/json/version", nil)
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	_ = res.Body.Close()
	if err != nil {
		return err
	}
	var version struct {
		URL string `json:"webSocketDebuggerUrl"`
	}
	if res.StatusCode != 200 || len(body) > 65536 || json.Unmarshal(body, &version) != nil {
		return fmt.Errorf("invalid local browser version")
	}
	u, err := url.Parse(version.URL)
	if err != nil || u.Scheme != "ws" || u.Host != origin || !strings.HasPrefix(u.Path, "/devtools/browser/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("unexpected local browser control endpoint")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", origin)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	upgrade, err := http.NewRequest(http.MethodGet, version.URL, nil)
	if err != nil {
		return err
	}
	upgrade.Header.Set("Connection", "Upgrade")
	upgrade.Header.Set("Upgrade", "websocket")
	upgrade.Header.Set("Sec-WebSocket-Version", "13")
	upgrade.Header.Set("Sec-WebSocket-Key", key)
	if err = upgrade.Write(conn); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReaderSize(conn, 4096), upgrade)
	if err != nil {
		return err
	}
	expected := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if response.StatusCode != 101 || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") || response.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(expected[:]) {
		return fmt.Errorf("browser control upgrade rejected")
	}
	// One fixed short masked text frame, no general WebSocket/CDP surface.
	payload := []byte(`{"id":1,"method":"Browser.close"}`)
	var mask [4]byte
	if _, err = rand.Read(mask[:]); err != nil {
		return err
	}
	frame := append([]byte{0x81, 0x80 | byte(len(payload))}, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	_, err = conn.Write(frame)
	return err
}
