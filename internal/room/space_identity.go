package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

// User is the login subject; neither a display name nor an Agent membership.
type User struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	AuthVersion uint64 `json:"auth_version"`
}

type SpaceRecord struct {
	ID          string     `json:"id"`
	OwnerUserID string     `json:"owner_user_id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Version     uint64     `json:"version"`
	Roots       spaceRoots `json:"roots"`
}

// Existing resources are registered in place, including Computer's home-based
// state and credentials. No path or installation/owner-derived ID is rewritten.
type spaceRoots struct {
	Home  string `json:"home"`
	Data  string `json:"data"`
	Teams string `json:"teams"`
}

type personalRegistration struct {
	Version        int         `json:"version"`
	InstallationID string      `json:"installation_id"`
	User           User        `json:"user"`
	Space          SpaceRecord `json:"space"`
	Completed      bool        `json:"completed"`
}

type userDirectory struct {
	Version int    `json:"version"`
	Users   []User `json:"users"`
}
type spaceDirectory struct {
	Version        int           `json:"version"`
	DefaultSpaceID string        `json:"default_space_id"`
	Spaces         []SpaceRecord `json:"spaces"`
}

type userSubject struct {
	UserID      string `json:"user_id"`
	AuthVersion uint64 `json:"auth_version"`
}

// SpaceContext is fixed at assembly/authorization, never a mutable current Space.
// RuntimeEpoch fences execution credentials without changing Session identity.
type SpaceContext struct {
	SpaceID      string `json:"space_id"`
	OwnerUserID  string `json:"owner_user_id"`
	RuntimeEpoch string `json:"runtime_epoch"`
}

type authorizedSpace struct {
	Subject userSubject
	SpaceContext
}
type spaceContextKey struct{}

func registerPersonalSpace(c startupConfig, installation string, writer *snapshotWriter) (_ *personalRegistration, runErr error) {
	home := filepath.Dir(c.path)
	if err := makeDurableDir(home); err != nil {
		return nil, err
	}
	lock, err := lockDirectoryFile(home, ".identity.lock")
	if err != nil {
		return nil, err
	}
	defer func() { runErr = errors.Join(runErr, unlockDirectory(lock)) }()
	roots := spaceRoots{Home: home}
	if c.Server.DataDir != nil {
		roots.Data = *c.Server.DataDir
	}
	if c.singleRoomDir != "" {
		roots.Data = c.singleRoomDir
	}
	if c.Server.TeamsDir != nil {
		roots.Teams = *c.Server.TeamsDir
	}
	for _, root := range []*string{&roots.Home, &roots.Data, &roots.Teams} {
		if *root == "" {
			continue
		}
		*root, err = resolveFutureDirectory(*root)
		if err != nil {
			return nil, err
		}
	}
	operationPath := filepath.Join(home, "personal-space-registration.json")
	usersPath, spacesPath := filepath.Join(home, "users.json"), filepath.Join(home, "spaces.json")
	var registration personalRegistration
	err = readIdentitySnapshot(operationPath, &registration)
	if errors.Is(err, os.ErrNotExist) {
		// A missing journal beside a published directory is damage, not first start.
		for _, path := range []string{usersPath, spacesPath} {
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				return nil, fmt.Errorf("identity registration missing beside %s; repair required", filepath.Base(path))
			}
		}
		id, err := newOpaqueID()
		if err != nil {
			return nil, err
		}
		registration = personalRegistration{Version: 1, InstallationID: installation,
			User:  User{ID: c.Access.OperatorID, Status: "active", AuthVersion: 1},
			Space: SpaceRecord{ID: id, OwnerUserID: c.Access.OperatorID, Name: "默认空间", Status: "active", Version: 1, Roots: roots}}
		if err := writer.save(operationPath, registration, maxPrivateFileBytes); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("read personal registration: %w", err)
	}
	if registration.Version != 1 || registration.InstallationID != installation || registration.User.ID != c.Access.OperatorID || !validOperatorID(registration.User.ID) || registration.User.AuthVersion == 0 || registration.User.Status != "active" || registration.Space.Status != "active" || registration.Space.OwnerUserID != registration.User.ID || !safeMembershipID(registration.Space.ID) || registration.Space.Version == 0 || registration.Space.Roots != roots {
		return nil, errors.New("personal identity or resource roots changed; repair required")
	}
	expectedUsers := userDirectory{1, []User{registration.User}}
	expectedSpaces := spaceDirectory{1, registration.Space.ID, []SpaceRecord{registration.Space}}
	var users userDirectory
	var spaces spaceDirectory
	// Only an incomplete reservation may publish missing files. Existing records
	// must match, even during retry; corrupt or foreign state is never overwritten.
	for _, item := range []struct {
		path            string
		value, expected any
	}{
		{usersPath, &users, expectedUsers}, {spacesPath, &spaces, expectedSpaces},
	} {
		err := readIdentitySnapshot(item.path, item.value)
		if errors.Is(err, os.ErrNotExist) && !registration.Completed {
			if err := writer.save(item.path, item.expected, maxPrivateFileBytes); err != nil {
				return nil, err
			}
			err = readIdentitySnapshot(item.path, item.value)
		}
		if err != nil {
			return nil, fmt.Errorf("read identity directory %s: %w", filepath.Base(item.path), err)
		}
		if item.path == spacesPath && registration.Completed {
			if err := validateSpaceDirectory(spaces, registration.Space, userSubject{registration.User.ID, registration.User.AuthVersion}); err != nil {
				return nil, err
			}
		} else if !reflect.DeepEqual(reflect.ValueOf(item.value).Elem().Interface(), item.expected) {
			return nil, errors.New("identity directory disagrees with registration; repair required")
		}
	}
	if !registration.Completed {
		registration.Completed = true
		if err := writer.save(operationPath, registration, maxPrivateFileBytes); err != nil {
			return nil, err
		}
	}
	for _, record := range spaces.Spaces {
		if record.ID == registration.Space.ID {
			registration.Space = record
		}
	}
	return &registration, nil
}

func (a *serverAccess) subject() userSubject {
	if a == nil || a.identity == nil {
		return userSubject{}
	}
	return userSubject{a.identity.User.ID, a.identity.User.AuthVersion}
}
func (a *serverAccess) authorizeSubject(subject userSubject) error {
	if a != nil && a.authority().users != nil {
		return a.authority().users.authorize(subject)
	}
	if a == nil || a.identity == nil || subject.UserID == "" || subject.AuthVersion == 0 || a.identity.User.Status != "active" || subject != a.subject() {
		return ErrIdentity
	}
	return nil
}
func (a *serverAccess) authorizeRegisteredSpace(subject userSubject, target string) error {
	if a.authorizeSubject(subject) != nil || target != a.space.SpaceID || a.identity.Space.ID != target || !validSpaceStatus(a.identity.Space.Status) || a.identity.Space.OwnerUserID != subject.UserID || a.space.OwnerUserID != subject.UserID || a.space.RuntimeEpoch == "" {
		return ErrIdentity
	}
	return nil
}
func (a *serverAccess) authorizeSpace(subject userSubject, target string) (authorizedSpace, error) {
	if a.authorizeRegisteredSpace(subject, target) != nil || a.revoked.Load() || a.identity.Space.Status != "active" {
		return authorizedSpace{}, ErrIdentity
	}
	return authorizedSpace{subject, a.space}, nil
}
func (a *serverAccess) bindExecution(binding executionBinding) (executionBinding, error) {
	if binding.Subject == (userSubject{}) {
		binding.Subject = a.subject()
	}
	if binding.SpaceContext == (SpaceContext{}) {
		binding.SpaceContext = a.space
	}
	if binding.SpaceContext != a.space {
		return binding, ErrIdentity
	}
	_, err := a.authorizeSpace(binding.Subject, binding.SpaceID)
	return binding, err
}
func (a *serverAccess) bindSpaceRequest(r *http.Request, subject userSubject) (*http.Request, error) {
	target := a.space.SpaceID
	path := r.URL.Path
	// Control-plane authentication survives archiving the default Space. It
	// grants no business admission; lifecycle handlers independently check owner.
	if a.spaces != nil && a.control == nil && spaceControlPath(path) {
		if err := a.authorizeSubject(subject); err != nil {
			return nil, err
		}
		return r.WithContext(context.WithValue(r.Context(), spaceContextKey{}, authorizedSpace{subject, a.space})), nil
	}
	if strings.HasPrefix(path, "/v1/spaces/") && !(a.spaces != nil && !strings.Contains(strings.TrimPrefix(path, "/v1/spaces/"), "/")) {
		selected, suffix, ok := strings.Cut(strings.TrimPrefix(path, "/v1/spaces/"), "/")
		if !ok || selected == "" || suffix == "" {
			return nil, ErrIdentity
		}
		target = selected
		copyURL := *r.URL
		copyURL.Path, copyURL.RawPath = "/v1/"+suffix, ""
		r = r.Clone(r.Context())
		r.URL = &copyURL
	}
	scope, err := a.authorizeSpace(subject, target)
	if err != nil {
		return nil, err
	}
	return r.WithContext(context.WithValue(r.Context(), spaceContextKey{}, scope)), nil
}
func (a *serverAccess) ownsRequest(r *http.Request) bool {
	scope, ok := r.Context().Value(spaceContextKey{}).(authorizedSpace)
	if !ok || scope.SpaceContext != a.space {
		return false
	}
	_, err := a.authorizeSpace(scope.Subject, scope.SpaceID)
	return err == nil
}

// Read from the same checked descriptor used by config/session access. Never
// follow a symlink or accept a registry writable by another OS account.
func readIdentitySnapshot(path string, value any) error {
	return readIdentitySnapshotForUID(path, value, uint32(os.Geteuid()))
}

// Root-run service maintenance must check the registered ordinary service
// account, while runtime reads continue to require the current process owner.
func readIdentitySnapshotForUID(path string, value any, owner uint32) error {
	data, info, err := readSettingsFile(path)
	if err != nil {
		return err
	}
	if info == nil {
		return os.ErrNotExist
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != owner || info.Mode().Perm() != 0600 || int64(len(data)) > maxPrivateFileBytes {
		return errors.New("identity directory must be a private 0600 file")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func requestSpaceID(r *http.Request) string {
	if scope, ok := r.Context().Value(spaceContextKey{}).(authorizedSpace); ok {
		return scope.SpaceID
	}
	return ""
}
