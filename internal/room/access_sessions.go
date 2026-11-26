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

type savedAccessSession struct {
	userSubject
	Expires time.Time `json:"expires_at"`
}

type accessSessionSnapshot struct {
	Version  int                           `json:"version"`
	Binding  string                        `json:"binding"`
	Sessions map[string]savedAccessSession `json:"sessions"`
}

func (a *serverAccess) sessionPath() string {
	return filepath.Join(filepath.Dir(a.config.path), "auth", "browser-sessions.json")
}

// Bind saved sessions to this installation, owner and current credential. A
// deployment-side password replacement must invalidate sessions on restart too.
func (a *serverAccess) sessionBinding() string {
	if a.users != nil {
		if c := a.config.Deployment.Authentication; c.Provider == "casdoor" {
			hash := sha256.Sum256([]byte(a.installationID + "\x00multi-user\x00casdoor\x00" + c.Issuer + "\x00" + c.ClientID))
			return hex.EncodeToString(hash[:])
		}
		hash := sha256.Sum256([]byte(a.installationID + "\x00multi-user\x00" + a.config.Deployment.Authentication.Provider + "\x00" + a.config.Deployment.Authentication.HubOrigin))
		return hex.EncodeToString(hash[:])
	}
	hash := sha256.Sum256([]byte(a.installationID + "\x00" + a.identity.User.ID + "\x00" + hex.EncodeToString(a.admin[:])))
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
	var header struct {
		Version int    `json:"version"`
		Binding string `json:"binding"`
	}
	if len(data) > maxAccessSessionBytes || json.Unmarshal(data, &header) != nil {
		return errors.New("invalid browser sessions file")
	}
	var saved accessSessionSnapshot
	switch header.Version {
	case 1:
		if a.users != nil {
			return errors.New("personal sessions cannot be adopted as multi-user")
		}
		// The old credential/installation binding proves the only permitted owner.
		// Persist the upgrade before accepting a legacy cookie.
		var legacy struct {
			Sessions map[string]time.Time `json:"sessions"`
		}
		if json.Unmarshal(data, &legacy) != nil {
			return errors.New("invalid browser sessions file")
		}
		saved = accessSessionSnapshot{Version: 2, Binding: header.Binding, Sessions: map[string]savedAccessSession{}}
		for digest, expiry := range legacy.Sessions {
			saved.Sessions[digest] = savedAccessSession{a.subject(), expiry}
		}
	case 2:
		if json.Unmarshal(data, &saved) != nil {
			return errors.New("invalid browser sessions file")
		}
	default:
		return errors.New("invalid browser sessions version")
	}
	if len(saved.Sessions) > maxAccessSessions {
		return errors.New("invalid browser sessions file")
	}
	next := map[[32]byte]accessSession{}
	if !a.disabled && saved.Binding == a.sessionBinding() {
		for value, session := range saved.Sessions {
			hash, err := hex.DecodeString(value)
			if err != nil || len(hash) != sha256.Size {
				return errors.New("invalid browser session digest")
			}
			if err := a.authorizeSubject(session.userSubject); err == nil && a.now().Before(session.Expires) {
				next[[32]byte(hash)] = accessSession{expires: session.Expires, Subject: session.userSubject}
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
	saved := accessSessionSnapshot{Version: 2, Binding: a.sessionBinding(), Sessions: map[string]savedAccessSession{}}
	for hash, session := range next {
		saved.Sessions[hex.EncodeToString(hash[:])] = savedAccessSession{session.Subject, session.expires}
	}
	if err := a.sessionStore.save(a.sessionPath(), saved, maxAccessSessionBytes); err != nil {
		return fmt.Errorf("save browser sessions: %w", err)
	}
	a.sessions = next
	a.revokeUIUpdatesLocked(false)
	return nil
}
