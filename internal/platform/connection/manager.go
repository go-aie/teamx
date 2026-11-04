// Package connection owns service-level connection records and credentials.
// Its owner serializes Load/Plan/Commit and Begin/Complete with its catalog lock.
// Query runs outside that lock. This package owns no worker or Room state.
package connection

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
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
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NormalizeFunc validates the supported provider and canonicalizes configuration.
type NormalizeFunc func(name, provider, origin string) (string, string, error)
type Manager struct {
	store     Store
	normalize NormalizeFunc
	verify    Verify
}

func New(store Store, normalize NormalizeFunc, verify Verify) *Manager {
	return &Manager{store: store, normalize: normalize, verify: verify}
}

type Request struct {
	ID                                 string
	Version                            uint64
	Name, Provider, URL, PAT, TokenEnv string
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
	rotating := index >= 0 && (req.PAT != "" || req.TokenEnv != "")
	if remove {
		file.Connections = append(file.Connections[:index], file.Connections[index+1:]...)
		file.removeCredential(req.ID)
		return Change{file: file}, nil
	}
	name, origin, err := m.normalize(req.Name, req.Provider, req.URL)
	if err != nil {
		return Change{}, err
	}
	c := Record{Connection: Connection{ID: req.ID, Validation: platform.Validation{Status: "unverified"}}}
	if index >= 0 {
		c = file.Connections[index]
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
	if req.PAT != "" && req.TokenEnv != "" {
		return Change{}, ErrRequest
	}
	if req.PAT != "" || req.TokenEnv != "" {
		if req.PAT != "" && !platform.ValidPAT(req.PAT) || req.TokenEnv != "" && (!envName.MatchString(req.TokenEnv) || len(req.TokenEnv) > 128) {
			return Change{}, fmt.Errorf("%w: invalid credential source", ErrRequest)
		}
		file.removeCredential(c.ID)
		c.CredentialID, c.TokenEnv, c.CredentialKind = "", req.TokenEnv, "pat"
		if req.PAT != "" {
			id, err := newOpaqueID()
			if err != nil {
				return Change{}, err
			}
			c.CredentialID = id
			file.Credentials = append(file.Credentials, Credential{ID: id, ConnectionID: c.ID, PAT: req.PAT})
		}
		if c.TokenEnv != "" {
			c.CredentialKind = "env"
		}
		c.CredentialConfigured = true
		c.CredentialVersion++
		c.Validation = platform.Validation{Status: "unverified"}
		c.IdentityBinding = nil
	}
	if !c.CredentialConfigured {
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

// This service-only resolver checks both revisions. Tool and event callers
// derive the connection from a validated Room binding, never an agent target.
func ResolveCredential(file Snapshot, c Record, version, credentialVersion uint64) (platform.Secret, error) {
	if c.Version != version || c.CredentialVersion != credentialVersion {
		return platform.Secret{}, ErrConflict
	}
	value := ""
	for _, cred := range file.Credentials {
		if cred.ID == c.CredentialID && cred.ConnectionID == c.ID {
			value = cred.PAT
			break
		}
	}
	if c.CredentialKind == "env" {
		value = os.Getenv(c.TokenEnv)
	}
	if !platform.ValidPAT(value) {
		return platform.Secret{}, errors.New("platform credential unavailable")
	}
	return platform.NewSecret(value), nil
}

func (file *Snapshot) removeCredential(connectionID string) {
	kept := file.Credentials[:0]
	for _, cred := range file.Credentials {
		if cred.ConnectionID != connectionID {
			kept = append(kept, cred)
		}
	}
	file.Credentials = kept
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
