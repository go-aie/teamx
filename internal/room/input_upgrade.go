package room

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/inputstream"
)

const (
	inputUpgradeDirectory = "input-upgrade"
	inputUpgradeJournal   = "input-upgrade.json"
	inputUpgradeLimit     = 128 << 20
	upgradeRoomFile       = "room/room.json"
)

type inputUpgradeOptions struct {
	expected RoomIdentity
	managed  *managedRoomConfig
	catalog  string
	log      io.Writer
	stop     func(string) error
	space    func(string) (uint64, error)
}

type inputUpgradeMarker struct {
	Version int `json:"version"`
	RoomIdentity
	Upgrade string `json:"input_upgrade"`
}

type inputUpgradeFile struct {
	Bytes  int64  `json:"bytes"`
	Digest string `json:"sha256"`
}

type inputUpgradeProgress struct {
	Version int `json:"version"`
	RoomIdentity
	Phase    string                      `json:"phase"`
	ID       string                      `json:"id"`
	Source   map[string]inputUpgradeFile `json:"source,omitempty"`
	Target   map[string]inputUpgradeFile `json:"target,omitempty"`
	Remove   []string                    `json:"remove,omitempty"`
	Checksum string                      `json:"checksum"`
}

func (p inputUpgradeProgress) checksum() string {
	p.Checksum = ""
	return inputstream.Ref("", p)
}

type inputUpgrade struct {
	store    *Store
	options  inputUpgradeOptions
	progress inputUpgradeProgress
}

// EnsureInputFormat runs under the original Store lock before any Room is
// opened. The Room header fences old binaries during publication.
func (s *Store) EnsureInputFormat(options inputUpgradeOptions) error {
	if err := s.check(true); err != nil {
		return err
	}
	u := inputUpgrade{store: s, options: options}
	if err := u.run(); err != nil {
		return fmt.Errorf("Room %s 输入格式升级失败（%s）\n%w。修复后重新启动即可继续", s.roomID, s.dir, err)
	}
	return nil
}

func (u *inputUpgrade) checkpoint(point string) error {
	if u.options.stop != nil {
		return u.options.stop(point)
	}
	return nil
}

func (u *inputUpgrade) log(format string, args ...any) {
	if u.options.log != nil {
		fmt.Fprintf(u.options.log, format+"\n", args...)
	}
}

func (u *inputUpgrade) journal() string    { return filepath.Join(u.store.dir, inputUpgradeJournal) }
func (u *inputUpgrade) workspace() string  { return filepath.Join(u.store.dir, inputUpgradeDirectory) }
func (u *inputUpgrade) generation() string { return filepath.Join(u.workspace(), u.progress.ID) }
func (u *inputUpgrade) backup() string     { return filepath.Join(u.generation(), "backup") }
func (u *inputUpgrade) stage() string      { return filepath.Join(u.generation(), "staging") }

