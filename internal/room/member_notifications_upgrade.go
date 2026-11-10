package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const memberNotificationsJournal = "member-notifications-upgrade.json"
const larkSearchUpgradeJournal = "lark-search-upgrade.json"

func (m *roomManager) notificationUpgradeJournal() string {
	if adapter, ok := m.notificationAdapters[lark.MessagesSourceID]; ok && adapter.Source.Schema() == lark.SearchSchema {
		return larkSearchUpgradeJournal
	}
	return memberNotificationsJournal
}

type notificationEnvelope struct {
	notification.SourceID
	Schema int             `json:"schema"`
	State  json.RawMessage `json:"state"`
}

func checkMemberNotificationsUpgrade(root string) error {
	for _, name := range []string{memberNotificationsJournal, larkSearchUpgradeJournal} {
		var j platformUpgrade
		err := readLarkUpgradeJournal(filepath.Join(root, name), &j)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if j.Version != 1 || !j.Complete {
			return fmt.Errorf("%w: notification scope upgrade incomplete (%s); stop service and run teamx notifications upgrade --rooms-dir ROOT using the version that started the upgrade", ErrVersion, name)
		}
	}
	return nil
}

// Only this offline boundary decodes the old writable formats. Both providers'
// envelope versions advance, including directories containing only Lark.
func (m *roomManager) convertNotificationSource(owner RoomIdentity, path string, before json.RawMessage) (json.RawMessage, notification.State, error) {
	var wire notificationEnvelope
	if err := decodeNotificationJSON(before, &wire); err != nil {
		return nil, nil, err
	}
	adapter, ok := m.notificationAdapters[wire.SourceID]
	if !ok {
		return nil, nil, ErrInvalid
	}
	if wire.Schema != adapter.Source.Schema() && wire.Schema != 2 && !(wire.SourceID == lark.MessagesSourceID && wire.Schema == 3 && adapter.Source.Schema() == lark.SearchSchema) && !(wire.SourceID == lark.MessagesSourceID && wire.Schema == 1) {
		return nil, nil, ErrVersion
	}
	raw := wire.State
	var err error
	if wire.SourceID == lark.MessagesSourceID && wire.Schema == 1 {
		raw, err = lark.UpgradeState(raw, owner)
		if err != nil {
			return nil, nil, err
		}
	}
	if wire.SourceID == lark.MessagesSourceID && adapter.Source.Schema() == lark.SearchSchema && wire.Schema != lark.SearchSchema {
		raw, err = lark.UpgradeSearchState(raw, 3, owner)
		if err != nil {
			return nil, nil, err
		}
	}
	state, err := adapter.Source.Decode(raw, owner)
	if err != nil || state == nil || state.Validate(owner) != nil {
		return nil, nil, ErrInvalid
	}
	meta := state.Metadata()
	target, err := meta.Target.Normalize()
	if err != nil || target != meta.Target || meta.Key.Source != wire.SourceID || filepath.Base(path) != wire.Provider+"."+wire.Channel+"."+meta.Key.SubscriptionID+".json" {
		return nil, nil, ErrInvalid
	}
	limits := notification.DefaultLimits()
	if int64(len(before)) > limits.Bytes || meta.Observed > limits.Observed || meta.Pending > limits.Inbox {
		return nil, nil, ErrCapacity
	}
	if wire.Schema == adapter.Source.Schema() {
		return before, state, nil
	}
	wire.Schema, wire.State = adapter.Source.Schema(), raw
	after, err := json.Marshal(wire)
	if int64(len(after)) > limits.Bytes {
		return nil, nil, ErrCapacity
	}
	return after, state, err
}

func legacyGitLabRecord(owner RoomIdentity, id string, states []notification.State) (gitlabNotificationRecord, error) {
	cfg := gitlab.NotificationConfig{Enabled: true, PlatformID: id, IntervalSeconds: gitlab.DefaultIntervalSeconds}
	found := false
	for _, state := range states {
		meta := state.Metadata()
		if meta.Key.Source.Provider != "gitlab" || meta.PlatformID != id || meta.Retired {
			continue
		}
		var target notification.Target
		var interval, backfill int
		var start = cfg.BackfillStart
		switch s := state.(type) {
		case gitlab.EventsState:
			cfg.Events = gitlab.EventSettingsFrom(s.Config)
			target, interval, backfill, start = s.Metadata().Target, s.Config.IntervalSeconds, s.Config.BackfillSeconds, s.Config.BackfillStart
		case gitlab.TodosState:
			cfg.Todos = gitlab.TodoSettingsFrom(s.Config)
			target, interval, backfill, start = s.Metadata().Target, s.Config.IntervalSeconds, s.Config.BackfillSeconds, s.Config.BackfillStart
		default:
			return gitlabNotificationRecord{}, ErrInvalid
		}
		if found && (cfg.Target != target || cfg.IntervalSeconds != interval || cfg.BackfillSeconds != backfill || !cfg.BackfillStart.Equal(start)) {
			return gitlabNotificationRecord{}, fmt.Errorf("%w: Room %s platform %s has incompatible channel target, interval or reception start", ErrConflict, owner.RoomID, id)
		}
		cfg.Target, cfg.IntervalSeconds, cfg.BackfillSeconds, cfg.BackfillStart = target, interval, backfill, start
		found = true
	}
	normalized, err := gitlab.NormalizeNotificationConfig(cfg)
	if err != nil || !found {
		return gitlabNotificationRecord{}, ErrInvalid
	}
	return gitlabNotificationRecord{RoomIdentity: owner, Schema: gitlabNotificationSchema, Version: 1, Config: normalized, BackfillLocked: true}, nil
}

