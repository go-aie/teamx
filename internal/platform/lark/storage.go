package lark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

type cliStorage struct{ root, executable string }

// WithStorage binds a trusted Space root and deployment executable. Profiles
// are opaque references; neither browser nor Agent supplies filesystem paths.
// Each newly configured profile owns one stable config/data pair. Explicit
// references to the same profile share its lifecycle within this Space only.
func (f *ClientFactory) WithStorage(root, executable string) (*ClientFactory, error) {
	if !filepath.IsAbs(root) || !filepath.IsAbs(executable) {
		return nil, failure("invalid_cli_storage")
	}
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, failure("invalid_cli_storage")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, failure("invalid_cli_storage")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil || privateCLIDirectory(root) != nil {
		return nil, failure("invalid_cli_storage")
	}
	if f == nil {
		f = NewClientFactory(AccountRequestInterval, 0, 0)
	}
	return &ClientFactory{requests: f.requests, selfSend: f.selfSend, storage: &cliStorage{root, executable}}, nil
}

func privateCLIDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return failure("invalid_cli_storage")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return failure("invalid_cli_storage")
	}
	return nil
}

func (s *cliStorage) directory(profile string) string {
	sum := sha256.Sum256([]byte(profile))
	return filepath.Join(s.root, hex.EncodeToString(sum[:]))
}

func (s *cliStorage) provision(p platform.CLIProfile) error {
	if !ValidProfile(p) || p.Executable != s.executable || privateCLIDirectory(s.root) != nil {
		return failure("invalid_cli_storage")
	}
	dir := s.directory(p.Profile)
	if err := os.Mkdir(dir, 0700); err != nil {
		return failure("profile_conflict") // never overwrite an old context
	}
	for _, name := range []string{"config", "data"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			return failure("invalid_cli_storage")
		}
	}
	data, _ := json.Marshal(p)
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), data, 0600); err != nil {
		return failure("invalid_cli_storage")
	}
	return nil
}

func (s *cliStorage) bind(c *Client) error {
	dir := s.directory(c.profile.Profile)
	for _, path := range []string{s.root, dir, filepath.Join(dir, "config"), filepath.Join(dir, "data")} {
		if privateCLIDirectory(path) != nil {
			return failure("invalid_cli_storage")
		}
	}
	var saved platform.CLIProfile
	data, err := os.ReadFile(filepath.Join(dir, "profile.json"))
	if err != nil || len(data) > 4096 || json.Unmarshal(data, &saved) != nil || saved != c.profile || saved.Executable != s.executable {
		return failure("invalid_cli_storage")
	}
	c.configDir, c.dataDir = filepath.Join(dir, "config"), filepath.Join(dir, "data")
	c.storage = s
	return nil
}

func (f *ClientFactory) SupportsPin() bool {
	return supportsLegacyPin() || f != nil && f.storage != nil && supportsLinuxPin()
}

func (f *ClientFactory) discoverScoped(ctx context.Context, executable, brand string) (Discovery, error) {
	result := Discovery{ManagedStorage: true, Executable: f.storage.executable, Profiles: []ProfileOption{}, SetupProfile: "teamx-" + brand}
	if Origin(brand) == "" || executable != "" && executable != f.storage.executable || privateCLIDirectory(f.storage.root) != nil {
		return result, failure("invalid_cli_storage")
	}
	// Version discovery has an isolated empty context too; it cannot inspect
	// the deployment account's native CLI configuration.
	private, err := os.MkdirTemp("", "teamx-lark-discovery-")
	if err != nil {
		return result, failure("invalid_cli_storage")
	}
	defer os.RemoveAll(private)
	c := &Client{profile: platform.CLIProfile{Executable: f.storage.executable}, configDir: private, dataDir: private, requests: f.requests, selfSend: f.selfSend, gates: &f.gates}
	version, err := c.run(ctx, "--version")
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(string(version)) != "lark-cli version "+CLIVersion {
		return result, failure("cli_version_mismatch")
	}
	root, err := os.Open(f.storage.root)
	if err != nil {
		return result, failure("invalid_cli_storage")
	}
	defer root.Close()
	entries, err := root.ReadDir(129)
	if err != nil && !errors.Is(err, io.EOF) {
		return result, failure("invalid_cli_storage")
	}
	if len(entries) > 128 {
		return result, failure("configuration_capacity")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return result, failure("invalid_cli_storage")
		}
		var p platform.CLIProfile
		data, err := os.ReadFile(filepath.Join(f.storage.root, entry.Name(), "profile.json"))
		if err != nil || len(data) > 4096 || json.Unmarshal(data, &p) != nil || !ValidProfile(p) || filepath.Base(f.storage.directory(p.Profile)) != entry.Name() {
			return result, failure("invalid_cli_storage")
		}
		if p.Brand != brand {
			continue
		}
		client, err := f.New(p)
		if err != nil {
			return result, err
		}
		unlock, err := client.lock(ctx)
		if err != nil {
			return result, err
		}
		profiles, err := client.configurationProfiles(ctx)
		unlock()
		if err != nil {
			return result, err
		}
		for _, profile := range profiles {
			if profile.Name != p.Profile || profile.Brand != p.Brand {
				return result, failure("identity_mismatch")
			}
			result.Profiles = append(result.Profiles, ProfileOption{Name: profile.Name, Brand: profile.Brand, AppID: profile.AppID, User: profile.User})
		}
	}
	return result, nil
}