func (u *inputUpgrade) live(name string) string {
	scope, relative, _ := strings.Cut(name, "/")
	root := u.store.dir
	if scope == "catalog" {
		root = u.options.catalog
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

func (u *inputUpgrade) validPath(name string) bool {
	if !filepath.IsLocal(name) || filepath.ToSlash(filepath.Clean(name)) != name {
		return false
	}
	parts := strings.Split(name, "/")
	if parts[0] == "catalog" {
		return u.options.catalog != "" && len(parts) == 3 && (slices.Contains(upgradeNoticeDirectories, parts[1]) || parts[1] == "platform-connect" || parts[1] == "platform-selections") && strings.HasSuffix(parts[2], ".json")
	}
	if parts[0] != "room" || len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "room.json", "human-read.json":
		return len(parts) == 2
	case "inboxes", "room-input", "lark-conversations":
		return len(parts) >= 3
	case "agents":
		if len(parts) < 4 || !safeMembershipID(parts[2]) {
			return false
		}
		switch parts[3] {
		case "agent.json", "capacity.json", "collaboration.json":
			return len(parts) == 4
		case "followups", "actions", "task-events":
			return len(parts) >= 5
		}
	}
	return false
}

func (u *inputUpgrade) validateProgress() error {
	p := u.progress
	if p.Version != 1 || p.RoomID != u.store.roomID || !validText(p.RoomInstance) ||
		p.Checksum != p.checksum() || len(p.ID) != 64 || strings.Trim(p.ID, "0123456789abcdef") != "" {
		return fmt.Errorf("%w: input upgrade journal checksum or identity", ErrInvalid)
	}
	switch p.Phase {
	case "completed":
		if len(p.Source)+len(p.Target)+len(p.Remove) == 0 {
			return nil
		}
	case "backup", "prepared":
		if p.Source[upgradeRoomFile].Digest == "" || p.ID != inputstream.Ref("", p.RoomIdentity, p.Source) ||
			p.Phase == "backup" && (len(p.Target) != 0 || len(p.Remove) != 0) ||
			p.Phase == "prepared" && p.Target[upgradeRoomFile].Digest == "" {
			break
		}
		for _, files := range []map[string]inputUpgradeFile{p.Source, p.Target} {
			for name, file := range files {
				if !u.validPath(name) || file.Bytes < 0 || len(file.Digest) != 64 || strings.Trim(file.Digest, "0123456789abcdef") != "" {
					return fmt.Errorf("%w: invalid upgrade manifest entry %s", ErrInvalid, name)
				}
			}
		}
		for _, name := range p.Remove {
			if name == upgradeRoomFile || p.Source[name].Digest == "" || p.Target[name].Digest != "" {
				return fmt.Errorf("%w: invalid upgrade removal %s", ErrInvalid, name)
			}
		}
		return nil
	}
	return fmt.Errorf("%w: invalid input upgrade phase %q", ErrInvalid, p.Phase)
}

func (u *inputUpgrade) saveProgress() error {
	u.progress.Checksum = u.progress.checksum()
	return u.store.save(u.journal(), u.progress, inputUpgradeLimit)
}

func (u *inputUpgrade) run() error {
	if info, err := os.Lstat(u.journal()); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%w: input upgrade journal is not a regular file", ErrInvalid)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err := readSnapshot(u.journal(), &u.progress, inputUpgradeLimit)
	hasProgress := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !hasProgress {
		if _, err := os.Lstat(u.workspace()); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: input upgrade workspace has no progress journal (%v)", ErrInvalid, err)
		}
	} else {
		if err := u.validateProgress(); err != nil {
			return err
		}
		if u.progress.Phase != "completed" {
			for _, path := range []string{u.workspace(), u.generation(), u.backup(), u.stage()} {
				if err := checkInputUpgradeDirectory(path); err != nil {
					return err
				}
			}
		}
	}
	room, marker, err := u.readRoomHeader()
	if errors.Is(err, os.ErrNotExist) && !hasProgress && u.options.expected == (RoomIdentity{}) {
		return nil // OpenStore independently checks for orphaned Room data.
	}
	if err != nil {
		return err
	}
	id := room.RoomIdentity
	if marker != nil {
		id = marker.RoomIdentity
	}
	if id.RoomID != u.store.roomID || u.options.expected != (RoomIdentity{}) && id != u.options.expected ||
		hasProgress && id != u.progress.RoomIdentity {
		return ErrIdentity
	}
	if marker != nil && (!hasProgress || u.progress.Phase != "prepared" || marker.Upgrade != u.progress.ID) {
		return fmt.Errorf("%w: Room upgrade marker has no matching prepared journal", ErrInvalid)
	}
	if hasProgress && u.progress.Phase == "completed" {
		if room.InputLog == nil {
			return fmt.Errorf("%w: completed input upgrade missing published Room", ErrInvalid)
		}
		return nil
	}
	if room.InputLog != nil && !hasProgress {
		return nil
	}
	if marker == nil && room.InputLog == nil {
		if err := room.validate(); err != nil {
			return err
		}
		if u.options.managed != nil {
			if err := checkManagedSnapshotIdentity(*u.options.managed, room); err != nil {
				return err
			}
		}
		if len(room.EventReceipts) != 0 {
			return fmt.Errorf("%w: new input state in legacy Room", ErrVersion)
		}
		if err := u.prepare(room, hasProgress); err != nil {
			return err
		}
		if err := u.store.save(u.live(upgradeRoomFile), inputUpgradeMarker{formatVersion, id, u.progress.ID}, maxRoomFileBytes); err != nil {
			return err
		}
		if err := u.checkpoint("header"); err != nil {
			return err
		}
	}
	if u.progress.Phase != "prepared" {
		return fmt.Errorf("%w: Room published before upgrade was prepared", ErrInvalid)
	}
	if room.InputLog != nil && marker == nil {
		if err := u.verifyBackup(false); err != nil {
			return err
		}
		if err := u.verifyPublished(true); err != nil {
			return err
		}
	} else if err := u.publish(); err != nil {
		return err
	}
	if err := u.checkpoint("room-published"); err != nil {
		return err
	}
	if err := u.clean(); err != nil {
		return err
	}
	u.progress.Phase = "completed"
	u.progress.Source, u.progress.Target, u.progress.Remove = nil, nil, nil
	if err := u.saveProgress(); err != nil {
		return err
	}
	if err := u.checkpoint("completed"); err != nil {
		return err
	}
	u.log("Room %s 输入格式升级完成，备份保留在 %s", id.RoomID, u.backup())
	return nil
}