func convertGitLabSettings(owner RoomIdentity, before json.RawMessage, states []notification.State) (json.RawMessage, error) {
	next := gitlabNotificationFile{RoomIdentity: owner, Schema: gitlabNotificationSchema, Platforms: map[string][]gitlabNotificationRecord{}}
	var head struct {
		Schema int `json:"schema"`
	}
	absent := string(before) == "null"
	if !absent && json.Unmarshal(before, &head) != nil {
		return nil, ErrInvalid
	}
	switch head.Schema {
	case gitlabNotificationSchema:
		if decodeNotificationJSON(before, &next) != nil {
			return nil, ErrInvalid
		}
		if err := validateGitLabNotificationFile(owner, next); err != nil {
			return nil, err
		}
		return before, nil
	case 3:
		var old struct {
			RoomIdentity
			Schema    int                                 `json:"schema"`
			Platforms map[string]gitlabNotificationRecord `json:"platforms"`
		}
		if decodeNotificationJSON(before, &old) != nil || old.RoomIdentity != owner || old.Platforms == nil {
			return nil, ErrInvalid
		}
		for id, record := range old.Platforms {
			if record.Schema != 3 || record.Config.PlatformID != id {
				return nil, ErrInvalid
			}
			record.Schema = gitlabNotificationSchema
			next.Platforms[id] = []gitlabNotificationRecord{record}
		}
	case 2:
		var record gitlabNotificationRecord
		if decodeNotificationJSON(before, &record) != nil {
			return nil, ErrInvalid
		}
		record.Config.Enabled = true // Schema 2 had category switches only.
		record.Schema = gitlabNotificationSchema
		next.Platforms[record.Config.PlatformID] = []gitlabNotificationRecord{record}
	case 0:
		if !absent {
			return nil, ErrVersion
		}
	default:
		return nil, ErrVersion
	}
	ids := map[string]bool{}
	for _, state := range states {
		meta := state.Metadata()
		if meta.Key.Source.Provider == "gitlab" && !meta.Retired {
			ids[meta.PlatformID] = true
		}
	}
	for id := range ids {
		if _, ok := next.Platforms[id]; ok {
			continue
		}
		if head.Schema == 2 {
			return nil, fmt.Errorf("%w: Room %s pending cross-platform GitLab configuration", ErrConflict, owner.RoomID)
		}
		record, err := legacyGitLabRecord(owner, id, states)
		if err != nil {
			return nil, err
		}
		next.Platforms[id] = []gitlabNotificationRecord{record}
	}
	if absent && len(next.Platforms) == 0 {
		return before, nil
	}
	if err := validateGitLabNotificationFile(owner, next); err != nil {
		return nil, err
	}
	// An unfinished destination/account switch cannot be assigned to independent
	// receivers. Frozen historical Routes are intentionally excluded from this check.
	for _, state := range states {
		meta := state.Metadata()
		if meta.Key.Source.Provider != "gitlab" || meta.Retired {
			continue
		}
		records := next.Platforms[meta.PlatformID]
		if len(records) != 1 || records[0].Config.Target != meta.Target {
			return nil, fmt.Errorf("%w: Room %s platform %s subscription %s differs from desired target; finish applying with previous version", ErrConflict, owner.RoomID, meta.PlatformID, meta.Key.SubscriptionID)
		}
	}
	return json.Marshal(next)
}

func notificationBackupPath(path string, before json.RawMessage) string {
	var head struct {
		Schema int `json:"schema"`
	}
	_ = json.Unmarshal(before, &head)
	return fmt.Sprintf("%s.v%d.bak", path, head.Schema)
}

