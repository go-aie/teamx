package room

import (
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-aie/teamx/internal/authn/opencsg"
)

type externalAuthConfig struct {
	Provider              string   `toml:"provider"`
	HubOrigin             string   `toml:"hub_origin"`
	Issuer                string   `toml:"issuer"`
	ClientID              string   `toml:"client_id"`
	ClientSecretFile      string   `toml:"client_secret_file"`
	AllowedSubjects       []string `toml:"allowed_subjects"`
	AllowAllAuthenticated bool     `toml:"allow_all_authenticated"`
	SessionMaxAge         string   `toml:"session_max_age"`
}

func (c externalAuthConfig) provider() (authn.Provider, error) {
	if _, err := c.sessionMaxAge(); err != nil {
		return nil, err
	}
	if !c.AllowAllAuthenticated && len(c.AllowedSubjects) == 0 {
		return nil, errors.New("authentication requires allowed_subjects or explicit allow_all_authenticated")
	}
	if c.Provider != "opencsg" {
		return nil, errors.New("multi-user deployment requires an explicit supported authentication provider")
	}
	if c.HubOrigin != "" {
		if _, err := opencsg.NewOAuthGateway(c.HubOrigin, nil); err != nil {
			return nil, err
		}
	}
	var secret string
	if c.ClientSecretFile != "" {
		if !filepath.IsAbs(c.ClientSecretFile) {
			return nil, errors.New("client_secret_file must be absolute")
		}
		data, info, err := readSettingsFile(c.ClientSecretFile)
		if err != nil || info == nil || info.Mode().Perm() != 0600 || len(data) > 16<<10 || strings.TrimSpace(string(data)) == "" {
			return nil, errors.New("client_secret_file must be a nonempty private 0600 regular file (maximum 16 KiB)")
		}
		secret = strings.TrimSpace(string(data))
	}
	return opencsg.NewRenewable(strings.TrimRight(c.Issuer, "/"), c.ClientID, secret, nil)
}

type externalUser struct {
	User
	Identity authn.Identity `json:"external_identity"`
	// The immutable reservation precedes publishing spaces.json. Retry/restart
	// completes this exact default Space, never mints a second user or Space.
	DefaultSpace SpaceRecord `json:"default_space"`
}

type hostedUserSnapshot struct {
	Version        int            `json:"version"`
	InstallationID string         `json:"installation_id"`
	Source         [2]string      `json:"source"`
	Users          []externalUser `json:"users"`
}

type hostedUsers struct {
	mu             sync.Mutex
	failed         bool
	home           string
	saved          hostedUserSnapshot
	writer         snapshotWriter
	gatewayChanges map[string]chan struct{} // Broadcast credential availability to setup workers.
}

