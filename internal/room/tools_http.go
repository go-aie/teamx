package room

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type toolFrame struct {
	Stream string `json:"stream"`
	Data   []byte `json:"data,omitempty"`
	Code   int    `json:"code,omitempty"`
}
type toolStream struct {
	mu     sync.Mutex
	w      http.ResponseWriter
	cancel context.CancelFunc
}
type toolStreamWriter struct {
	stream *toolStream
	name   string
}

func (w toolStreamWriter) Write(data []byte) (int, error) {
	w.stream.mu.Lock()
	defer w.stream.mu.Unlock()
	// Bound each frame while preserving arbitrary bytes (JSON base64).
	size := len(data)
	for len(data) > 0 {
		n := min(len(data), 32<<10)
		if err := json.NewEncoder(w.stream.w).Encode(toolFrame{Stream: w.name, Data: data[:n]}); err != nil {
			w.stream.cancel()
			return 0, err
		}
		if err := http.NewResponseController(w.stream.w).Flush(); err != nil {
			w.stream.cancel()
			return 0, err
		}
		data = data[n:]
	}
	return size, nil
}
func (m *roomManager) serveTools(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/tools/run":
		m.serveToolRun(w, r)
		return
	case "/v1/tools/list", "/v1/tools/skill":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
			writeAPIError(w, ErrIdentity)
			return
		}
		var req toolsRequest
		if !decodeToolRequest(w, r, &req) {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(req.Args) != 0 || r.URL.Path == "/v1/tools/list" && req.Tool != "" {
			writeAPIError(w, ErrRequest)
			return
		}
		if err := m.checkToolMemberLocked(r.Context(), req); err != nil {
			writeAPIError(w, err)
			return
		}
		f, err := m.readToolBindingsLocked(req.RoomIdentity)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		if r.URL.Path == "/v1/tools/skill" {
			for _, b := range f.Bindings {
				if b.Tool == req.Tool && b.AgentID == req.Caller.ID && b.MembershipID == req.Caller.MembershipID {
					d, err := m.toolDefinition(b.Tool)
					if err == nil {
						data, err := tool.ReadSkill(d)
						if err == nil {
							w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
							_, _ = w.Write(data)
							return
						}
					}
				}
			}
			writeAPIError(w, ErrRequest)
			return
		}
		selections, err := m.selectionsLocked(f, req.Caller)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(selections)
		return
	case "/v1/tools/bindings":
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		q := r.URL.Query()
		if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		f, err := m.readToolBindingsLocked(RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(struct {
			toolBindingsFile
			Catalog []tool.Definition `json:"catalog"`
		}{f, m.toolDefinitions()})
		return
	case "/v1/tools/bind":
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		var req toolBindingChange
		if !decodeToolRequest(w, r, &req) {
			return
		}
		m.mu.Lock()
		f, pending, err := m.changeToolBindingLocked(r.Context(), req)
		m.mu.Unlock()
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			if notification.Wait(ctx, pending) != nil {
				err = ErrBusy
			}
		}
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(f)
		return
	default:
		http.NotFound(w, r)
	}
}

func decodeToolRequest(w http.ResponseWriter, r *http.Request, out any) bool {
	data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || strictToolJSON(data, out) != nil {
		writeAPIError(w, ErrRequest)
		return false
	}
	return true
}
func (m *roomManager) serveToolRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	// One bounded JSON line contains only identity, executable name and argv;
	// everything after it is raw stdin. No credential travels over this protocol.
	reader := bufio.NewReaderSize(r.Body, 64<<10)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	var req toolsRequest
	if err = strictToolJSON(line, &req); err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.EnableFullDuplex()
	_ = controller.SetReadDeadline(time.Time{})
	_ = controller.SetWriteDeadline(time.Time{})
	m.mu.Lock()
	process, call, ctx, err := m.prepareToolLocked(r.Context(), req)
	m.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer m.finishPlatformCall(call)
	ioDone := make(chan struct{})
	ioStopped := make(chan struct{})
	defer func() { close(ioDone); <-ioStopped }()
	go func() {
		defer close(ioStopped)
		select {
		case <-ctx.Done():
			// A disconnected/slow reader must not prevent disabling or shutdown.
			_ = controller.SetReadDeadline(time.Now())
			_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
		case <-ioDone:
		}
	}()

	// A file descriptor avoids exec's input-copy goroutine waiting forever when
	// the executable exits without consuming interactive stdin.
	input, feed, err := os.Pipe()
	if err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	defer input.Close()
	defer feed.Close()
	go func() { _, _ = io.Copy(feed, reader); _ = feed.Close() }()
	stream := &toolStream{w: w, cancel: call.cancel}
	process.Input = input
	process.Output = toolStreamWriter{stream, "stdout"}
	process.Error = toolStreamWriter{stream, "stderr"}
	w.Header().Set("Content-Type", "application/x-ndjson")
	// A command may finish before request stdin reaches EOF. Close this duplex
	// connection so a read deadline or unread upload cannot affect a later call.
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	_ = controller.Flush()
	code := process.Run(ctx, req.Args)
	_ = input.Close()
	// Stop the request upload even when stdin was never read by the tool.
	_ = controller.SetReadDeadline(time.Now())
	_ = r.Body.Close()
	stream.mu.Lock()
	_ = json.NewEncoder(w).Encode(toolFrame{Stream: "exit", Code: code})
	_ = controller.Flush()
	stream.mu.Unlock()
}
