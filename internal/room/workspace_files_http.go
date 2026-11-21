package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Every file read shares this bounded, cancellable admission queue. No Room,
// catalog or configuration lock is held while waiting or doing filesystem IO.
var workspaceFileReads = make(chan struct{}, workspaceFileConcurrency)

type workspaceFileSnapshot struct {
	id        RoomIdentity
	members   []managedMember
	workspace workspaceConfig
	roomDir   string
	policy    *roomManager
	labels    map[string]string
	reference *RoomMessage
}

func (m *roomManager) workspaceFilesSnapshot(r *http.Request) (workspaceFileSnapshot, error) {
	snapshot, err := m.workspaceFilesConfigSnapshot(r)
	if err != nil || !r.URL.Query().Has("message_seq") {
		return snapshot, err
	}
	// History IO is outside the manager lock, just like content reads. Every
	// delivery takes a fresh configuration and visible-message snapshot below.
	room, err := readPublishedRoom(snapshot.roomDir, snapshot.id)
	if err != nil {
		return workspaceFileSnapshot{}, err
	}
	seq, _ := strconv.ParseUint(r.URL.Query().Get("message_seq"), 10, 64)
	if seq <= room.ClearedThrough || seq > room.LatestSeq {
		return workspaceFileSnapshot{}, fileError("reference_unavailable")
	}
	message := room.Messages[seq-room.ClearedThrough-1]
	if message.Author.Kind != "agent" || message.Type != "" || message.Event != nil || message.Source != nil || message.WorkspaceRootID == "" || message.AuthorMembershipID == "" {
		return workspaceFileSnapshot{}, fileError("reference_unavailable")
	}
	active := slices.ContainsFunc(room.Members, func(member MemberState) bool {
		return member.AgentID == message.Author.ID && member.MembershipID == message.AuthorMembershipID
	})
	configured := slices.ContainsFunc(snapshot.members, func(member managedMember) bool {
		return member.AgentID == message.Author.ID && member.MembershipID == message.AuthorMembershipID
	})
	if !active || !configured {
		return workspaceFileSnapshot{}, fileError("workspace_source_changed")
	}
	snapshot.reference = &message
	return snapshot, nil
}

