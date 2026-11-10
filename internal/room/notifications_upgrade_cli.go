package room

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform/lark"
)

// These explicit offline commands convert local notification settings/scopes.
// They never start collectors, query a provider, or change progress and receipts.
func runNotificationsUpgrade(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "upgrade" && args[0] != "upgrade-lark") {
		return fmt.Errorf("usage: teamx notifications upgrade|upgrade-lark --rooms-dir ROOT (service must be stopped)")
	}
	flags := flag.NewFlagSet("notifications upgrade", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("rooms-dir", "", "existing service root")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" || flags.NArg() != 0 {
		return ErrRequest
	}
	abs, err := filepath.Abs(*root)
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
	m := &roomManager{store: store}
	m.initPlatform()
	if err := readSnapshot(m.indexPath(), &m.index, maxRoomsFileBytes); err != nil {
		return err
	}
	if err := m.validateIndex(); err != nil {
		return err
	}
	if _, err := m.platforms.Load(); err != nil {
		return err
	}
	count, err := m.upgradeMemberNotificationsLocked()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Notification scope format: GitLab settings schema %d, sources schema 3; Lark sources schema %d; upgraded %d files. Subscription IDs, progress, frozen routes and receipts are preserved. Private .vN.bak backups and %s support recovery. After new deliveries, recover the latest complete state with a compatible binary; do not replace it with a pre-upgrade backup.\n", gitlabNotificationSchema, m.notificationAdapters[lark.MessagesSourceID].Source.Schema(), count, m.notificationUpgradeJournal())
	return err
}

func readPrivateNotificationJSON(path string) (json.RawMessage, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxGitLabConfigBytes {
		return nil, ErrInvalid
	}
	var data json.RawMessage
	if err := readSnapshot(path, &data, maxGitLabConfigBytes); err != nil {
		return nil, err
	}
	return data, nil
}

func decodeNotificationJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
