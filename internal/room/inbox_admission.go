package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type inboxAdmissionResolver func(context.Context, *service, platform.SourceRef) (json.RawMessage, error)

type automaticInboxCheck struct {
	latest, through uint64
	skipped         int
	ready, blocked  bool
	until           time.Time
}

func (s *Store) notificationCapabilities(provider string) notification.Capabilities {
	if s.notificationAdapters != nil {
		return s.notificationAdapters(provider)
	}
	return notificationCapabilities(provider)
}

func admissionPath(s *Store, cap *notification.AdmissionCapability, source platform.SourceRef) (string, error) {
	if cap == nil || cap.EvidencePath == nil || cap.Inspect == nil {
		return "", ErrInvalid
	}
	relative := cap.EvidencePath(source)
	if !filepath.IsLocal(relative) {
		return "", ErrInvalid
	}
	return filepath.Join(s.dir, relative), nil
}

// Raw Inbox evidence remains immutable. Only the adapter interprets its
// separate native proof; the common Store knows verification and admission.
func (s *Store) inboxAdmissionLocked(e InboxEntry) (notification.Admission, error) {
	cap := s.notificationCapabilities(e.Item.Source.Provider).Admission
	if cap == nil {
		return notification.Admission{Verified: true, Decision: notification.AdmissionAllow}, nil
	}
	if cap.Inspect == nil {
		return notification.Admission{}, ErrInvalid
	}
	initial, err := cap.Inspect(e.Item, nil)
	if err != nil || !initial.Valid() {
		return notification.Admission{}, ErrInvalid
	}
	if initial.Verified {
		return initial, nil
	}
	path, err := admissionPath(s, cap, e.Item.Source)
	if err != nil {
		return notification.Admission{}, err
	}
	var evidence json.RawMessage
	if err := readSnapshot(path, &evidence, maxPrivateFileBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
		return notification.Admission{}, err
	}
	result, err := cap.Inspect(e.Item, evidence)
	if err == nil && !result.Valid() {
		err = ErrInvalid
	}
	return result, err
}

// Reconciliation runs outside Inbox actors and manager locks. Missing evidence
// blocks automatic input without turning a failed verification into exclusion.
func (s *service) reconcileInboxAdmission(ctx context.Context, id MemberIdentity, status InboxStatus) (InboxStatus, error) {
	s.inboxAdmissionMu.Lock()
	defer s.inboxAdmissionMu.Unlock()
	status.AutomaticChecked = true
	status.AutomaticReady, status.AutomaticBlocked = false, false
	if c, ok := s.inboxAdmissionChecks[id.MembershipID]; ok && c.latest == status.LatestSeq && c.through == status.inputThrough() && c.skipped == status.SkippedEntries && time.Now().Before(c.until) {
		status.AutomaticReady, status.AutomaticBlocked = c.ready, c.blocked
		return status, nil
	}
	remember := func() {
		if s.inboxAdmissionChecks == nil {
			s.inboxAdmissionChecks = map[string]automaticInboxCheck{}
		}
		s.inboxAdmissionChecks[id.MembershipID] = automaticInboxCheck{status.LatestSeq, status.inputThrough(), status.SkippedEntries, status.AutomaticReady, status.AutomaticBlocked, time.Now().Add(time.Minute)}
	}
	var consumption inboxConsumption
	s.store.mu.Lock()
	readErr := readSnapshot(filepath.Join(s.store.dir, "inboxes", id.MembershipID, "consumption.json"), &consumption, maxPrivateFileBytes)
	s.store.mu.Unlock()
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return status, readErr
	}
	for seq := status.inputThrough() + 1; seq <= status.LatestSeq; seq++ {
		s.store.mu.Lock()
		e, err := s.store.inboxEntryLocked(id, seq)
		var result notification.Admission
		if err == nil && consumption.skipped(seq) {
			s.store.mu.Unlock()
			continue
		}
		if err == nil {
			result, err = s.store.inboxAdmissionLocked(e)
		}
		s.store.mu.Unlock()
		if err != nil {
			return status, err
		}
		if !result.Verified && s.resolveInboxAdmission != nil {
			cap := s.store.notificationCapabilities(e.Item.Source.Provider).Admission
			path, err := admissionPath(s.store, cap, e.Item.Source)
			if err != nil {
				return status, err
			}
			if !time.Now().Before(s.inboxAdmissionRetry[path]) {
				if s.inboxAdmissionRetry == nil {
					s.inboxAdmissionRetry = map[string]time.Time{}
				}
				s.inboxAdmissionRetry[path] = time.Now().Add(time.Minute)
				evidence, queryErr := s.resolveInboxAdmission(ctx, e.Item.Source)
				if queryErr == nil {
					checked, err := cap.Inspect(e.Item, evidence)
					if err != nil || !checked.Valid() {
						return status, ErrInvalid
					}
					if checked.Verified {
						s.store.mu.Lock()
						err = s.store.save(path, evidence, maxPrivateFileBytes)
						s.store.mu.Unlock()
						if err != nil {
							return status, err
						}
						result = checked
					}
				}
			}
		}
		if !result.Verified {
			status.AutomaticBlocked = true
			remember()
			return status, nil
		}
		if result.Decision == notification.AdmissionAllow {
			status.AutomaticReady = true
		}
	}
	remember()
	return status, nil
}
