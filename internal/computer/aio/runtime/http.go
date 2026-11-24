package runtime

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio/protocol"
)

// Handler is the authenticated management entrance. It deliberately exposes no
// generic proxy, process command, endpoint override, or raw MCP route.
func Handler(e *Engine, token string) (http.Handler, error) {
	if len(token) < 32 || len(token) > 4096 || strings.TrimSpace(token) != token {
		return nil, computer.Fail(computer.Invalid, "management token must contain at least 32 characters")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req protocol.Request
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, computer.RequestLimit))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, computer.Fail(computer.Invalid, "invalid or oversized management request"))
			return
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			writeError(w, computer.Fail(computer.Invalid, "trailing request data"))
			return
		}
		var result any
		var err error
		switch r.URL.Path {
		case "/inspect":
			result, err = e.Inspect(r.Context())
		case "/claim":
			result, err = e.Claim(r.Context(), req.Claim)
		case "/screens":
			result, err = e.Screens(r.Context(), req.Claim.Owner)
		case "/member/admission":
			err = e.SetMemberAdmission(r.Context(), req.Admission)
		case "/ensure":
			var screen computer.Screen
			screen, err = e.EnsureScreen(r.Context(), req.Binding)
			if err == nil {
				result = screen.Info()
			}
		case "/screen/tools", "/screen/call", "/screen/operation", "/screen/drain", "/screen/stop", "/screen/control", "/screen/view", "/screen/asset":
			var s computer.Screen
			s, err = e.Resolve(req.Screen)
			if err == nil {
				switch r.URL.Path {
				case "/screen/control":
					result, err = s.(computer.DisplayScreen).Control(r.Context(), req.Control)
				case "/screen/asset":
					result, err = s.(computer.DisplayScreen).ViewAsset(r.Context(), req.Asset)
				case "/screen/view":
					if r.Header.Get("Upgrade") != "teamx-rfb" {
						err = computer.Fail(computer.Invalid, "RFB upgrade required")
						break
					}
					var stream io.ReadWriteCloser
					stream, err = s.(computer.DisplayScreen).View(r.Context(), req.View)
					if err != nil {
						break
					}
					defer stream.Close()
					h, ok := w.(http.Hijacker)
					if !ok {
						err = computer.Fail(computer.Unavailable, "streaming unavailable")
						break
					}
					conn, rw, hijackErr := h.Hijack()
					if hijackErr != nil {
						return
					}
					defer conn.Close()
					_, _ = fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: teamx-rfb\r\n\r\n")
					if rw.Flush() != nil {
						return
					}
					done := make(chan struct{})
					go func() { defer close(done); _, _ = io.Copy(stream, rw); stream.Close() }()
					_, _ = io.Copy(conn, stream)
					conn.Close()
					stream.Close()
					<-done
					return
				case "/screen/tools":
					result, err = s.Tools(r.Context())
				case "/screen/call":
					result, err = s.Call(r.Context(), req.Call)
				case "/screen/operation":
					result, err = s.Operation(r.Context(), req.OperationID)
				case "/screen/drain":
					err = s.Drain(r.Context(), req.Scope)
				case "/screen/stop":
					err = s.Stop(r.Context())
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		raw, err := json.Marshal(result)
		if err != nil || len(raw) > computer.ResponseLimit+65536 {
			writeError(w, computer.Fail(computer.Unavailable, "management response exceeds bounded transport"))
			return
		}
		_, _ = w.Write(raw)
	}), nil
}
func writeError(w http.ResponseWriter, err error) {
	var ce *computer.Error
	if !errors.As(err, &ce) {
		log.Printf("Computer management operation failed: %v", err)
		ce = &computer.Error{Code: computer.Unavailable, Message: "Runtime operation failed; inspect server diagnostics and reconcile before retrying mutations"}
	}
	status := http.StatusConflict
	if ce.Code == computer.Invalid {
		status = http.StatusBadRequest
	}
	if ce.Code == computer.Unavailable {
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ce)
}
