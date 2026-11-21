package room

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/go-aie/teamx/internal/app/platforms"
	"io"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func runPlatformsUpgradeWithCatalog(args []string, stdout, stderr io.Writer, assembly *platforms.Catalog) error {
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
	store := &Store{catalog: assembly, dir: abs, lock: lock, snapshotWriter: snapshotWriter{ops: defaultSnapshotOps()}}
	defer store.Close()
	if resumed, err := resumePlatformUpgrade(store); resumed || err != nil {
		return err
	}
	catalog := assembly.Types()
	connections := make(map[string]connectionProvider)
	for _, d := range assembly.Definitions() {
		if d.Connection != nil {
			c := d.Connection
			connections[d.Type.Provider] = connectionProvider{Normalize: c.Normalize, LegacyIdentity: c.LegacyIdentity, ValidProfile: c.ValidProfile}
		}
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
		if connections[provider].Normalize == nil {
			return "", "", registry.ErrRequest
		}
		return connections[provider].Normalize(name, origin)
	}, catalog)
	m.SetIdentityResolver(func(provider, origin string, v platform.Validation) (platform.AccountIdentity, bool) {
		if resolve := connections[provider].LegacyIdentity; resolve != nil {
			return resolve(origin, v)
		}
		return v.Account, v.Account.Valid()
	})
	m.SetProfileSupport(func(p string) bool { return connections[p].ValidProfile != nil }, func(provider, origin string, p platform.CLIProfile) bool {
		valid := connections[provider].ValidProfile
		return valid != nil && valid(origin, p)
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
