package room

import (
	"encoding/json"
	"io"
	"net/http"
)

type memberReasoningInput struct {
	MemberIdentity
	Caller                   Caller `json:"caller"`
	ServiceEpoch             string `json:"service_epoch"`
	ExpectedVersion          uint64 `json:"expected_version"`
	ExpectedSelectionVersion uint64 `json:"expected_selection_version"`
	ReasoningEffort          string `json:"reasoning_effort"`
}

// Only the future member selection is published. No actor, current generation,
// model catalog, validation receipt or execution gate is modified here.
func (c *modelController) saveMemberReasoning(input memberReasoningInput, owner string) (modelMemberView, error) {
	m := c.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if owner == "" || input.Caller.Kind != "human" || input.Caller.ID != owner || input.Caller.MembershipID != "" || input.ServiceEpoch != m.epoch {
		return modelMemberView{}, ErrIdentity
	}
	if m.closing {
		return modelMemberView{}, ErrClosing
	}
	if input.ExpectedVersion != c.service.snapshot().Version {
		return modelMemberView{}, ErrConflict
	}
	if m.roomBusyLocked(input.RoomIdentity) || m.config.models.gate(input.MemberIdentity) != "" {
		return modelMemberView{}, ErrBusy
	}
	refs, err := c.referencesLocked()
	if err != nil {
		return modelMemberView{}, err
	}
	for _, ref := range refs {
		if ref.MemberIdentity != input.MemberIdentity {
			continue
		}
		if ref.Version != input.ExpectedSelectionVersion {
			return modelMemberView{}, ErrConflict
		}
		selection := ref.Selection
		selection.ReasoningEffort = input.ReasoningEffort
		entry, err := m.config.models.catalog().resolve(selection)
		if err != nil {
			return modelMemberView{}, err
		}
		if _, err := selection.executionEntry(entry); err != nil {
			return modelMemberView{}, modelConfigDiagnostic(err)
		}
		if selection != ref.Selection {
			version := ref.Version + 1
			if version == 0 {
				return modelMemberView{}, ErrCapacity
			}
			if err := c.saveSelectionLocked(modelChangeMember{MemberIdentity: ref.MemberIdentity, Before: ref.Selection, Selection: selection, BeforeVersion: ref.Version, SelectionVersion: version}); err != nil {
				return modelMemberView{}, err
			}
			ref.Selection, ref.Version = selection, version
			m.updates.mark(uiUpdate{Topic: "models"})
		}
		return c.memberViewLocked(ref), nil
	}
	return modelMemberView{}, ErrIdentity
}

func (c *modelController) serveMemberReasoning(w http.ResponseWriter, r *http.Request) {
	var input memberReasoningInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	view, err := c.saveMemberReasoning(input, requestOperator(r))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(view)
}
