package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Standalone binaries must not depend on host zoneinfo files.

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const defaultTimeZone = "Asia/Shanghai"

type settingsView struct {
	TimeZone        string `json:"time_zone"`
	SettingsVersion uint64 `json:"settings_version"`
}

// A snapshot is copied under one lock; consumers keep the location and version
// together for the duration of their operation. Nothing here enters Room state.
type settingsSnapshot struct {
	settingsView
	location *time.Location
}

type settingsCommit struct {
	snapshot settingsSnapshot
	source   []byte
}

type systemSettings struct {
	mu      sync.Mutex
	current settingsSnapshot
	path    string
	source  []byte
	file    os.FileInfo
	pending *settingsCommit
	ops     snapshotOps
}

func loadTimeZone(name string) (*time.Location, error) {
	// LoadLocation accepts "" and "Local", both of which depend on the host.
	if name == "" || name == "Local" || strings.TrimSpace(name) != name {
		return nil, invalidField("time_zone", "use a valid IANA time zone, for example Asia/Shanghai or UTC")
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, invalidField("time_zone", "use a valid IANA time zone, for example Asia/Shanghai or UTC")
	}
	return location, nil
}

func defaultSystemSettings() *systemSettings {
	location, _ := loadTimeZone(defaultTimeZone)
	return &systemSettings{current: settingsSnapshot{settingsView{defaultTimeZone, 1}, location}, ops: defaultSnapshotOps()}
}

func newSystemSettings(config startupConfig) *systemSettings {
	s := defaultSystemSettings()
	s.path, s.source, s.file = config.path, config.source, config.file
	// loadStartupConfig already validated this value.
	if config.Server.TimeZone != nil {
		s.current.TimeZone = *config.Server.TimeZone
		s.current.location, _ = loadTimeZone(s.current.TimeZone)
	}
	return s
}