func (u *inputUpgrade) readRoomHeader() (RoomSnapshot, *inputUpgradeMarker, error) {
	var room RoomSnapshot
	path := u.live(upgradeRoomFile)
	if err := readSnapshot(path, &room, maxRoomFileBytes); err != nil {
		if errors.Is(err, ErrInvalid) {
			var marker inputUpgradeMarker
			if readSnapshot(path, &marker, maxPrivateFileBytes) == nil && marker.Version == formatVersion && marker.Upgrade != "" {
				return RoomSnapshot{}, &marker, nil
			}
		}
		return room, nil, err
	}
	if err := validateHeader(room.Version, room.RoomIdentity); err != nil {
		return room, nil, err
	}
	return room, nil, nil
}

func (u *inputUpgrade) prepare(room RoomSnapshot, hasProgress bool) error {
	if _, err := os.Lstat(filepath.Join(u.store.dir, "room-input")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: legacy Room already has room-input files", ErrInvalid)
	}
	for _, member := range room.Members {
		if err := u.checkLegacySession(MemberIdentity{room.RoomIdentity, member.AgentID, member.MembershipID}); err != nil {
			return err
		}
	}
	source, err := u.inventory(room)
	if err != nil {
		return err
	}
	if hasProgress {
		if err := u.verifyBackup(u.progress.Phase == "backup"); err != nil {
			return err
		}
	}
	if !hasProgress || !reflect.DeepEqual(source, u.progress.Source) {
		u.progress = inputUpgradeProgress{Version: 1, RoomIdentity: room.RoomIdentity, Phase: "backup", Source: source,
			ID: inputstream.Ref("", room.RoomIdentity, source)}
		if err := u.saveProgress(); err != nil {
			return err
		}
	}
	if u.progress.Phase == "backup" {
		u.log("Room %s 输入格式升级，备份路径 %s", room.RoomID, u.backup())
		if err := os.RemoveAll(u.stage()); err != nil {
			return err
		}
		if err := u.fillBackup(); err != nil {
			return err
		}
		manifest := filepath.Join(u.generation(), "backup-manifest.json")
		if _, err := os.Lstat(manifest); errors.Is(err, os.ErrNotExist) {
			if err := u.store.save(manifest, u.progress.Source, inputUpgradeLimit); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		u.log("Room %s 备份校验通过，开始转换输入", room.RoomID)
		if err := u.checkpoint("backup-ready"); err != nil {
			return err
		}
		if err := u.convert(); err != nil {
			return err
		}
		if err := u.checkpoint("converted"); err != nil {
			return err
		}
		u.progress.Phase = "prepared"
		if err := u.saveProgress(); err != nil {
			return err
		}
	}
	if err := u.validateProgress(); err != nil {
		return err
	}
	if err := u.verifyStage(); err != nil {
		return err
	}
	again, err := u.inventory(room)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(again, source) {
		return fmt.Errorf("%w: source changed before upgrade header; restart to back up the updated Room", ErrConflict)
	}
	return u.checkpoint("prepared")
}

func (u *inputUpgrade) fillBackup() error {
	space := u.options.space
	if space == nil {
		space = inputUpgradeAvailableSpace
	}
	free, err := space(u.store.dir)
	if err != nil {
		return err
	}
	var need uint64
	for name, file := range u.progress.Source {
		need += uint64(file.Bytes) // Temporary target tree.
		if _, err := os.Lstat(filepath.Join(u.backup(), name)); errors.Is(err, os.ErrNotExist) {
			// A retained legacy Inbox file occupies a block even if its JSON is small.
			need += max(4096, (uint64(file.Bytes)+4095)/4096*4096)
		} else if err != nil {
			return err
		}
	}
	if need > free {
		return fmt.Errorf("磁盘空间不足，升级预估需要 %d 字节，可用 %d 字节", need, free)
	}
	existingDirs := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(u.progress.Source)) {
		info := u.progress.Source[name]
		dest := filepath.Join(u.backup(), name)
		if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
			if err := copyInputUpgradeFile(u.live(name), dest, info); err != nil {
				return err
			}
			if err := u.checkpoint("backup-file"); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			existingDirs[filepath.Dir(dest)] = true
		}
	}
	for dir := range existingDirs {
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	return u.verifyBackupFiles(false)
}