func checkDeploymentRoot(c startupConfig) error {
	home := filepath.Dir(c.path)
	foreign := "multi-user-registration.json"
	if c.Deployment.Mode == "multi-user" {
		foreign = "personal-space-registration.json"
	}
	if _, err := os.Lstat(filepath.Join(home, foreign)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("business root belongs to another deployment mode; migration is required")
	}
	if c.Deployment.Mode == "multi-user" {
		if _, err := os.Lstat(filepath.Join(home, "multi-user-registration.json")); errors.Is(err, os.ErrNotExist) {
			for _, name := range []string{"users.json", "spaces.json", "first-access.json", "model-credentials", "computer-credentials", "computer-state"} {
				if _, err := os.Lstat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
					return errors.New("existing personal resources require an explicit deployment migration")
				}
			}
			if c.Server.DataDir != nil {
				if err := requireFreshModelHome(c); err != nil {
					return errors.New("existing business data requires an explicit deployment migration")
				}
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func registerHostedUsers(c startupConfig, installation string) (_ *hostedUsers, runErr error) {
	if err := checkDeploymentRoot(c); err != nil {
		return nil, err
	}
	home := filepath.Dir(c.path)
	home, resolveErr := resolveFutureDirectory(home)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if err := makeDurableDir(home); err != nil {
		return nil, err
	}
	lock, err := lockDirectoryFile(home, ".identity.lock")
	if err != nil {
		return nil, err
	}
	defer func() { runErr = errors.Join(runErr, unlockDirectory(lock)) }()
	u := &hostedUsers{home: home, writer: snapshotWriter{ops: defaultSnapshotOps()}}
	var journal struct {
		Version        int    `json:"version"`
		InstallationID string `json:"installation_id"`
		Completed      bool   `json:"completed"`
	}
	path := filepath.Join(home, "multi-user-registration.json")
	err = readIdentitySnapshot(path, &journal)
	if errors.Is(err, os.ErrNotExist) {
		for _, name := range []string{"users.json", "spaces.json", "first-access.json"} {
			if _, err := os.Lstat(filepath.Join(home, name)); !errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("unregistered business root cannot be adopted as multi-user")
			}
		}
		journal.Version, journal.InstallationID = 1, installation
		if err := u.writer.save(path, journal, maxPrivateFileBytes); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if journal.Version != 1 || journal.InstallationID != installation {
		return nil, ErrIdentity
	}
	u.saved = hostedUserSnapshot{Version: 1, InstallationID: installation}
	err = readIdentitySnapshot(filepath.Join(home, "users.json"), &u.saved)
	if errors.Is(err, os.ErrNotExist) && !journal.Completed {
		err = u.writer.save(filepath.Join(home, "users.json"), u.saved, maxPrivateFileBytes)
	}
	if err != nil {
		return nil, err
	}
	if u.saved.Version != 1 || u.saved.InstallationID != installation {
		return nil, ErrIdentity
	}
	ids, keys := map[string]bool{}, map[[3]string]bool{}
	for _, entry := range u.saved.Users {
		key := externalIdentityKey(entry.Identity)
		if u.saved.Source != ([2]string{key[0], key[1]}) {
			return nil, ErrIdentity
		}
		if !validOperatorID(entry.ID) || entry.AuthVersion == 0 || entry.Status != "active" && entry.Status != "disabled" || ids[entry.ID] || keys[key] || key[0] == "" || key[1] == "" || key[2] == "" || entry.DefaultSpace.OwnerUserID != entry.ID || !validSpaceID(entry.DefaultSpace.ID) {
			return nil, ErrIdentity
		}
		ids[entry.ID], keys[key] = true, true
	}
	if auth := c.Deployment.Authentication; auth.Provider == "opencsg" && u.saved.Source != ([2]string{}) &&
		u.saved.Source != ([2]string{"opencsg", strings.TrimRight(auth.Issuer, "/")}) {
		return nil, errors.New("saved authentication source does not match the configured OpenCSG issuer")
	}
	var spaces spaceDirectory
	err = readIdentitySnapshot(filepath.Join(home, "spaces.json"), &spaces)
	if errors.Is(err, os.ErrNotExist) && !journal.Completed {
		err = u.writer.save(filepath.Join(home, "spaces.json"), spaceDirectory{Version: 1}, maxPrivateFileBytes)
	}
	if err != nil {
		return nil, err
	}
	if !journal.Completed {
		journal.Completed = true
		if err := u.writer.save(path, journal, maxPrivateFileBytes); err != nil {
			return nil, err
		}
	}
	return u, nil
}

func externalIdentityKey(id authn.Identity) [3]string {
	return [3]string{id.Provider, id.Authority, id.Subject}
}

func (u *hostedUsers) user(id string) (externalUser, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failed {
		return externalUser{}, false
	}
	for _, entry := range u.saved.Users {
		if entry.ID == id {
			return entry, true
		}
	}
	return externalUser{}, false
}

func (u *hostedUsers) authorize(subject userSubject) error {
	entry, ok := u.user(subject.UserID)
	if !ok || entry.Status != "active" || subject.AuthVersion != entry.AuthVersion {
		return ErrIdentity
	}
	return nil
}

func (u *hostedUsers) reserve(identity authn.Identity, admission externalAuthConfig) (externalUser, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failed {
		return externalUser{}, ErrCommitUnknown
	}
	if identity.Provider == "" || identity.Authority == "" || identity.Subject == "" ||
		!admission.AllowAllAuthenticated && !slices.Contains(admission.AllowedSubjects, identity.Subject) {
		return externalUser{}, ErrIdentity
	}
	source := [2]string{identity.Provider, identity.Authority}
	if u.saved.Source != ([2]string{}) && u.saved.Source != source {
		return externalUser{}, ErrIdentity
	}
	for i, entry := range u.saved.Users {
		if externalIdentityKey(entry.Identity) == externalIdentityKey(identity) {
			if entry.Status != "active" {
				return externalUser{}, ErrIdentity
			}
			if entry.Identity.DisplayName != identity.DisplayName {
				entry.Identity.DisplayName = identity.DisplayName
				next := u.saved
				next.Users = slices.Clone(next.Users)
				next.Users[i] = entry
				if err := u.writer.save(filepath.Join(u.home, "users.json"), next, maxPrivateFileBytes); err != nil {
					if errors.Is(err, ErrCommitUnknown) {
						u.failed = true
					}
					return externalUser{}, err
				}
				u.saved = next
			}
			return entry, nil
		}
	}
	user, err := newOpaqueID()
	if err != nil {
		return externalUser{}, err
	}
	space, err := newOpaqueID()
	if err != nil {
		return externalUser{}, err
	}
	home := filepath.Join(u.home, "spaces", space)
	entry := externalUser{User: User{ID: "owner_" + user, Status: "active", AuthVersion: 1}, Identity: identity}
	entry.DefaultSpace = SpaceRecord{ID: space, OwnerUserID: entry.ID, Name: "默认空间", Status: "active", Version: 1, Roots: spaceRoots{home, filepath.Join(home, "data"), filepath.Join(home, "teams")}}
	next := u.saved
	next.Source = source
	next.Users = append(slices.Clone(next.Users), entry)
	if err := u.writer.save(filepath.Join(u.home, "users.json"), next, maxPrivateFileBytes); err != nil {
		if errors.Is(err, ErrCommitUnknown) {
			u.failed = true
		}
		return externalUser{}, err
	}
	u.saved = next
	return entry, nil
}

// A local disable increments the durable authentication version before any
// runtime cancellation. Failure/unknown never grants a new session.
func (u *hostedUsers) setStatus(id, status string) (externalUser, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failed {
		return externalUser{}, ErrCommitUnknown
	}
	if status != "active" && status != "disabled" {
		return externalUser{}, ErrRequest
	}
	next := u.saved
	next.Users = slices.Clone(next.Users)
	for i, entry := range next.Users {
		if entry.ID != id {
			continue
		}
		if entry.Status == status {
			return entry, nil
		}
		if entry.AuthVersion == ^uint64(0) {
			return externalUser{}, ErrInvalid
		}
		entry.Status = status
		entry.AuthVersion++
		next.Users[i] = entry
		if err := u.writer.save(filepath.Join(u.home, "users.json"), next, maxPrivateFileBytes); err != nil {
			if errors.Is(err, ErrCommitUnknown) {
				u.failed = true
			}
			return externalUser{}, err
		}
		u.saved = next
		return entry, nil
	}
	return externalUser{}, ErrIdentity
}

// Identity-only providers retain the short-session contract. Production OpenCSG
// uses the renewable host owner and its separate absolute/check deadlines.
// Gateway credentials remain in the owner's independent model credential store.
const externalSessionLifetime = 15 * time.Minute

func tokenDigest(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func (s *spaceServer) validateDirectory(directory spaceDirectory) error {
	for _, record := range directory.Spaces {
		if record.ModelSourceSpaceID == "" {
			continue
		}
		if s.access.users != nil || !validSpaceID(record.ModelSourceSpaceID) || record.ModelSourceSpaceID == record.ID {
			return ErrIdentity
		}
		owned := false
		for _, source := range directory.Spaces {
			owned = owned || source.ID == record.ModelSourceSpaceID && source.OwnerUserID == record.OwnerUserID
		}
		if !owned {
			return ErrIdentity
		}
	}
	if s.access.users == nil {
		return validateSpaceDirectory(directory, s.access.identity.Space, s.access.subject())
	}
	if directory.Version != 1 || directory.DefaultSpaceID != "" {
		return ErrIdentity
	}
	seen := map[string]bool{}
	for _, record := range directory.Spaces {
		_, ok := s.access.users.user(record.OwnerUserID)
		home := filepath.Join(s.assembly.roots.Home, "spaces", record.ID)
		if !ok || seen[record.ID] || !validSpaceID(record.ID) || !validSpaceStatus(record.Status) || record.Version == 0 || !validSpaceName(record.Name) || record.Roots != (spaceRoots{home, filepath.Join(home, "data"), filepath.Join(home, "teams")}) {
			return ErrIdentity
		}
		for _, path := range []string{record.Roots.Home, record.Roots.Data, record.Roots.Teams} {
			resolved, err := resolveFutureDirectory(path)
			if err != nil || resolved != path {
				return ErrIdentity
			}
		}
		seen[record.ID] = true
	}
	return nil
}

func (a *serverAccess) browserSubject(r *http.Request) (userSubject, bool) {
	a = a.authority()
	cookie, err := r.Cookie(a.cookieName())
	if err != nil {
		return userSubject{}, false
	}
	a.mu.Lock()
	session, exists := a.sessions[tokenDigest(cookie.Value)]
	now := a.now()
	a.mu.Unlock()
	if a.renewal != nil {
		a.mu.Lock()
		unavailable := a.renewal.broken || a.renewal.closed || a.renewal.denied[tokenDigest(cookie.Value)]
		a.mu.Unlock()
		if unavailable {
			return userSubject{}, false
		}
	}
	return session.Subject, exists && now.Before(session.expires) && a.authorizeSubject(session.Subject) == nil
}

func (a *serverAccess) defaultSpace(subject userSubject) string {
	if a.users != nil {
		if entry, ok := a.users.user(subject.UserID); ok {
			return entry.DefaultSpace.ID
		}
		return ""
	}
	return a.space.SpaceID
}

func (s *spaceServer) admitExternal(ctx context.Context, w http.ResponseWriter, identity authn.Identity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return ErrClosing
	}
	entry, err := s.access.users.reserve(identity, s.access.config.Deployment.Authentication)
	if err == nil {
		err = s.publishDefaultSpaceLocked(entry)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	a := s.access
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.authorizeSubject(userSubject{entry.ID, entry.AuthVersion}); err != nil {
		return err
	}
	token, err := randomAccessToken()
	if err != nil {
		return err
	}
	next := maps.Clone(a.sessions)
	for key, session := range next {
		if !a.now().Before(session.expires) {
			delete(next, key)
		}
	}
	if len(next) >= maxAccessSessions {
		return ErrBusy
	}
	next[tokenDigest(token)] = accessSession{Subject: userSubject{entry.ID, entry.AuthVersion}, expires: a.now().Add(externalSessionLifetime)}
	if !identity.ValidUntil.IsZero() {
		session := next[tokenDigest(token)]
		if !a.now().Before(identity.ValidUntil) {
			return ErrIdentity
		}
		if identity.ValidUntil.Before(session.expires) {
			session.expires = identity.ValidUntil
			next[tokenDigest(token)] = session
		}
	}
	if err := a.replaceSessions(next); err != nil {
		return err
	}
	http.SetCookie(w, a.cookie(token, int(externalSessionLifetime/time.Second)))
	return nil
}

func (s *spaceServer) publishDefaultSpaceLocked(entry externalUser) error {
	for _, record := range s.directory.Spaces {
		if record.ID == entry.DefaultSpace.ID {
			if record.OwnerUserID != entry.ID || record.Roots != entry.DefaultSpace.Roots {
				return ErrIdentity
			}
			return nil
		}
	}
	record := entry.DefaultSpace
	if entry.Status == "disabled" {
		record.Status = "suspended"
	}
	next := s.directory
	next.Spaces = append(slices.Clone(next.Spaces), record)
	if err := s.assembly.config.executionResources.checkSpaces(next.Spaces); err != nil {
		return err
	}
	if err := s.validateDirectory(next); err != nil {
		return err
	}
	if s.claims != nil {
		s.claims.mu.Lock()
		defer s.claims.mu.Unlock()
		if err := s.claims.checkNewSpaceLocked(record); err != nil {
			return err
		}
	}
	if err := s.writer.save(filepath.Join(s.assembly.roots.Home, "spaces.json"), next, maxPrivateFileBytes); err != nil {
		return err
	}
	s.directory = next
	if s.claims != nil {
		s.claims.directory = next
		s.startLocked(record, nil, spaceAddress(s.access.activeListen))
	}
	return nil
}