func (s *systemSettings) snapshot() settingsSnapshot {
	if s == nil {
		return defaultSystemSettings().current
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// Re-read both identity and bytes, including after preparing the temporary
// file. An editor's replacement or in-place edit invalidates this save.
func (s *systemSettings) unchanged() error {
	data, file, err := readSettingsFile(s.path)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, s.source) || (file == nil) != (s.file == nil) ||
		(file != nil && (!os.SameFile(file, s.file) || !file.ModTime().Equal(s.file.ModTime()))) {
		return &requestDiagnostic{cause: ErrConflict, Field: "time_zone", Message: "startup config changed outside this service; restart to load it before saving"}
	}
	return nil
}

func readSettingsFile(path string) ([]byte, os.FileInfo, error) {
	file, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	// Do not replace a symlink with a regular file or follow a newly changed link.
	if !file.Mode().IsRegular() || file.Size() > 1<<20 {
		return nil, nil, fmt.Errorf("config must be a regular file of at most 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(file, opened) {
		return nil, nil, ErrConflict
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, fmt.Errorf("cannot read config (maximum 1 MiB)")
	}
	return data, opened, nil
}

func (s *systemSettings) save(name string, expected uint64) (settingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	location, err := loadTimeZone(name)
	if err != nil {
		return s.current.settingsView, err
	}
	checkVersion := func() error {
		if expected != s.current.SettingsVersion {
			actual := s.current.SettingsVersion
			return &requestDiagnostic{cause: ErrConflict, Field: "expected_version", Message: "system settings version changed; read current settings before saving", ExpectedVersion: &expected, ActualVersion: &actual}
		}
		return nil
	}
	if s.pending == nil {
		if err := checkVersion(); err != nil {
			return s.current.settingsView, err
		}
		if name == s.current.TimeZone {
			return s.current.settingsView, nil
		}
	}
	if s.path == "" {
		return s.current.settingsView, errors.New("startup config path unavailable")
	}
	dir := filepath.Dir(s.path)
	if err := s.ops.makeDir(dir); err != nil {
		return s.current.settingsView, err
	}
	// Serialize services sharing this config even when they use different data
	// directories. Keep the lock file in place so concurrent writers share an inode.
	lock, err := lockDirectoryFile(dir, "."+filepath.Base(s.path)+".lock")
	if err != nil {
		return s.current.settingsView, err
	}
	defer unlockDirectory(lock)
	if s.pending != nil {
		if err := s.reconcile(); err != nil {
			return s.current.settingsView, err
		}
	}
	if err := checkVersion(); err != nil {
		return s.current.settingsView, err
	}
	if name == s.current.TimeZone {
		return s.current.settingsView, nil
	}
	if err := s.unchanged(); err != nil {
		return s.current.settingsView, err
	}
	next, err := patchTimeZone(s.source, name)
	if err != nil {
		return s.current.settingsView, err
	}
	f, err := s.ops.createTemp(dir, ".config-*")
	if err != nil {
		return s.current.settingsView, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	mode := os.FileMode(0600)
	if s.file != nil {
		mode = s.file.Mode().Perm()
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		return s.current.settingsView, err
	}
	if _, err := io.Copy(f, bytes.NewReader(next)); err != nil {
		return s.current.settingsView, err
	}
	if err := f.Sync(); err != nil {
		return s.current.settingsView, err
	}
	if err := f.Close(); err != nil {
		return s.current.settingsView, err
	}
	if err := s.unchanged(); err != nil {
		return s.current.settingsView, err
	}
	s.pending = &settingsCommit{settingsSnapshot{settingsView{name, s.current.SettingsVersion + 1}, location}, next}
	// Even an error may mean rename happened. Always inspect the file before
	// deciding whether to publish; reconcile also retries durability barriers.
	replaceErr := s.ops.rename(f.Name(), s.path)
	err = s.reconcile()
	if err != nil {
		return s.current.settingsView, err
	}
	if s.current.TimeZone != name {
		if replaceErr == nil {
			replaceErr = ErrConflict
		}
		return s.current.settingsView, replaceErr
	}
	return s.current.settingsView, nil
}

func (s *systemSettings) reconcile() error {
	data, file, err := readSettingsFile(s.path)
	if err != nil {
		return fmt.Errorf("%w: inspect config", ErrCommitUnknown)
	}
	if bytes.Equal(data, s.pending.source) && file != nil {
		// A previous rename/sync error is not success until file and directory
		// durability have both been established on the observed file.
		f, err := os.Open(s.path)
		if err != nil {
			return ErrCommitUnknown
		}
		err = f.Sync()
		err = errors.Join(err, f.Close())
		if err != nil || s.ops.syncDir(filepath.Dir(s.path)) != nil {
			return ErrCommitUnknown
		}
		verified, current, err := readSettingsFile(s.path)
		if err != nil || current == nil || !os.SameFile(file, current) || !bytes.Equal(data, verified) {
			return ErrCommitUnknown
		}
		s.current, s.source, s.file = s.pending.snapshot, data, current
		s.pending = nil
		return nil
	}
	if s.unchanged() == nil {
		s.pending = nil // Replacement demonstrably did not happen.
		return nil
	}
	return ErrCommitUnknown // Preserve evidence and block writes until reconciled.
}

// Patch just the string token, retaining every other byte, including comments.
// Handle ordinary tables, dotted/quoted keys and inline server tables.
func patchTimeZone(data []byte, name string) ([]byte, error) {
	var parsed startupConfig
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&parsed); err != nil && len(data) != 0 {
		return nil, errors.New("startup config is no longer valid TOML")
	}
	var p unstable.Parser
	p.Reset(data)
	var table []string
	insert := -1
	inline := false
	inlineHasFields := false
	start, end := -1, -1
	keys := func(n *unstable.Node) []string {
		var result []string
		it := n.Key()
		for it.Next() {
			result = append(result, string(it.Node().Data))
		}
		return result
	}
	var visit func(*unstable.Node, []string)
	visit = func(n *unstable.Node, base []string) {
		path := append(slices.Clone(base), keys(n)...)
		v := n.Value()
		if slices.Equal(path, []string{"server", "time_zone"}) {
			start, end = int(v.Raw.Offset), int(v.Raw.Offset+v.Raw.Length)
		}
		if v.Kind == unstable.InlineTable {
			if slices.Equal(path, []string{"server"}) {
				insert, inline, inlineHasFields = int(v.Raw.Offset)+1, true, v.Child() != nil
			}
			it := v.Children()
			for it.Next() {
				visit(it.Node(), path)
			}
		}
	}
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = keys(n)
			if n.Kind == unstable.Table && slices.Equal(table, []string{"server"}) {
				key := n.Child()
				offset := int(key.Raw.Offset)
				newline := bytes.IndexByte(data[offset:], '\n')
				insert = len(data)
				if newline >= 0 {
					insert = offset + newline + 1
				}
			}
		case unstable.KeyValue:
			visit(n, table)
		}
	}
	if p.Error() != nil {
		return nil, errors.New("cannot parse startup config")
	}
	value := strconv.Quote(name)
	var next []byte
	switch {
	case start >= 0:
		next = append(append(bytes.Clone(data[:start]), value...), data[end:]...)
	case inline:
		field := " time_zone = " + value
		if inlineHasFields {
			field += ","
		}
		next = append(append(bytes.Clone(data[:insert]), field...), data[insert:]...)
	case insert >= 0:
		newline := "\n"
		if bytes.Contains(data, []byte("\r\n")) {
			newline = "\r\n"
		}
		field := "time_zone = " + value + newline
		if insert > 0 && data[insert-1] != '\n' {
			field = newline + field
		}
		next = append(append(bytes.Clone(data[:insert]), field...), data[insert:]...)
	default:
		next = append([]byte("server.time_zone = "+value+"\n"), data...)
	}
	var check startupConfig
	if len(next) > 1<<20 || toml.NewDecoder(bytes.NewReader(next)).DisallowUnknownFields().Decode(&check) != nil || check.Server.TimeZone == nil || *check.Server.TimeZone != name {
		return nil, errors.New("cannot safely update server.time_zone in startup config")
	}
	return next, nil
}

type settingsRequest struct {
	ServiceInfo
	Caller          Caller `json:"caller"`
	ExpectedVersion uint64 `json:"expected_version"`
	TimeZone        string `json:"time_zone"`
}

func serveSystemSettings(w http.ResponseWriter, r *http.Request, s *systemSettings, info ServiceInfo) {
	if r.URL.Path == "/v1/settings" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(struct {
			ServiceInfo
			settingsView
		}{info, s.snapshot().settingsView})
		return
	}
	if r.URL.Path != "/v1/settings/save" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req settingsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	// Follow the existing local operator contract. Agent membership conveys no
	// authority to change service-wide settings, even with a valid Room binding.
	if req.ServiceInfo != info || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	current, err := s.save(req.TimeZone, req.ExpectedVersion)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(struct {
				Error   string `json:"error"`
				Code    string `json:"code"`
				Message string `json:"message"`
				settingsView
			}{err.Error(), "conflict", err.Error(), current})
		} else {
			writeAPIError(w, err)
		}
		return
	}
	_ = json.NewEncoder(w).Encode(current)
}