// overrides reconstructs the complete pre-upgrade view during interrupted
// commits. All candidates and cross-file identities are revalidated on recovery.
func (m *roomManager) planMemberNotificationsUpgrade(overrides map[string]json.RawMessage) (platformUpgrade, error) {
	j := platformUpgrade{Version: 1}
	platforms, err := m.platforms.Load()
	if err != nil {
		return j, err
	}
	ops, err := m.onboardingOperationsLocked()
	if err != nil {
		return j, err
	}
	for _, op := range ops {
		if !op.Cancelled && (!onboardingFinished(op.Notifications) || !onboardingFinished(op.Tool)) {
			return j, fmt.Errorf("%w: unfinished platform onboarding %s; finish or cancel with previous version", ErrConflict, op.ID)
		}
	}
	read := func(path string) (json.RawMessage, error) {
		rel, err := filepath.Rel(m.store.dir, path)
		if err != nil {
			return nil, err
		}
		if raw, ok := overrides[rel]; ok {
			return raw, nil
		}
		raw, err := readPrivateLarkSnapshot(path)
		if errors.Is(err, os.ErrNotExist) {
			return json.RawMessage("null"), nil
		}
		if err == nil && string(raw) == "null" {
			return nil, ErrInvalid
		}
		return raw, err
	}
	add := func(path string, before, after json.RawMessage) error {
		if equalPlatformJSON(before, after) {
			return nil
		}
		if raw, err := readPrivateLarkSnapshot(notificationBackupPath(path, before)); err == nil {
			if !equalPlatformJSON(raw, before) {
				return ErrConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		rel, err := filepath.Rel(m.store.dir, path)
		if err != nil {
			return err
		}
		j.Files = append(j.Files, platformFileChange{Path: rel, Before: before, After: after})
		return nil
	}
	for _, room := range m.index.Rooms {
		if room.Status != "ready" {
			continue
		}
		owner := room.RoomIdentity
		roomPath := m.roomDir(owner.RoomID)
		// Validate ancestors even for a Room with no source snapshots.
		for _, dir := range []string{filepath.Join(m.store.dir, "rooms"), roomPath} {
			info, e := os.Lstat(dir)
			if e != nil {
				return j, e
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
				return j, ErrInvalid
			}
		}
		dir := filepath.Join(roomPath, "notifications")
		entries, e := os.ReadDir(dir)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return j, e
		}
		if e == nil {
			if err := validateLarkUpgradeDirectories(m.store.dir, owner.RoomID); err != nil {
				return j, err
			}
		}
		var states []notification.State
		type sourcePlatform struct {
			Source   notification.SourceID
			Platform string
		}
		formats := map[sourcePlatform]struct {
			Count  int
			Legacy bool
		}{}
		scopes := map[struct {
			Source           notification.SourceID
			Platform, Target string
		}]notification.Metadata{}
		// Membership identifies the receiver even across channels and platforms.
		agents := map[string]string{}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			before, err := read(path)
			if err != nil {
				return j, err
			}
			after, state, err := m.convertNotificationSource(owner, path, before)
			if err != nil {
				return j, fmt.Errorf("Room %s %s: %w", owner.RoomID, entry.Name(), err)
			}
			meta := state.Metadata()
			if meta.Target.Inbox() {
				if agent, ok := agents[meta.Target.MembershipID]; ok && agent != meta.Target.AgentID {
					return j, ErrIdentity
				}
				agents[meta.Target.MembershipID] = meta.Target.AgentID
			}
			if !meta.Retired {
				var envelope notificationEnvelope
				if decodeNotificationJSON(before, &envelope) != nil {
					return j, ErrInvalid
				}
				slot := sourcePlatform{meta.Key.Source, meta.PlatformID}
				format := formats[slot]
				format.Count++
				format.Legacy = format.Legacy || envelope.Schema < 3
				if format.Legacy && format.Count > 1 {
					return j, fmt.Errorf("%w: Room %s platform %s has duplicate legacy %s subscriptions", ErrConflict, owner.RoomID, meta.PlatformID, meta.Key.Source.Channel)
				}
				formats[slot] = format
				key := struct {
					Source           notification.SourceID
					Platform, Target string
				}{meta.Key.Source, meta.PlatformID, meta.Target.ScopeKey()}
				if _, ok := scopes[key]; ok {
					return j, ErrConflict
				}
				scopes[key] = meta
			}
			states = append(states, state)
			if err := add(path, before, after); err != nil {
				return j, err
			}
		}
		path := m.gitlabNotificationPath(owner)
		before, err := read(path)
		if err != nil {
			return j, err
		}
		if int64(len(before)) > maxGitLabConfigBytes {
			return j, ErrCapacity
		}
		after, err := convertGitLabSettings(owner, before, states)
		if err != nil {
			return j, err
		}
		if int64(len(after)) > maxGitLabConfigBytes {
			return j, ErrCapacity
		}
		if string(after) != "null" {
			var settings gitlabNotificationFile
			if err := decodeNotificationJSON(after, &settings); err != nil {
				return j, err
			}
			for _, records := range settings.Platforms {
				for _, record := range records {
					t := record.Config.Target
					if t.Inbox() {
						if agent, ok := agents[t.MembershipID]; ok && agent != t.AgentID {
							return j, ErrIdentity
						}
						agents[t.MembershipID] = t.AgentID
					}
				}
			}
		}
		for _, state := range states {
			meta := state.Metadata()
			if meta.Key.Source.Provider != "gitlab" || meta.Retired {
				continue
			}
			generation := ""
			switch source := state.(type) {
			case gitlab.EventsState:
				generation = source.Binding.PlatformGeneration
			case gitlab.TodosState:
				generation = source.Binding.PlatformGeneration
			}
			for _, platform := range platforms.Platforms {
				if platform.ID == meta.PlatformID && platform.Generation != generation {
					return j, fmt.Errorf("%w: Room %s platform %s subscription %s has an unapplied account generation; finish applying with previous version", ErrConflict, owner.RoomID, meta.PlatformID, meta.Key.SubscriptionID)
				}
			}
		}

		if err := add(path, before, after); err != nil {
			return j, err
		}
	}
	sort.Slice(j.Files, func(i, k int) bool { return j.Files[i].Path < j.Files[k].Path })
	return j, nil
}

