package room

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func (s *spaceServer) openGitLabApplications() error {
	s.assembly.config.gitlabApplications = gitlaboauth.Disabled{}
	if !s.access.config.Deployment.GitLab.Enabled {
		return nil
	}
	path := filepath.Join(s.assembly.roots.Home, "gitlab-applications.json")
	catalog, err := gitlaboauth.Open(gitlaboauth.Store{
		OtherGrant: s.otherGitLabGrant,
		Load: func() (gitlaboauth.Snapshot, error) {
			saved := gitlaboauth.Snapshot{Version: 1, Applications: []gitlaboauth.Application{}}
			if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
				return saved, nil
			}
			err := readIdentitySnapshot(path, &saved)
			return saved, err
		},
		Save: func(saved gitlaboauth.Snapshot) error { return s.writer.save(path, saved, 1<<20) },
	})
	if err != nil {
		return err
	}
	s.gitlabApplications = catalog
	return nil
}

func (a *serverAccess) serveGitLabApplications(w http.ResponseWriter, r *http.Request) {
	s := a.spaces
	if s.gitlabApplications == nil {
		writeAPIError(w, hostToolUnavailable("managed_gitlab"))
		return
	}
	if r.Method == http.MethodGet {
		apps := s.gitlabApplications.Public()
		// Include durable grant references, including closed/archived spaces. This
		// inventory is read-only; their original owners remain the only grant writers.
		impact, err := s.gitlabGrantInventory()
		if err != nil {
			writeAPIError(w, managedPlatformError(err))
			return
		}

		live := map[string]any{}
		for _, app := range apps {
			live[app.ID] = s.gitlabApplications.Impact(app.ID)
		}
		_ = writeJSON(w, map[string]any{"applications": apps, "grants": impact, "operations": live, "requires_restart": s.gitlabApplications.RequiresRestart()})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var input struct {
		Application gitlaboauth.Application `json:"application"`
		Version     uint64                  `json:"version"`
		Settle      bool                    `json:"settle,omitempty"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	if input.Settle {
		if !validSpaceID(input.Application.ID) {
			writeAPIError(w, ErrRequest)
			return
		}
		if err := s.gitlabApplications.Settle(r.Context(), input.Application.ID); err != nil {
			writeAPIError(w, managedPlatformError(err))
			return
		}
		_ = writeJSON(w, map[string]bool{"settled": true})
		return
	}
	if input.Application.RedirectURI != a.public+gitlabCallbackPath {
		writeAPIError(w, ErrRequest)
		return
	}
	app, err := s.gitlabApplications.Update(r.Context(), input.Application, input.Version)
	if err != nil {
		writeAPIError(w, managedPlatformError(err))
		return
	}
	_ = writeJSON(w, app)
}

type gitlabGrantReference struct {
	ApplicationID string `json:"application_id"`
	OwnerUserID   string `json:"user_id"`
	SpaceID       string `json:"space_id"`
	CredentialID  string `json:"credential_id"`
	NativeUserID  int64  `json:"native_user_id"`
	State         string `json:"state"`
}

func (s *spaceServer) gitlabGrantInventory() ([]gitlabGrantReference, error) {
	out := []gitlabGrantReference{}
	err := s.visitGitLabSnapshots(func(record SpaceRecord, saved registry.Snapshot) error {
		for _, grant := range saved.OAuthCredentials {
			if grant.OwnerUserID != record.OwnerUserID || grant.SpaceID != record.ID {
				return ErrIdentity
			}
			out = append(out, gitlabGrantReference{grant.ApplicationID, record.OwnerUserID, record.ID, grant.ID, grant.UserID, grant.State})
		}
		return nil
	})
	return out, err
}

// This control-plane inventory is read-only. A service use holds the application
// gate while inspecting atomic snapshots, so a concurrent login/refresh cannot
// commit between this check and remote revocation. No Space lock is acquired.
func (s *spaceServer) visitGitLabSnapshots(visit func(SpaceRecord, registry.Snapshot) error) error {
	s.mu.Lock()
	records := append([]SpaceRecord{}, s.directory.Spaces...)
	s.mu.Unlock()
	for _, record := range records {
		var saved registry.Snapshot
		err := readIdentitySnapshot(filepath.Join(record.Roots.Data, "platform-connections.json"), &saved)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ErrInvalid
		}
		if saved.Version != registry.OAuthSnapshotVersion {
			return ErrInvalid
		}
		if err := visit(record, saved); err != nil {
			return err
		}
	}
	return nil
}
func (s *spaceServer) otherGitLabGrant(applicationID string, nativeUserID int64, spaceID string) (bool, error) {
	sharing := false
	err := s.visitGitLabSnapshots(func(record SpaceRecord, saved registry.Snapshot) error {
		for _, grant := range saved.OAuthCredentials {
			if grant.ApplicationID == applicationID && grant.UserID == nativeUserID && grant.State != "logged_out" {
				sharing = true
			}
		}
		for _, cleanup := range saved.OAuthCleanup {
			if record.ID != spaceID && cleanup.ApplicationID == applicationID && cleanup.State != "revoked" && (cleanup.NativeUserID == 0 || cleanup.NativeUserID == nativeUserID) {
				sharing = true
			}
		}
		return nil
	})
	return sharing, err
}
