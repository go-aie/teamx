package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// Bounded reads use explicit per-Membership binding and settled call lifetimes.
type larkReadRequest struct {
	ServiceInfo
	Caller    Caller                `json:"caller"`
	Messages  *lark.MessagesRequest `json:"messages,omitempty"`
	MessageID string                `json:"message_id,omitempty"`
	Target    *larkTargetRequest    `json:"target,omitempty"`
}

func (m *roomManager) checkLarkReadLocked(ctx context.Context, req toolsRequest, expected connection.Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkToolMemberLocked(ctx, req); err != nil {
		return err
	}
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return err
	}
	for _, b := range f.Bindings {
		if b.Tool == "lark" && b.AgentID == req.Caller.ID && b.MembershipID == req.Caller.MembershipID && b.ConnectionID == expected.ConnectionID && b.ConnectionGeneration == expected.Generation {
			current, _, err := m.resolvePlatformAccountLocked(req.RoomIdentity, b.ConnectionID, "lark")
			if err != nil {
				return err
			}
			if current.Identity != expected.Identity || current.ConnectionVersion != expected.ConnectionVersion || current.CredentialVersion != expected.CredentialVersion || current.CredentialFingerprint != expected.CredentialFingerprint {
				return ErrConflict
			}
			return nil
		}
	}
	return ErrConflict
}
func (m *roomManager) readLark(ctx context.Context, r larkReadRequest) (any, error) {
	if r.Target != nil {
		return m.readLarkTarget(ctx, r)
	}
	req := toolsRequest{ServiceInfo: r.ServiceInfo, Caller: r.Caller, Tool: "lark"}
	m.mu.Lock()
	a, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	defer m.finishPlatformCall(call)
	if a.account.CLIProfile == nil || !lark.ValidAccount(a.account.Identity, *a.account.CLIProfile) {
		return nil, ErrConflict
	}
	client, err := m.larkClients.New(*a.account.CLIProfile)
	if err != nil {
		return nil, err
	}
	var result any
	if r.Messages != nil {
		result, err = client.Messages(callCtx, a.account.Identity, *r.Messages)
	} else {
		result, err = client.Message(callCtx, a.account.Identity, r.MessageID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if checkErr := m.checkLarkReadLocked(callCtx, req, a.account); checkErr != nil {
		return nil, checkErr
	}
	if err != nil {
		if saveErr := m.recordLarkReadFailureLocked(a.account, err); saveErr != nil {
			return nil, saveErr
		}
		return nil, err
	}
	return result, nil
}

func (m *roomManager) recordLarkReadFailureLocked(account connection.Account, err error) error {
	switch lark.ErrorCode(err) {
	case "identity_mismatch", "credential_unavailable", "credential_rejected", "permission_denied", "cli_version_mismatch":
		// Preserve the binding, but require explicit connection verification before
		// admitting another read. Existing notification progress is preserved.
		ticket, beginErr := m.connections.BeginVerification(account.ConnectionID, account.ConnectionVersion)
		if beginErr != nil {
			return platformConnectionError(beginErr)
		}
		_, saveErr := m.connections.CompleteVerification(ticket, platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: lark.ErrorCode(err)})
		if saveErr != nil {
			return platformConnectionError(saveErr)
		}
	}
	return nil
}
func (m *roomManager) serveLarkRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req larkReadRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.Caller.Kind != "agent" || (r.URL.Path == "/v1/platform/lark/target" && (req.Target == nil || req.Messages != nil || req.MessageID != "")) || (r.URL.Path != "/v1/platform/lark/target" && req.Target != nil) || (r.URL.Path == "/v1/platform/lark/messages" && (req.Messages == nil || req.MessageID != "")) || (r.URL.Path == "/v1/platform/lark/message" && (req.Messages != nil || req.MessageID == "")) {
		writeAPIError(w, ErrRequest)
		return
	}
	result, err := m.readLark(r.Context(), req)
	if err != nil {
		writeLarkReadError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func writeLarkReadError(w http.ResponseWriter, err error) {
	var remote *lark.Error
	if errors.As(err, &remote) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		code := lark.ErrorCode(err)
		if code == "invalid_request" {
			writeAPIError(w, ErrRequest)
			return
		}
		status := http.StatusBadGateway
		if code == "timeout" {
			status = http.StatusGatewayTimeout
		}
		if code == "cancelled" {
			status = http.StatusRequestTimeout
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Code string `json:"code"`
		}{lark.ErrorCode(err)})
		return
	}
	writeAPIError(w, err)
}
