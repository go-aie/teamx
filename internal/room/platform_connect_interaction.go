package room

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

// The operation owns the durable interaction reference and original message
// association. Views never copy live authorization material into Room history.
type platformConnectInteractionView struct {
	MemberIdentity
	OperationID     string                        `json:"operation_id"`
	SourceSeq       uint64                        `json:"source_seq"`
	Name            string                        `json:"name"`
	Version         uint64                        `json:"version"`
	ResultVersion   uint64                        `json:"result_version"`
	Status          string                        `json:"status"`
	Stage           string                        `json:"stage"`
	ErrorCode       string                        `json:"error_code,omitempty"`
	TargetAvailable bool                          `json:"target_available"`
	AccountID       string                        `json:"account_id,omitempty"`
	Application     onboardingStep                `json:"application"`
	Authorization   onboardingStep                `json:"authorization"`
	Connection      onboardingStep                `json:"connection"`
	Interaction     platformConnectInteraction    `json:"interaction"`
	Continuation    platformOperationContinuation `json:"continuation"`
}

type platformConnectInteractionRequest struct {
	ServiceInfo
	Caller      Caller `json:"caller"`
	OperationID string `json:"operation_id,omitempty"`
	Version     uint64 `json:"version,omitempty"`
}

func (m *roomManager) platformConnectInteractionLocked(op platformConnectOperation, available bool) (platformConnectInteractionView, error) {
	continuation, err := m.connectContinuationLocked(op)
	if err != nil {
		return platformConnectInteractionView{}, err
	}
	if available {
		var err error
		op, err = m.publicPlatformConnectLocked(op)
		if err != nil {
			return platformConnectInteractionView{}, err
		}
	}
	return platformConnectInteractionView{
		MemberIdentity: op.MemberIdentity, OperationID: op.ID, SourceSeq: op.Source.Seq,
		Name: op.Input.Name, Version: op.Version, ResultVersion: op.ResultVersion,
		Status: op.Status, Stage: op.Stage, ErrorCode: op.ErrorCode, TargetAvailable: available,
		AccountID: op.Account.ID.Value, Application: op.Application, Authorization: op.Authorization,
		Connection: op.Connection, Interaction: op.Interaction, Continuation: continuation,
	}, nil
}

// Separate operator boundary; the Agent-only endpoints retain their checks.
// These controls only observe or explicitly cancel an existing operation. The
// server owns authorization, account verification and the single connection save.
func (m *roomManager) servePlatformConnectInteraction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req platformConnectInteractionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		writeAPIError(w, ErrClosing)
		return
	}
	if req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	svc, rt := m.runtimeServiceLocked(req.RoomIdentity)
	if svc == nil || req.ServiceInfo != svc.info {
		writeAPIError(w, ErrIdentity)
		return
	}
	if m.busy || rt.busy || rt.status != "ready" {
		writeAPIError(w, ErrBusy)
		return
	}
	snapshot, err := svc.store.LoadRoom()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	available := func(op platformConnectOperation) bool {
		for _, member := range snapshot.Members {
			if member.AgentID == op.AgentID && member.MembershipID == op.MembershipID {
				return true
			}
		}
		return false
	}
	action := strings.TrimPrefix(r.URL.Path, "/v1/platforms/interactions/")
	if action == "list" {
		if req.OperationID != "" || req.Version != 0 {
			writeAPIError(w, ErrRequest)
			return
		}
		ops, err := m.platformConnectOperationsLocked()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		views := []platformConnectInteractionView{}
		for _, op := range ops {
			if op.RoomIdentity != req.RoomIdentity || op.Source.Author.ID != req.Caller.ID {
				continue
			}
			view, err := m.platformConnectInteractionLocked(op, available(op))
			if err != nil {
				writeAPIError(w, err)
				return
			}
			views = append(views, view)
		}
		sort.Slice(views, func(i, j int) bool {
			if views[i].SourceSeq != views[j].SourceSeq {
				return views[i].SourceSeq < views[j].SourceSeq
			}
			return views[i].OperationID < views[j].OperationID
		})
		selections, err := m.platformSelectionsLocked()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		selectionViews := []platformSelectionView{}
		for _, s := range selections {
			if s.RoomIdentity != req.RoomIdentity || s.Source.Author.ID != req.Caller.ID {
				continue
			}
			view, err := m.selectionViewLocked(s)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			selectionViews = append(selectionViews, view)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Interactions []platformConnectInteractionView `json:"interactions"`
			Selections   []platformSelectionView          `json:"selections"`
		}{views, selectionViews})
		return
	}
	if action != "cancel" || req.Version == 0 {
		writeAPIError(w, ErrRequest)
		return
	}
	op, err := m.readPlatformConnectLocked(req.OperationID)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if op.RoomIdentity != req.RoomIdentity || op.Source.Author.ID != req.Caller.ID {
		writeAPIError(w, ErrIdentity)
		return
	}
	// The original human may stop this exact operation even after its member
	// leaves. No authorization material or effects transfer to a replacement,
	// and the shared native authorization reservation can still be released.
	// A repeated cancellation returns its receipt even after version advancement.
	if op.Version != req.Version && !op.CancelRequested {
		writeAPIError(w, ErrConflict)
		return
	}
	op, err = m.cancelPlatformConnectLocked(r.Context(), op)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	view, err := m.platformConnectInteractionLocked(op, available(op))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(view)
}
