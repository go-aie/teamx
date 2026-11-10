package room

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform"
)

// Operator/account association is evidence only. It grants no send capability
// and does not replace the separately enabled, verified self-delivery binding.
type actionAccountOwner struct {
	Version int `json:"version"`
	MemberIdentity
	Account    platform.AccountIdentity `json:"account"`
	PlatformID string                   `json:"platform_id"`
	Generation string                   `json:"generation"`
	Source     RoomMessage              `json:"source"`
}

func (s *Store) actionAccountOwner(id MemberIdentity, a platform.AccessContext, seq, turn uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return false, err
	}
	if !a.Valid() || a.RoomIdentity != id.RoomIdentity || a.AgentID != id.AgentID || a.MembershipID != id.MembershipID {
		return false, ErrIdentity
	}
	path := filepath.Join(s.actionRoot(id), "account-owner-"+referenceDigest("", a.Account.Provider, a.PlatformID)+".json")
	if seq != 0 {
		if _, err := s.runningTurnLocked(id, turn); err != nil {
			return false, err
		}
		room, err := s.loadRoom()
		if err != nil {
			return false, err
		}
		source, err := roomHumanMessage(room, seq)
		if err != nil {
			return false, err
		}
		if !validHumanEvidence(source) || source.ReplyTo != 0 {
			return false, ErrIdentity
		}
		evidence := actionAccountOwner{Version: actionSchema, MemberIdentity: id, Account: a.Account, PlatformID: a.PlatformID, Generation: a.Generation, Source: source}
		if err := s.save(path, evidence, followupBytes); err != nil {
			return false, err
		}
		return true, nil
	}
	var evidence actionAccountOwner
	if err := readSnapshot(path, &evidence, followupBytes); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Historical provider-scoped proof is read only and still must match
			// this exact platform/account/generation. New confirmations never
			// overwrite another platform's evidence or renew the old proof.
			legacy := filepath.Join(s.actionRoot(id), "account-owner-"+referenceDigest("", a.Account.Provider)+".json")
			if err := readSnapshot(legacy, &evidence, followupBytes); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return false, nil
				}
				return false, err
			}
		} else {
			return false, err
		}
	}
	if evidence.Version != actionSchema || evidence.MemberIdentity != id || !validHumanEvidence(evidence.Source) {
		return false, ErrInvalid
	}
	return evidence.Account == a.Account && evidence.PlatformID == a.PlatformID && evidence.Generation == a.Generation, nil
}
