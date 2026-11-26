//go:build darwin || linux

package room

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const hostedServiceSeed = `[server]
listen = "127.0.0.1:18088"
access_mode = "proxy"
public_base_url = "https://assistant.example.com"
trusted_proxies = ["127.0.0.1"]
[deployment]
mode = "multi-user"
[deployment.authentication]
provider = "opencsg"
allowed_subjects = ["server-proven-subject"]
`

func TestServiceInitialConfigAllowsExplicitOpenAuthentication(t *testing.T) {
	p := filepath.Join(t.TempDir(), "open.toml")
	serviceTestFile(t, p, strings.Replace(hostedServiceSeed, `allowed_subjects = ["server-proven-subject"]`, `allow_all_authenticated = true`, 1), 0600)
	if _, err := readServiceInitialConfig(p); err != nil {
		t.Fatal("explicit open admission was rejected", err)
	}
}

func TestServiceInitialConfigAcceptsDirectCasdoorWithoutEmbeddingSecret(t *testing.T) {
	home := t.TempDir()
	secret := filepath.Join(home, "client-secret")
	serviceTestFile(t, secret, "private-client-secret", 0600)
	seed := strings.Replace(hostedServiceSeed, `provider = "opencsg"`, `provider = "casdoor"`+"\nissuer = 'https://casdoor.example'\nclient_id = 'teamx-client'\nclient_secret_file = '"+filepath.ToSlash(secret)+"'", 1)
	path := filepath.Join(home, "seed.toml")
	serviceTestFile(t, path, seed, 0600)
	document, err := readServiceInitialConfig(path)
	if err != nil {
		t.Fatal("Casdoor installation seed rejected", err)
	}
	r := serviceRegistration{Root: t.TempDir(), Home: t.TempDir(), UID: os.Getuid(), GID: os.Getgid(), Execution: "srt"}
	if err := initializeServiceConfig(r, "/published/release", document); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.Home, "config.toml"))
	if err != nil || strings.Contains(string(data), "private-client-secret") {
		t.Fatal("installation copied raw client credentials into TOML", err)
	}
	c, err := loadStartupConfig(filepath.Join(r.Home, "config.toml"), true)
	if err != nil || c.Deployment.Authentication.Provider != "casdoor" || c.Deployment.Authentication.ClientSecretFile != secret {
		t.Fatal("installation did not preserve Casdoor settings", err)
	}
}

func TestServiceRegistrationIsReadableByOrdinaryCore(t *testing.T) {
	r := serviceRegistration{Root: t.TempDir(), Home: t.TempDir(), Execution: "srt", UID: os.Getuid()}
	if err := writeServiceRegistration(r); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(r.Root, "service.json"))
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("ordinary service account cannot read deployment selection", info, err)
	}
}

func TestServiceHostedBootstrapPreparesProtectedDataBeforeAnyUser(t *testing.T) {
	root := bundleTestRoot(t)
	home := t.TempDir()
	r := serviceRegistration{Root: root, Home: home, UID: os.Getuid(), GID: os.Getgid(), Execution: "srt"}
	seedPath := filepath.Join(t.TempDir(), "initial.toml")
	serviceTestFile(t, seedPath, hostedServiceSeed, 0600)
	seed, err := readServiceInitialConfig(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeServiceConfig(r, root, seed); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	for range 2 {
		if err := runBundle("init", []string{"--bundle-root", root, "--home", home}, &output, &output); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(filepath.Join(home, "data"))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatal("SRT protected data directory was not initialized privately", info, err)
		}
	}
	for _, name := range []string{"first-access.json", "personal-space-registration.json", "users.json", "spaces.json"} {
		if _, err := os.Lstat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatal("bootstrap created a business identity", name, err)
		}
	}
}

func TestServiceInitialConfigSelectsCompositionBeforeIdentityReservation(t *testing.T) {
	r := serviceRegistration{Root: t.TempDir(), Home: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
	r.Execution = "srt"
	input := filepath.Join(t.TempDir(), "initial.toml")
	serviceTestFile(t, input, hostedServiceSeed, 0600)
	seed, err := readServiceInitialConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeServiceConfig(r, "/published/release", seed); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.Home, "config.toml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c startupConfig
	if err := toml.Unmarshal(before, &c); err != nil {
		t.Fatal(err)
	}
	if c.Deployment.Mode != "multi-user" || c.Runtime == nil || c.Runtime.DSH.BinaryPath != filepath.Join(r.Root, "current/bin/dsh") || c.Access.LoginEnabled == nil || !*c.Access.LoginEnabled || c.Server.PublicBaseURL != "https://assistant.example.com" {
		t.Fatal("composition did not preserve managed runtime and authentication")
	}
	if err := initializeServiceConfig(r, "/published/release", seed); err != nil {
		t.Fatal("interrupted installation could not resume", err)
	}
	seed["deployment"].(map[string]any)["mode"] = "personal"
	if err := initializeServiceConfig(r, "/published/release", seed); err == nil {
		t.Fatal("existing composition rebound")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("reinstall changed existing settings")
	}
	if _, err := os.Stat(filepath.Join(r.Home, "first-access.json")); !os.IsNotExist(err) {
		t.Fatal("seed reserved a personal identity", err)
	}
	initialized, err := readStartupConfig(path, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareFirstAccess(initialized); err != nil {
		t.Fatal("managed account initialization failed", err)
	}
	if _, err := os.Stat(filepath.Join(r.Home, "first-access.json")); !os.IsNotExist(err) {
		t.Fatal("first init published a personal reservation", err)
	}
	if _, err := readAdminToken(adminTokenPath(initialized)); err != nil {
		t.Fatal("deployment admin credential missing", err)
	}
}

func TestServiceInitialConfigRejectsHostOverridesAndDamagedHomes(t *testing.T) {
	for name, data := range map[string]string{
		"runtime":           hostedServiceSeed + "[runtime.dsh]\nbinary_path='/arbitrary'\n",
		"environment":       hostedServiceSeed + "[env]\nKEY='private'\n",
		"roots":             "[server]\ndata_dir='/other'\n[deployment]\nmode='personal'\n",
		"execution":         hostedServiceSeed + "[deployment.execution]\nkind='native'\n",
		"missing-mode":      "[server]\nlisten='127.0.0.1:8088'\n",
		"public-listen":     "[server]\nlisten='0.0.0.0:8088'\n[deployment]\nmode='personal'\n",
		"missing-admission": "[deployment]\nmode='multi-user'\n[deployment.authentication]\nprovider='opencsg'\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "seed.toml")
			serviceTestFile(t, path, data, 0600)
			if _, err := readServiceInitialConfig(path); err == nil {
				t.Fatal("unsupported seed accepted")
			}
		})
	}
	r := serviceRegistration{Root: t.TempDir(), Home: t.TempDir(), UID: os.Getuid(), GID: os.Getgid()}
	path := filepath.Join(t.TempDir(), "seed.toml")
	serviceTestFile(t, path, hostedServiceSeed, 0600)
	seed, err := readServiceInitialConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeServiceConfig(r, "/release", seed); err == nil {
		t.Fatal("multi-user seed accepted native execution")
	}
	r.Execution = "srt"
	serviceTestFile(t, filepath.Join(r.Home, "users.json"), `{}`, 0600)
	if err := initializeServiceConfig(r, "/release", seed); err == nil {
		t.Fatal("missing configuration treated as fresh install")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readServiceInitialConfig(path); err == nil {
		t.Fatal("public seed accepted")
	}
}