func (m *roomManager) upgradeMemberNotificationsLocked() (int, error) {
	// Finish a journal produced by the historical Lark upgrader before
	// constructing the new whole-directory transaction. New upgrades never
	// create a separate schema-2 transaction.
	var legacy platformUpgrade
	if err := readLarkUpgradeJournal(filepath.Join(m.store.dir, larkUpgradeJournal), &legacy); err == nil {
		if legacy.Version != 1 {
			return 0, ErrVersion
		}
		if !legacy.Complete {
			if err := commitLarkNotificationUpgrade(m.store, legacy); err != nil {
				return 0, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	path := filepath.Join(m.store.dir, m.notificationUpgradeJournal())
	var prior platformUpgrade
	err := readLarkUpgradeJournal(path, &prior)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err == nil && prior.Version != 1 {
		return 0, ErrVersion
	}
	if err == nil && !prior.Complete {
		return len(prior.Files), m.commitMemberNotificationsUpgrade(prior)
	}
	next, err := m.planMemberNotificationsUpgrade(nil)
	if err != nil {
		return 0, err
	}
	if len(next.Files) == 0 {
		return 0, nil
	}
	if prior.Complete && !reflect.DeepEqual(prior.Files, next.Files) {
		return 0, ErrConflict
	}
	if err := m.store.save(path, next, platformUpgradeLimit); err != nil {
		return 0, err
	}
	return len(next.Files), m.commitMemberNotificationsUpgrade(next)
}

func (m *roomManager) commitMemberNotificationsUpgrade(j platformUpgrade) error {
	if j.Version != 1 || j.Complete || len(j.Files) == 0 {
		return ErrInvalid
	}
	overrides := map[string]json.RawMessage{}
	for _, f := range j.Files {
		parts := strings.Split(filepath.ToSlash(f.Path), "/")
		if len(parts) < 3 || len(parts) > 4 || parts[0] != "rooms" || !safeMembershipID(parts[1]) || filepath.Clean(f.Path) != f.Path {
			return ErrInvalid
		}
		if len(parts) == 3 && parts[2] != "gitlab-notifications.json" || len(parts) == 4 && (parts[2] != "notifications" || !strings.HasSuffix(parts[3], ".json")) {
			return ErrInvalid
		}
		if _, ok := overrides[f.Path]; ok {
			return ErrInvalid
		}
		overrides[f.Path] = f.Before
		path := filepath.Join(m.store.dir, f.Path)
		raw, err := readPrivateLarkSnapshot(path)
		if errors.Is(err, os.ErrNotExist) && string(f.Before) == "null" {
			raw = f.Before
		} else if err != nil {
			return err
		}
		if !equalPlatformJSON(raw, f.Before) && !equalPlatformJSON(raw, f.After) {
			return ErrConflict
		}
	}
	expected, err := m.planMemberNotificationsUpgrade(overrides)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(j, expected) {
		return ErrInvalid
	}
	// Write the entire backup set before replacing any original. The journal is
	// durable already; unknown writes latch the store and startup blocks until resume.
	for _, f := range j.Files {
		path := notificationBackupPath(filepath.Join(m.store.dir, f.Path), f.Before)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := m.store.save(path, f.Before, platformUpgradeLimit); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	for _, f := range j.Files {
		if err := m.store.save(filepath.Join(m.store.dir, f.Path), f.After, platformUpgradeLimit); err != nil {
			return err
		}
	}
	j.Complete = true
	return m.store.save(filepath.Join(m.store.dir, m.notificationUpgradeJournal()), j, platformUpgradeLimit)
}
