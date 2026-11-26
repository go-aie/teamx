package room

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Caller owns the home lock. This only renames the former direct-OIDC provider;
// the retired broker protocol is never selected or used for identity matching.
func migrateOpenCSGConfig(c startupConfig) (startupConfig, error) {
	if !c.legacyOpenCSGProvider {
		return c, nil
	}
	if _, err := c.Deployment.Authentication.provider(); err != nil {
		return c, err
	}
	var document map[string]any
	if err := toml.Unmarshal(c.source, &document); err != nil {
		return c, errors.New("cannot parse authentication configuration for migration")
	}
	deployment, _ := document["deployment"].(map[string]any)
	authentication, _ := deployment["authentication"].(map[string]any)
	if authentication["provider"] != "casdoor" {
		return c, ErrConflict
	}
	authentication["provider"] = "opencsg"
	section, err := toml.Marshal(authentication)
	if err != nil {
		return c, err
	}
	source, err := removeConfigSubtrees(c.source, []string{"deployment", "authentication"})
	if err != nil {
		return c, err
	}
	next := append(append(source, []byte("\n[deployment.authentication]\n")...), section...)
	var after map[string]any
	if toml.Unmarshal(next, &after) != nil || !reflect.DeepEqual(document, after) {
		return c, errors.New("authentication migration changed unrelated configuration")
	}
	if err := writePrivateConfig(c.path, c.source, next); err != nil {
		return c, err
	}
	data, file, err := readSettingsFile(c.path)
	if err != nil || !bytes.Equal(data, next) {
		return c, ErrConflict
	}
	c.source, c.file, c.legacyOpenCSGProvider = data, file, false
	return c, nil
}

// Called under the identity lock after validating the complete saved directory.
// A single atomic snapshot preserves local IDs, status, auth versions and Space
// reservations. Interrupted saves are retried on restart before login is opened.
func (u *hostedUsers) migrateOpenCSGProvider(c externalAuthConfig) error {
	if c.Provider != "opencsg" || u.saved.Source == ([2]string{}) {
		return nil
	}
	issuer := strings.TrimRight(c.Issuer, "/")
	if u.saved.Source[1] != issuer || u.saved.Source[0] != "opencsg" && u.saved.Source[0] != "casdoor" {
		return errors.New("authentication source changed; explicit identity migration required")
	}
	if u.saved.Source[0] == "opencsg" {
		return nil
	}
	next := u.saved
	next.Source[0] = "opencsg"
	next.Users = slices.Clone(next.Users)
	for i := range next.Users {
		next.Users[i].Identity.Provider = "opencsg"
	}
	if err := u.writer.save(filepath.Join(u.home, "users.json"), next, maxPrivateFileBytes); err != nil {
		return err
	}
	u.saved = next
	return nil
}
