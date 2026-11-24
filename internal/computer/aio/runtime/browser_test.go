package runtime

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBrowserShutdownUsesOnlyFixedLocalManagementCommand(t *testing.T) {
	received := make(chan string, 1)
	var origin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://%s/devtools/browser/test"}`, origin)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		digest := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(digest[:]))
		_ = rw.Flush()
		header := make([]byte, 2)
		if _, err = io.ReadFull(rw, header); err != nil {
			t.Error(err)
			return
		}
		if header[0] != 0x81 || header[1]&0x80 == 0 {
			t.Error("management client must mask text frame")
			return
		}
		mask := make([]byte, 4)
		if _, err = io.ReadFull(rw, mask); err != nil {
			t.Error(err)
			return
		}
		payload := make([]byte, int(header[1]&0x7f))
		if _, err = io.ReadFull(rw, payload); err != nil {
			t.Error(err)
			return
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		received <- string(payload)
	}))
	defer server.Close()
	origin = strings.TrimPrefix(server.URL, "http://")
	_, portText, _ := net.SplitHostPort(origin)
	port, _ := strconv.Atoi(portText)
	if err := closeBrowser(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != `{"id":1,"method":"Browser.close"}` {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing orderly shutdown command")
	}
}
