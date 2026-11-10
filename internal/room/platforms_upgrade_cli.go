package room

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
)

func runPlatformsUpgrade(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "upgrade" {
		return fmt.Errorf("usage: teamx platforms upgrade --rooms-dir ROOT (service must be stopped)")
	}
	flags := flag.NewFlagSet("platforms upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("rooms-dir", "", "existing service root")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" || flags.NArg() != 0 {
		return ErrRequest
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	lock, err := lockDirectory(abs)
	if err != nil {
		return err
	}
	store := &Store{dir: abs, lock: lock, ops: defaultSnapshotOps()}
	defer store.Close()
	if resumed, err := resumePlatformUpgrade(store); resumed || err != nil {
		return err
	}
	candidates := map[string]json.RawMessage{}
	capture := func(path string, value any, limit int64) error {
		b, err := json.Marshal(value)
		if int64(len(b)) > limit {
			return ErrCapacity
		}
		if err == nil {
			candidates[path] = b
		}
		return err
	}
	m := registry.New(registry.Store{Path: filepath.Join(abs, "platform-connections.json"), Check: store.check, Save: capture}, func(name, provider, origin string) (string, string, error) {
		if _, ok := platform.LookupType(provider); !ok {
			return "", "", registry.ErrRequest
		}
		return registry.Normalize(name, origin)
	})
	m.SetIdentityResolver(func(provider, origin string, v platform.Validation) (platform.AccountIdentity, bool) {
		if provider == "gitlab" {
			return gitlab.LegacyAccountIdentity(origin, v)
		}
		return v.Account, v.Account.Valid()
	})
	m.SetProfileSupport(func(p string) bool { return p == "lark" || p == "scion" }, func(provider, origin string, p platform.CLIProfile) bool {
		return provider == "lark" && lark.ValidProfile(p) && origin == lark.Origin(p.Brand) || provider == "scion" && scion.ValidProfile(p)
	}, nil)
	count, err := m.Upgrade()
	if err != nil {
		return err
	}
	if err := upgradePlatformModel(store, candidates); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Platform configuration version %d; %d legacy platforms require GitLab login. Platform IDs and existing authorizations are preserved. Any upgraded snapshot has a private platform-connections.json.vN.bak backup beside it. Room platform references are converted without changing notification progress or tool authorization. platform-model-upgrade.json retains the private preimages and recovery status.\n", registry.ConfigurationVersion, count)
	return err
}
