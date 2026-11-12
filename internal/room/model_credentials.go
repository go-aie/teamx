package room

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Immutable versions are deliberately retained for Session/operation recovery.
// Only this private store contains key bytes; models and snapshots contain refs.
type modelCredentials struct{ dir string }

func newModelCredentials(home string) (*modelCredentials, error) {
	if !filepath.IsAbs(home) {
		return nil, ErrRequest
	}
	dir := filepath.Join(home, "model-credentials")
	if err := makeDurableDir(dir); err != nil {
		return nil, errors.New("cannot prepare private model credentials")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("model credential directory must be private (0700)")
	}
	return &modelCredentials{dir}, nil
}

func validCredentialRef(ref string) bool {
	return strings.HasPrefix(ref, "cred-") && validRestartID(strings.TrimPrefix(ref, "cred-"))
}

func (s *modelCredentials) put(value string) (string, error) {
	if strings.TrimSpace(value) == "" || len(value) > 64<<10 || strings.ContainsRune(value, 0) {
		return "", invalidField("api_key", "enter a valid API key")
	}
	id, err := newOpaqueID()
	if err != nil {
		return "", err
	}
	ref := "cred-" + id
	f, err := os.CreateTemp(s.dir, ".credential-*")
	if err != nil {
		return "", errors.New("cannot save private model credential")
	}
	defer f.Close()
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		return "", errors.New("cannot protect model credential")
	}
	if _, err = f.WriteString(value); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Close()
	}
	// Link creates the immutable destination exclusively; never overwrite a ref.
	linked := false
	if err == nil {
		err = os.Link(f.Name(), filepath.Join(s.dir, ref))
		linked = err == nil
	}
	if err == nil {
		err = defaultSnapshotOps().syncDir(s.dir)
	}
	if err != nil {
		if linked {
			_ = os.Remove(filepath.Join(s.dir, ref))
		}
		return "", errors.New("cannot save private model credential")
	}
	return ref, nil
}

func (s *modelCredentials) get(ref string) (*dsh.Credential, error) {
	fail := func() (*dsh.Credential, error) {
		return nil, errors.New("model credential unavailable; enter the API key again")
	}
	if s == nil || !validCredentialRef(ref) {
		return fail()
	}
	path := filepath.Join(s.dir, ref)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 {
		return fail()
	}
	f, err := os.Open(path)
	if err != nil {
		return fail()
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fail()
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || strings.TrimSpace(string(data)) == "" || strings.ContainsRune(string(data), 0) {
		return fail()
	}
	return &dsh.Credential{Ref: ref, Value: string(data)}, nil
}

// Used only by the later one-time migration, preserving explicit empty exports.
func legacyModelCredential(name string, configured map[string]string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return configured[name]
}