func (m *roomManager) workspaceFilesConfigSnapshot(r *http.Request) (workspaceFileSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := r.URL.Query()
	if m.closing {
		return workspaceFileSnapshot{}, ErrClosing
	}
	if q.Get("service_epoch") != m.epoch || q.Get("caller_id") != requestOperator(r) || !validText(q.Get("caller_id")) {
		return workspaceFileSnapshot{}, ErrIdentity
	}
	id := RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
	entry, err := m.workspaceRoom(id)
	if err != nil || entry.Status != "ready" {
		return workspaceFileSnapshot{}, ErrIdentity
	}
	if err := m.store.check(false); err != nil {
		return workspaceFileSnapshot{}, err
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity == id && !op.finished() {
			return workspaceFileSnapshot{}, fileError("workspace_source_unavailable")
		}
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil || saved.Workspace == nil {
		return workspaceFileSnapshot{}, fileError("workspace_source_unavailable")
	}
	policy := &roomManager{store: m.store, teamsDir: m.teamsDir, workspaceProtected: slices.Clone(m.workspaceProtected)}
	if m.workspaceRoots != nil {
		roots := slices.Clone(*m.workspaceRoots)
		policy.workspaceRoots = &roots
	}
	labels := map[string]string{}
	for _, member := range saved.Members {
		labels[member.MembershipID] = m.agentNames.name(MemberIdentity{RoomIdentity: id, AgentID: member.AgentID, MembershipID: member.MembershipID})
	}
	snapshot := workspaceFileSnapshot{id: id, members: saved.Members, workspace: *saved.Workspace, roomDir: m.roomDir(id.RoomID), policy: policy, labels: labels}
	return snapshot, nil
}

func (s workspaceFileSnapshot) source(member managedMember) (workspaceFileSource, *workspaceFileRoot, error) {
	resolved := resolveMemberWorkspace(s.roomDir, s.workspace, member)
	source := workspaceFileSource{MembershipID: member.MembershipID, Label: s.labels[member.MembershipID], DirectorySource: resolved.Source}
	if source.Label == "" {
		source.Label = member.AgentID
	}
	unavailable := func() (workspaceFileSource, *workspaceFileRoot, error) {
		source.UnavailableReason = "workspace_source_unavailable"
		return source, nil, fileError(source.UnavailableReason)
	}
	directory := resolved.Directory
	if resolved.Source == "managed" {
		if err := checkWorkspaceDirectory(s.roomDir, member.Workspace); err != nil {
			return unavailable()
		}
		real, err := filepath.EvalSymlinks(directory)
		if err != nil {
			return unavailable()
		}
		directory = real
	} else {
		// External bindings are frozen canonical paths; never follow a replaced
		// root to a different location or fall back to the server working directory.
		real, err := s.policy.canonicalWorkspaceDirectory(directory, true)
		if err != nil {
			if stringDirectoryError(err) == "directory_path_changed" {
				source.UnavailableReason = "workspace_source_changed"
				return source, nil, fileError(source.UnavailableReason)
			}
			return unavailable()
		}
		directory = real
	}
	for _, part := range strings.Split(filepath.ToSlash(directory), "/") {
		if strings.EqualFold(part, ".git") || resolved.Source != "managed" && strings.EqualFold(part, ".teamx") {
			return unavailable()
		}
	}
	storeDirectory, err := resolveFutureDirectory(s.policy.store.dir)
	if err != nil {
		return unavailable()
	}
	protected := []string{}
	for _, p := range append([]string{s.policy.store.dir, s.policy.teamsDir}, s.policy.workspaceProtected...) {
		if p == "" {
			continue
		}
		real, err := resolveFutureDirectory(p)
		if err != nil {
			return unavailable()
		}
		// The exact managed workspace is exempt from protected ancestors of the
		// Room store (including a deployment home containing that store).
		// Explicit protected descendants remain forbidden.
		if resolved.Source == "managed" && pathWithin(real, storeDirectory) && pathWithin(real, directory) {
			continue
		}
		if pathWithin(real, directory) {
			return unavailable()
		}
		protected = append(protected, real)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return unavailable()
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return unavailable()
	}
	source.RootID = workspaceRootID(s.id, member.MembershipID, directory)
	sum := sha256.Sum256([]byte(directory))
	source.SharedKey = hex.EncodeToString(sum[:])
	source.Available = true
	return source, &workspaceFileRoot{root: root, info: info, directory: directory, protected: protected}, nil
}

func validWorkspaceFileQuery(q url.Values, sources bool) bool {
	allowed := map[string]bool{"room_id": true, "room_instance": true, "service_epoch": true, "caller_id": true}
	if !sources {
		for _, k := range []string{"membership_id", "root_id", "message_seq", "path", "show_hidden", "download"} {
			allowed[k] = true
		}
	}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return false
		}
	}
	for _, k := range []string{"room_id", "room_instance", "service_epoch", "caller_id"} {
		if len(q[k]) != 1 {
			return false
		}
	}
	if !sources {
		if len(q["path"]) != 1 || !validWorkspaceFilePath(q.Get("path")) {
			return false
		}
		if q.Has("message_seq") {
			seq, err := strconv.ParseUint(q.Get("message_seq"), 10, 64)
			if err != nil || seq == 0 || q.Has("membership_id") || q.Has("root_id") {
				return false
			}
		} else if len(q["membership_id"]) != 1 || len(q["root_id"]) != 1 {
			return false
		}
	}
	return (!q.Has("show_hidden") || q.Get("show_hidden") == "true" || q.Get("show_hidden") == "false") && (!q.Has("download") || q.Get("download") == "true")
}

