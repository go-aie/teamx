package connection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/go-aie/teamx/internal/platform"
)

// Upgrade is an explicit offline conversion, called while the service root is
// locked. It never resolves, validates with GitLab, or revokes an old token.
// Runtime readers accept only the current schema.
func (m *Manager) Upgrade() (int, error) {
	if err := m.store.Check(true); err != nil {
		return 0, err
	}
	info, err := os.Lstat(m.store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxPlatformConnectionsBytes {
		return 0, ErrInvalid
	}
	data, err := os.ReadFile(m.store.Path)
	if err != nil {
		return 0, err
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return 0, ErrInvalid
	}
	var version int
	if json.Unmarshal(raw["version"], &version) != nil || version < 1 || version > snapshotVersion {
		return 0, ErrInvalid
	}
	if version == snapshotVersion {
		_, err := m.Load()
		return 0, err
	}
	for key := range raw {
		switch key {
		case "version", "connections", "credentials", "installation_id", "oauth_applications", "oauth_credentials", "oauth_initializations", "oauth_client_imports", "oauth_cleanup":
		default:
			return 0, ErrInvalid
		}
	}
	var records []map[string]json.RawMessage
	if json.Unmarshal(raw["connections"], &records) != nil {
		return 0, ErrInvalid
	}
	count := 0
	for _, record := range records {
		var kind, provider string
		if json.Unmarshal(record["credential_kind"], &kind) != nil || json.Unmarshal(record["provider"], &provider) != nil {
			return 0, ErrInvalid
		}
		delete(record, "token_env")
		if kind == "pat" || kind == "env" {
			if provider != "gitlab" {
				return 0, ErrInvalid
			}
			var v, cv uint64
			if json.Unmarshal(record["version"], &v) != nil || json.Unmarshal(record["credential_version"], &cv) != nil || v == ^uint64(0) || cv == ^uint64(0) {
				return 0, ErrInvalid
			}
			putJSON(record, "version", v+1)
			putJSON(record, "credential_version", cv+1)
			putJSON(record, "credential_kind", "oauth")
			putJSON(record, "credential_configured", false)
			putJSON(record, "validation", platform.Validation{Status: "unverified"})
			delete(record, "credential_id")
			delete(record, "identity_binding")
			count++
		}
	}
	putJSON(raw, "connections", records)
	putJSON(raw, "version", snapshotVersion)
	delete(raw, "credentials")
	delete(raw, "oauth_initializations")
	// Remote integer IDs were needed only by the removed application creator.
	for _, key := range []string{"oauth_applications", "oauth_client_imports"} {
		if len(raw[key]) == 0 {
			continue
		}
		var values []map[string]json.RawMessage
		if json.Unmarshal(raw[key], &values) != nil {
			return 0, ErrInvalid
		}
		for _, value := range values {
			delete(value, "remote_id")
		}
		putJSON(raw, key, values)
	}
	data, err = json.Marshal(raw)
	if err != nil {
		return 0, ErrInvalid
	}
	var file Snapshot
	// Strict decoding and full validation happen before the single atomic write.
	if err = decodeUpgrade(data, &file); err != nil {
		return 0, err
	}
	if file.InstallationID == "" {
		file.InstallationID, err = newOpaqueID()
		if err != nil {
			return 0, err
		}
	}
	if _, err = m.validateSnapshot(file); err != nil {
		return 0, fmt.Errorf("%w: cannot upgrade platform connections", ErrInvalid)
	}
	return count, m.save(file)
}

func putJSON(m map[string]json.RawMessage, key string, value any) { m[key], _ = json.Marshal(value) }

func decodeUpgrade(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(value) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
