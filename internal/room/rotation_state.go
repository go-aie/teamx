package room

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const rotationVersion = 1

// The last confirmed phase is retained on failure. Directory names are the
// generation identities; neither a retry nor recovery allocates another target.
type rotationRecord struct {
	Version int `json:"version"`
	MemberIdentity
	OperationID string `json:"operation_id"`
	Reason      string `json:"reason"`
	From        string `json:"from_generation"`
	To          string `json:"to_generation"`
	BaseUsed    uint64 `json:"base_used"`
	Phase       string `json:"phase"`
	Cancel      bool   `json:"cancel"`
	Resume      bool   `json:"resume"`
	Replenish   bool   `json:"replenish"`
	Error       string `json:"error,omitempty"`
	FailedStep  string `json:"failed_step,omitempty"`
}

func validSessionGeneration(g string) bool {
	const base = "session.json.dsh"
	return g == base || len(g) == len(base)+33 && g[:len(base)+1] == base+"-" && validRestartID(g[len(base)+1:])
}

func (r rotationRecord) validate(id MemberIdentity) error {
	if r.Version != rotationVersion || r.MemberIdentity != id || !validRestartID(r.OperationID) || !validText(r.Reason) || len(r.Reason) > 256 || !validSessionGeneration(r.From) || !validSessionGeneration(r.To) || r.From == r.To {
		return fmt.Errorf("%w: rotation identity or target", ErrInvalid)
	}
	switch r.Phase {
	case "prepared", "old_closed", "completed":
	default:
		return fmt.Errorf("%w: rotation phase", ErrInvalid)
	}
	return nil
}
func (s *Store) loadRotation(id MemberIdentity) (*rotationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _, err := s.memberPath(id, "rotation.json", false)
	if err != nil {
		return nil, err
	}
	var r rotationRecord
	if err := readSnapshot(path, &r, maxPrivateFileBytes); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var fields map[string]any
	if err := readSnapshot(path, &fields, maxPrivateFileBytes); err != nil {
		return nil, err
	}
	for _, key := range []string{"base_used", "cancel", "resume", "replenish"} {
		if value, ok := fields[key]; !ok || value == nil {
			return nil, fmt.Errorf("%w: missing rotation %s", ErrInvalid, key)
		}
	}
	if err := r.validate(id); err != nil {
		return nil, err
	}
	return &r, nil
}
func (s *Store) saveRotation(r rotationRecord) error {
	if err := r.validate(r.MemberIdentity); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _, err := s.memberPath(r.MemberIdentity, "rotation.json", true)
	if err != nil {
		return err
	}
	return s.save(path, r, maxPrivateFileBytes)
}

// Reconcile only confirmed local facts before any Actor can admit execution.
// A prepared operation still needs durable old-resource settlement evidence.
func reconcileRotation(store *Store, opts SessionOptions, r *rotationRecord, state *memberScheduling) error {
	if err := r.validate(opts.MemberIdentity); err != nil {
		return err
	}
	if state.MemberIdentity != opts.MemberIdentity || (state.Version != formatVersion && state.Version != schedulingVersion) || state.SessionStartUsed > state.Used {
		return ErrIdentity
	}
	b := dshBackend{store: store}
	ref, _, err := b.reference(opts)
	if err != nil {
		return err
	}
	generation := ref.Directory
	if generation == "" {
		generation = filepath.Base(dshStateDir(opts.StatePath))
	}
	if r.Phase == "completed" {
		if generation != r.To || state.Version != schedulingVersion || state.SessionGeneration != r.To || state.SessionStartUsed != r.BaseUsed || state.Used < r.BaseUsed {
			return fmt.Errorf("%w: completed rotation baseline", ErrConflict)
		}
		return nil
	}
	if state.Used != r.BaseUsed || (state.SessionGeneration != "" && state.SessionGeneration != r.From && state.SessionGeneration != r.To) || (generation != r.From && generation != r.To) {
		return fmt.Errorf("%w: rotation reference or count changed", ErrConflict)
	}
	if ref.Directory == "" && r.BaseUsed > 0 {
		return fmt.Errorf("%w: missing old Session reference", ErrConflict)
	}
	if r.Phase == "prepared" {
		if generation != r.From {
			return fmt.Errorf("%w: prepared rotation already switched", ErrConflict)
		}
		if err := b.checkRotationClosed(opts, r.From, false); err != nil {
			return err
		}
		r.Phase, r.Error, r.FailedStep = "old_closed", "", ""
		if err := store.saveRotation(*r); err != nil {
			return err
		}
	}
	// A target that has started before completion is a conflict, even if it has
	// subsequently stopped. Pending references additionally verify no directory.
	if generation == r.To && !ref.Pending {
		return fmt.Errorf("%w: incomplete rotation target has execution evidence", ErrConflict)
	}
	if state.SessionGeneration == r.To && (state.SessionStartUsed != r.BaseUsed || generation != r.To) {
		return fmt.Errorf("%w: rotation baseline changed", ErrConflict)
	}
	if _, err := b.switchSession(opts, r.From, r.To); err != nil {
		return err
	}
	state.Version, state.SessionGeneration, state.SessionStartUsed = schedulingVersion, r.To, r.BaseUsed
	if err := store.saveScheduling(*state); err != nil {
		return err
	}
	r.Phase, r.Error, r.FailedStep = "completed", "", ""
	return store.saveRotation(*r)
}
