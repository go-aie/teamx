// Package connection owns service-level connection records and credentials.
// Its owner serializes Load/Plan/Commit and Begin/Complete with its catalog lock.
// Query runs outside that lock. This package owns no worker or Room state.
package connection

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/go-aie/teamx/internal/platform"
)

var (
	ErrRequest  = errors.New("invalid platform connection request")
	ErrConflict = errors.New("platform connection conflicts with existing data")
	ErrCapacity = errors.New("platform connection capacity exceeded")
	ErrInvalid  = errors.New("invalid platform connection snapshot")
)

// NormalizeFunc validates the supported provider and canonicalizes configuration.
type NormalizeFunc func(name, provider, origin string) (string, string, error)
type Manager struct {
	store            Store
	normalize        NormalizeFunc
	identityResolver func(string, string, platform.Validation) (platform.AccountIdentity, bool)
	verifyProfile    func(context.Context, string, string, platform.CLIProfile) platform.Validation
	profileOnly      func(string) bool
	validProfile     func(string, string, platform.CLIProfile) bool
}

func New(store Store, normalize NormalizeFunc) *Manager {
	return &Manager{store: store, normalize: normalize}
}

// SetProfileSupport registers reference-only credentials without interpreting
// provider-specific CLI profiles in the common connection layer.
func (m *Manager) SetProfileSupport(only func(string) bool, valid func(string, string, platform.CLIProfile) bool, verify func(context.Context, string, string, platform.CLIProfile) platform.Validation) {
	m.profileOnly, m.validProfile, m.verifyProfile = only, valid, verify
}

// SetIdentityResolver delegates provider identity validation and historical
// field interpretation. Resolution is in-memory and never migrates disk.
func (m *Manager) SetIdentityResolver(resolve func(string, string, platform.Validation) (platform.AccountIdentity, bool)) {
	m.identityResolver = resolve
}

type Request struct {
	CLIProfile          *platform.CLIProfile
	ID                  string
	Version             uint64
	Name, Provider, URL string
}

func (Request) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED platform request]") }

// Change is a private candidate. Commit it only after reference checks and any
// affected queries have settled, under the same catalog serialization.
type Change struct {
	file     Snapshot
	record   Record
	rotating bool
}

func (Change) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED platform change]") }
func (c Change) Rotating() bool           { return c.rotating }
func (m *Manager) Commit(c Change) (Connection, error) {
	if err := m.save(c.file); err != nil {
		return Connection{}, err
	}
	return c.record.Connection, nil
}
func (m *Manager) Plan(req Request, remove bool) (Change, error) {
	file, err := m.Load()
	if err != nil {
		return Change{}, err
	}
	if !validRestartID(req.ID) {
		return Change{}, ErrRequest
	}
	index := -1
	for i, c := range file.Connections {
		if c.ID == req.ID {
			index = i
			break
		}
	}
	if index < 0 && (remove || req.Version != 0) || index >= 0 && file.Connections[index].Version != req.Version {
		return Change{}, ErrConflict
	}
	rotating := index >= 0 && req.CLIProfile != nil
	if remove {
		file.Connections = append(file.Connections[:index], file.Connections[index+1:]...)
		return Change{file: file}, nil
	}
	name, origin, err := m.normalize(req.Name, req.Provider, req.URL)
	if err != nil {
		return Change{}, err
	}
	c := Record{Connection: Connection{ID: req.ID, Validation: platform.Validation{Status: "unverified"}}}
	if index >= 0 {
		c = file.Connections[index]
		if c.CredentialKind == "oauth" && rotating {
			return Change{}, fmt.Errorf("%w: OAuth authorization requires explicit replacement", ErrRequest)
		}
		if c.Provider != req.Provider || c.URL != origin {
			return Change{}, fmt.Errorf("%w: create a new connection to change platform or instance", ErrRequest)
		}
	} else {
		if len(file.Connections) >= 100 {
			return Change{}, ErrCapacity
		}
		c.Generation, err = newOpaqueID()
		if err != nil {
			return Change{}, err
		}
	}
	if c.Version == ^uint64(0) || c.CredentialVersion == ^uint64(0) {
		return Change{}, ErrCapacity
	}
	c.Name, c.Provider, c.URL = name, req.Provider, origin
	if req.CLIProfile != nil {
		if m.validProfile == nil || !m.validProfile(req.Provider, origin, *req.CLIProfile) {
			return Change{}, ErrRequest
		}
		profile := *req.CLIProfile
		c.CLIProfile, c.CredentialID, c.CredentialKind = &profile, "", "cli_profile"
		c.CredentialConfigured = true
		c.CredentialVersion++
		c.Validation = platform.Validation{Status: "unverified"}
		c.IdentityBinding = nil
	}
	if m.profileOnly != nil && m.profileOnly(c.Provider) && c.CredentialKind != "cli_profile" {
		return Change{}, ErrRequest
	}
	if c.CredentialKind == "" && c.Provider == "gitlab" {
		c.CredentialKind = "oauth"
		c.CredentialVersion = 1
	}
	if !c.CredentialConfigured && c.CredentialKind != "oauth" {
		return Change{}, fmt.Errorf("%w: credential source required", ErrRequest)
	}
	c.Version++
	if index < 0 {
		file.Connections = append(file.Connections, c)
	} else {
		file.Connections[index] = c
	}
	return Change{file: file, record: c, rotating: rotating}, nil
}
func Normalize(name, origin string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", "", fmt.Errorf("%w: invalid connection name or provider", ErrRequest)
	}
	u, err := httpsURL(origin)
	if err != nil || u.Path != "" {
		return "", "", fmt.Errorf("%w: connection URL must be an HTTPS origin", ErrRequest)
	}
	return name, u.String(), nil
}

func httpsURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		len(value) > 4096 || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return &url.URL{}, ErrRequest
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

func validRestartID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}
func newOpaqueID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
