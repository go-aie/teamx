package room

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const accessSessionLifetime = 30 * 24 * time.Hour
const maxAccessSessions = 16
const maxAccessSessionBytes = 16 << 10

type accessSessionSnapshot struct {
	Version  int                  `json:"version"`
	Binding  string               `json:"binding"`
	Sessions map[string]time.Time `json:"sessions"`
}

func (a *serverAccess) sessionPath() string {
	return filepath.Join(filepath.Dir(a.config.path), "auth", "browser-sessions.json")
}

// Bind saved sessions to this installation, owner and current credential. A
// deployment-side password replacement must invalidate sessions on restart too.
func (a *serverAccess) sessionBinding() string {
	hash := sha256.Sum256([]byte(a.installationID + "\x00" + a.operator + "\x00" + hex.EncodeToString(a.admin[:])))
	return hex.EncodeToString(hash[:])
}

func (a *serverAccess) restoreSessions() error {
	data, file, err := readSettingsFile(a.sessionPath())
	if err != nil || file == nil {
		return err
	}
	stat, ok := file.Sys().(*syscall.Stat_t)
	if file.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("browser sessions must belong to the deployment account with mode 0600")
	}
	var saved accessSessionSnapshot
	if len(data) > maxAccessSessionBytes || json.Unmarshal(data, &saved) != nil || saved.Version != 1 || len(saved.Sessions) > maxAccessSessions {
		return errors.New("invalid browser sessions file")
	}
	next := map[[32]byte]accessSession{}
	if !a.disabled && saved.Binding == a.sessionBinding() {
		for value, expires := range saved.Sessions {
			hash, err := hex.DecodeString(value)
			if err != nil || len(hash) != sha256.Size {
				return errors.New("invalid browser session digest")
			}
			if a.now().Before(expires) {
				next[[32]byte(hash)] = accessSession{expires}
			}
		}
	}
	// Persist pruning and invalidation so reverting a password or re-enabling
	// login cannot resurrect sessions that were already revoked.
	return a.replaceSessions(next)
}

// Caller holds a.mu (or is initializing). Publish only after the atomic private
// snapshot and its directory have been synced; failed writes cannot grant login.
func (a *serverAccess) replaceSessions(next map[[32]byte]accessSession) error {
	if a.sessionStore.uncertain {
		return ErrCommitUnknown
	}
	saved := accessSessionSnapshot{Version: 1, Binding: a.sessionBinding(), Sessions: map[string]time.Time{}}
	for hash, session := range next {
		saved.Sessions[hex.EncodeToString(hash[:])] = session.expires
	}
	if err := a.sessionStore.save(a.sessionPath(), saved, maxAccessSessionBytes); err != nil {
		return fmt.Errorf("save browser sessions: %w", err)
	}
	a.sessions = next
	a.revokeUIUpdatesLocked(false)
	return nil
}
