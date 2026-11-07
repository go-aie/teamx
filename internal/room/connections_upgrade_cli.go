package room

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func runConnectionsUpgrade(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "upgrade" {
		return fmt.Errorf("usage: teamx connections upgrade --rooms-dir ROOT (service must be stopped)")
	}
	flags := flag.NewFlagSet("connections upgrade", flag.ContinueOnError)
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
	m := connection.New(connection.Store{Path: filepath.Join(abs, "platform-connections.json"), Check: store.check, Save: store.save}, func(name, provider, origin string) (string, string, error) {
		if provider != "gitlab" && provider != "lark" {
			return "", "", connection.ErrRequest
		}
		return connection.Normalize(name, origin)
	})
	m.SetIdentityResolver(func(provider, origin string, v platform.Validation) (platform.AccountIdentity, bool) {
		if provider == "gitlab" {
			return gitlab.LegacyAccountIdentity(origin, v)
		}
		return v.Account, v.Account.Valid()
	})
	m.SetProfileSupport(func(p string) bool { return p == "lark" }, func(provider, origin string, p platform.CLIProfile) bool {
		return provider == "lark" && lark.ValidProfile(p) && origin == lark.Origin(p.Brand)
	}, nil)
	count, err := m.Upgrade()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Connections upgraded to OAuth-only format; %d connections require GitLab login. Connection IDs and Room references are preserved.\n", count)
	return err
}