func (u *inputUpgrade) verifyBackup(incomplete bool) error {
	var saved map[string]inputUpgradeFile
	path := filepath.Join(u.generation(), "backup-manifest.json")
	err := readSnapshot(path, &saved, inputUpgradeLimit)
	switch {
	case err == nil && reflect.DeepEqual(saved, u.progress.Source):
		incomplete = false
	case incomplete && errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("%w: upgrade backup manifest %s (%v)", ErrInvalid, path, err)
	}
	return u.verifyBackupFiles(incomplete)
}

func (u *inputUpgrade) verifyBackupFiles(incomplete bool) error {
	for name, want := range u.progress.Source {
		path := filepath.Join(u.backup(), name)
		got, err := hashInputUpgradeFile(path)
		if incomplete && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || got != want {
			return fmt.Errorf("%w: upgrade backup differs from manifest: %s (%v)", ErrInvalid, path, err)
		}
	}
	return nil
}

func (u *inputUpgrade) verifyStage() error {
	for name, want := range u.progress.Target {
		got, err := hashInputUpgradeFile(filepath.Join(u.stage(), name))
		if err != nil || got != want {
			return fmt.Errorf("%w: upgrade staged file %s (%v)", ErrInvalid, name, err)
		}
	}
	return nil
}

func (u *inputUpgrade) publish() error {
	if err := u.verifyBackup(false); err != nil {
		return err
	}
	if err := u.verifyStage(); err != nil {
		return err
	}
	existingDirs := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(u.progress.Target)) {
		if name == upgradeRoomFile {
			continue
		}
		want := u.progress.Target[name]
		dest := u.live(name)
		have, err := hashInputUpgradeFile(dest)
		if err == nil && have == want {
			existingDirs[filepath.Dir(dest)] = true
			continue
		}
		before, existed := u.progress.Source[name]
		if !(existed && err == nil && have == before || !existed && errors.Is(err, os.ErrNotExist)) {
			return fmt.Errorf("%w: upgrade destination changed: %s (%v)", ErrInvalid, dest, err)
		}
		if err := copyInputUpgradeFile(filepath.Join(u.stage(), name), dest, want); err != nil {
			return err
		}
		if err := u.checkpoint("replace-file"); err != nil {
			return err
		}
	}
	for _, name := range u.progress.Remove {
		path := u.live(name)
		have, err := hashInputUpgradeFile(path)
		if errors.Is(err, os.ErrNotExist) {
			existingDirs[filepath.Dir(path)] = true
			continue
		}
		if err != nil || have != u.progress.Source[name] {
			return fmt.Errorf("%w: legacy input changed: %s (%v)", ErrInvalid, path, err)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		if err := u.checkpoint("remove-file"); err != nil {
			return err
		}
	}
	// A prior process could have exited after rename/unlink but before its
	// directory sync. Reassert those durability boundaries before the Room header.
	for dir := range existingDirs {
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	if err := u.verifyPublished(false); err != nil {
		return err
	}
	return copyInputUpgradeFile(filepath.Join(u.stage(), upgradeRoomFile), u.live(upgradeRoomFile), u.progress.Target[upgradeRoomFile])
}

func (u *inputUpgrade) verifyPublished(roomPublished bool) error {
	removed := make(map[string]bool, len(u.progress.Remove))
	for _, name := range u.progress.Remove {
		removed[name] = true
	}
	for name, want := range u.progress.Source {
		if _, updated := u.progress.Target[name]; updated || removed[name] {
			continue
		}
		have, err := hashInputUpgradeFile(u.live(name))
		if err != nil || have != want {
			return fmt.Errorf("%w: unchanged upgrade source %s (%v)", ErrInvalid, name, err)
		}
	}
	for name, want := range u.progress.Target {
		if name == upgradeRoomFile && !roomPublished {
			continue
		}
		have, err := hashInputUpgradeFile(u.live(name))
		if err != nil || have != want {
			return fmt.Errorf("%w: published upgrade file %s (%v)", ErrInvalid, name, err)
		}
	}
	for _, name := range u.progress.Remove {
		if _, err := os.Lstat(u.live(name)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: legacy upgrade file still exists: %s", ErrInvalid, name)
		}
	}
	return nil
}

func (u *inputUpgrade) clean() error {
	if err := os.RemoveAll(u.stage()); err != nil {
		return err
	}
	entries, err := os.ReadDir(u.workspace())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != u.progress.ID && len(e.Name()) == 64 && strings.Trim(e.Name(), "0123456789abcdef") == "" {
			if err := os.RemoveAll(filepath.Join(u.workspace(), e.Name())); err != nil {
				return err
			}
		}
	}
	return syncDirectory(u.workspace())
}

func checkInputUpgradeDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil && !info.IsDir() {
		return fmt.Errorf("%w: input upgrade path is not a directory: %s", ErrInvalid, path)
	}
	return err
}

func hashInputUpgradeFile(path string) (inputUpgradeFile, error) {
	var result inputUpgradeFile
	info, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return result, fmt.Errorf("%w: not a regular upgrade file: %s", ErrInvalid, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n != info.Size() {
		if err == nil {
			err = ErrConflict
		}
		return result, err
	}
	return inputUpgradeFile{n, hex.EncodeToString(h.Sum(nil))}, nil
}

// Stream to a sibling temporary file, then durably replace in one rename.
func copyInputUpgradeFile(source, dest string, want inputUpgradeFile) error {
	dir := filepath.Dir(dest)
	if err := makeDurableDir(dir); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".snapshot-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, want.Bytes+1))
	if err == nil && (n != want.Bytes || hex.EncodeToString(h.Sum(nil)) != want.Digest) {
		err = fmt.Errorf("%w: upgrade copy checksum for %s", ErrInvalid, source)
	}
	if err == nil {
		err = out.Sync()
	}
	if err = errors.Join(err, out.Close()); err != nil {
		return err
	}
	if err := os.Rename(out.Name(), dest); err != nil {
		return err
	}
	return syncDirectory(dir)
}

// Managed Rooms complete the format transition before collectors, HTTP
// admission or lazy runtime restore. The manager already holds the catalog lock.
func (m *roomManager) upgradeRoomInputs(options inputUpgradeOptions) error {
	for _, entry := range m.index.Rooms {
		switch entry.Status {
		case "ready":
		case "creating":
			if _, err := os.Lstat(filepath.Join(m.roomDir(entry.RoomID), "room.json")); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
		default:
			continue
		}
		options.expected = entry.RoomIdentity
		var config managedRoomConfig
		if entry.Initialization != nil {
			config = entry.Initialization.Config
		} else if err := readSnapshot(filepath.Join(m.roomDir(entry.RoomID), "room-config.json"), &config, maxPrivateFileBytes); err != nil {
			return err
		}
		options.managed = &config
		store, err := lockStore(m.roomDir(entry.RoomID), entry.RoomID)
		if err != nil {
			return err
		}
		err = store.EnsureInputFormat(options)
		if err := errors.Join(err, store.Close()); err != nil {
			return err
		}
	}
	return nil
}