func (m *roomManager) serveWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	sources := r.URL.Path == "/v1/workspace/files/sources"
	if !sources && r.URL.Path != "/v1/workspace/files" && r.URL.Path != "/v1/workspace/files/preview" && r.URL.Path != "/v1/workspace/files/content" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	if !validWorkspaceFileQuery(q, sources) {
		writeAPIError(w, ErrRequest)
		return
	}
	if q.Has("message_seq") && r.URL.Path == "/v1/workspace/files" || q.Has("download") && r.URL.Path != "/v1/workspace/files/content" {
		writeAPIError(w, ErrRequest)
		return
	}
	timeout := 10 * time.Second
	if q.Get("download") == "true" {
		timeout = workspaceFileDownloadTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	select {
	case workspaceFileReads <- struct{}{}:
		defer func() { <-workspaceFileReads }()
	case <-ctx.Done():
		writeWorkspaceFileError(w, fileError("workspace_source_unavailable"))
		return
	}
	snapshot, err := m.workspaceFilesSnapshot(r)
	if err != nil {
		writeWorkspaceFileError(w, err)
		return
	}
	type response struct {
		ServiceInfo
		Path    string                `json:"path"`
		Sources []workspaceFileSource `json:"sources,omitempty"`
		Limits  any                   `json:"limits,omitempty"`
		Source  *workspaceFileSource  `json:"source,omitempty"`
		*workspaceFileListing
		*workspaceFilePreview
	}
	var content *workspaceFileContent
	result := response{Limits: workspaceFileLimits(), ServiceInfo: ServiceInfo{RoomIdentity: snapshot.id, ServiceEpoch: q.Get("service_epoch")}}
	if sources {
		result.Sources = []workspaceFileSource{}
		for _, member := range snapshot.members {
			if err := ctx.Err(); err != nil {
				writeWorkspaceFileError(w, err)
				return
			}
			source, root, _ := snapshot.source(member)
			if root != nil {
				root.Close()
			}
			result.Sources = append(result.Sources, source)
		}

		latest, err := m.workspaceFilesSnapshot(r)
		if err != nil {
			writeWorkspaceFileError(w, err)
			return
		}
		if !reflect.DeepEqual(snapshot.members, latest.members) || !reflect.DeepEqual(snapshot.workspace.MemberDirectories, latest.workspace.MemberDirectories) {
			writeWorkspaceFileError(w, fileError("workspace_source_changed"))
			return
		}
	} else {
		membership, rootID := q.Get("membership_id"), q.Get("root_id")
		if snapshot.reference != nil {
			membership, rootID = snapshot.reference.AuthorMembershipID, snapshot.reference.WorkspaceRootID
		}
		var selected *managedMember
		for _, member := range snapshot.members {
			if member.MembershipID == membership {
				selected = &member
				break
			}
		}
		if selected == nil {
			writeWorkspaceFileError(w, fileError("workspace_source_changed"))
			return
		}
		configured := resolveMemberWorkspace(snapshot.roomDir, snapshot.workspace, *selected)
		if configured.Source == "external" && workspaceRootID(snapshot.id, selected.MembershipID, configured.Directory) != rootID {
			writeWorkspaceFileError(w, fileError("workspace_source_changed"))
			return
		}
		source, root, err := snapshot.source(*selected)
		if err != nil {
			writeWorkspaceFileError(w, err)
			return
		}
		defer root.Close()
		if source.RootID != rootID {
			writeWorkspaceFileError(w, fileError("workspace_source_changed"))
			return
		}
		result.Source = &source
		p := q.Get("path")
		if p == "." {
			p = ""
		}
		if r.URL.Path == "/v1/workspace/files" {
			listing, err := root.list(ctx, p, q.Get("show_hidden") == "true")
			if err != nil {
				writeWorkspaceFileError(w, err)
				return
			}
			result.Path = listing.Path
			result.workspaceFileListing = &listing
		} else if r.URL.Path == "/v1/workspace/files/content" {
			content, err = root.content(ctx, p, q.Get("download") == "true")
			if err != nil {
				writeWorkspaceFileError(w, err)
				return
			}
			defer content.file.Close()
		} else {
			preview, err := root.preview(ctx, p)
			if err != nil {
				writeWorkspaceFileError(w, err)
				return
			}
			result.Path = preview.Path
			if preview.PreviewKind == "image" {
				preview.ContentURL = "/v1/workspace/files/content?" + q.Encode()
			}
			result.workspaceFilePreview = &preview
		}
		var info os.FileInfo
		if content != nil {
			info = content.info
		}
		if err := m.admitWorkspaceFile(r, source, root, p, info); err != nil {
			writeWorkspaceFileError(w, err)
			return
		}
	}
	if ctx.Err() != nil {
		writeWorkspaceFileError(w, fileError("workspace_source_unavailable"))
		return
	}
	if content != nil {
		if err := content.serve(ctx, w, filepath.Base(q.Get("path")), q.Get("download") == "true"); err != nil {
			writeWorkspaceFileError(w, fileError("workspace_source_unavailable"))
		}
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func writeWorkspaceFileError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		err = fileError("workspace_source_unavailable")
	}
	var fileErr workspaceFileError
	if !errors.As(err, &fileErr) {
		writeAPIError(w, err)
		return
	}
	status := http.StatusBadRequest
	switch fileErr {
	case "workspace_source_changed", "file_changed":
		status = http.StatusConflict
	case "workspace_source_unavailable":
		status = http.StatusServiceUnavailable
	case "file_not_found", "reference_unavailable":
		status = http.StatusNotFound
	case "file_not_allowed":
		status = http.StatusForbidden
	case "file_too_large", "image_limit_exceeded":
		status = http.StatusRequestEntityTooLarge
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Code: string(fileErr), Error: string(fileErr)})
}

func sameWorkspaceFileBinding(a, b workspaceFileSnapshot, membership string) bool {
	var before, after *managedMember
	for _, member := range a.members {
		if member.MembershipID == membership {
			before = &member
			break
		}
	}
	for _, member := range b.members {
		if member.MembershipID == membership {
			after = &member
			break
		}
	}
	return before != nil && after != nil && before.AgentID == after.AgentID &&
		resolveMemberWorkspace(a.roomDir, a.workspace, *before).Directory == resolveMemberWorkspace(b.roomDir, b.workspace, *after).Directory
}

// Revalidate at the delivery boundary without holding manager locks across IO.
func (m *roomManager) admitWorkspaceFile(r *http.Request, source workspaceFileSource, root *workspaceFileRoot, p string, info os.FileInfo) error {
	// Recheck only this member's effective source. Unrelated platform changes,
	// member additions and directory changes must not invalidate the read.
	latest, err := m.workspaceFilesSnapshot(r)
	if err != nil {
		return err
	}
	found := false
	for _, member := range latest.members {
		if member.MembershipID != source.MembershipID {
			continue
		}
		current, currentRoot, err := latest.source(member)
		if err != nil {
			return fileError("workspace_source_changed")
		}
		same := current.RootID == source.RootID && os.SameFile(root.info, currentRoot.info) && currentRoot.allowed(p)
		if same {
			probe, probeErr := currentRoot.open(p)
			if probeErr != nil {
				same = false
			} else {
				_ = probe.Close()
			}
		}
		currentRoot.Close()
		if !same {
			return fileError("workspace_source_changed")
		}
		found = true
		break
	}
	if !found {
		return fileError("workspace_source_changed")
	}
	if info != nil {
		if err := root.unchanged(p, info); err != nil {
			return err
		}
	}
	// Filesystem rechecks above may be slow. The final admission is a
	// fresh configuration snapshot, with no subsequent filesystem IO.
	admitted, err := m.workspaceFilesSnapshot(r)
	if err != nil {
		return err
	}
	if !sameWorkspaceFileBinding(latest, admitted, source.MembershipID) {
		return fileError("workspace_source_changed")
	}
	return nil
}
