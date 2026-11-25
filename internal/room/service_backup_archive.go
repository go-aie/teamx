//go:build darwin || linux

package room

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const serviceBackupArchive = "snapshot.tar"

type serviceBackupReader struct {
	ctx context.Context
	io.Reader
}

func (r serviceBackupReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(b)
}

// One metadata walk and one sequential write; no per-file hashes or fsyncs.
// The private archive and its containing directory are synced before publish.
func writeServiceBackupArchive(ctx context.Context, r serviceRegistration, name string) (string, int64, error) {
	out, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", 0, err
	}
	defer out.Close()
	hash := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(out, hash))
	roots := []struct{ name, source string }{{"home", r.Home}, {"server", r.Root}}
	if r.Unit != "" {
		if info, err := os.Lstat(r.Unit); err == nil && info.Mode().IsRegular() {
			roots = append(roots, struct{ name, source string }{"service-unit", r.Unit})
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", 0, err
		}
	}
	for _, root := range roots {
		err := filepath.WalkDir(root.source, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				link, err = os.Readlink(name)
				resolved, resolveErr := filepath.EvalSymlinks(name)
				if err != nil || resolveErr != nil || filepath.IsAbs(link) || !pathWithin(root.source, resolved) {
					return fmt.Errorf("unsupported or escaping backup link: %s", name)
				}
			} else if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported backup file: %s", name)
			}
			header, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root.source, name)
			if err != nil {
				return err
			}
			header.Name, header.Mode = path.Join(root.name, filepath.ToSlash(rel)), int64(info.Mode().Perm())
			if strings.ContainsAny(header.Name, "\\\r\n\x00") {
				return errors.New("unsupported backup path")
			}
			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			in, err := os.Open(name)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, serviceBackupReader{ctx, in})
			return errors.Join(copyErr, in.Close())
		})
		if err != nil {
			return "", 0, err
		}
	}
	if err := errors.Join(tw.Close(), out.Sync()); err != nil {
		return "", 0, err
	}
	info, err := out.Stat()
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), info.Size(), out.Close()
}

// Verify the complete archive in one sequential pass. Extraction uses a fresh
// private stage, and creates links last so archive entries cannot traverse them.
func readServiceBackupArchive(ctx context.Context, dir string, b serviceBackup, stage string) error {
	if stage != "" {
		var err error
		stage, err = filepath.EvalSymlinks(stage)
		if err != nil {
			return err
		}
	}
	name := filepath.Join(dir, serviceBackupArchive)
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != b.ArchiveSize || b.ArchiveSize <= 0 || len(b.ArchiveSHA256) != 64 {
		return errors.New("backup archive missing or invalid size, permissions or digest")
	}
	in, err := os.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	hash := sha256.New()
	reader := io.TeeReader(serviceBackupReader{ctx, in}, hash)
	tr := tar.NewReader(reader)
	seen := map[string]bool{}
	type link struct{ name, target, root string }
	var links []link
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		root := strings.Split(name, "/")[0]
		if name == "" || name == "." || name == ".." || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\r\n\x00") || (root != "home" && root != "server" && name != "service-unit") || seen[name] || header.Size < 0 || header.Mode&^0777 != 0 {
			return fmt.Errorf("invalid backup archive entry: %q", header.Name)
		}
		seen[name] = true
		dest := filepath.Join(stage, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if stage != "" {
				if err := os.MkdirAll(dest, fs.FileMode(header.Mode)|0700); err != nil {
					return err
				}
			}
		case tar.TypeReg:
			var target io.Writer = io.Discard
			var out *os.File
			if stage != "" {
				if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
					return err
				}
				out, err = os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode))
				if err != nil {
					return err
				}
				target = out
			}
			_, copyErr := io.Copy(target, serviceBackupReader{ctx, tr})
			if out != nil {
				copyErr = errors.Join(copyErr, out.Chmod(fs.FileMode(header.Mode)), out.Close())
			}
			if copyErr != nil {
				return copyErr
			}
		case tar.TypeSymlink:
			resolved := path.Clean(path.Join(path.Dir(name), header.Linkname))
			if header.Linkname == "" || path.IsAbs(header.Linkname) || strings.ContainsAny(header.Linkname, "\\\r\n\x00") || !strings.HasPrefix(resolved, root+"/") {
				return errors.New("backup link escapes its managed root")
			}
			links = append(links, link{name, header.Linkname, root})
		default:
			return fmt.Errorf("unsupported backup archive entry: %s", name)
		}
	}
	// Include tar padding/trailer bytes in the whole-file checksum.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != b.ArchiveSHA256 {
		return errors.New("backup archive SHA-256 mismatch")
	}
	if !seen["home/config.toml"] || !seen["server/releases/"+b.Current+"/manifest.json"] {
		return errors.New("backup lacks configuration or current release")
	}
	if stage != "" {
		for _, link := range links {
			dest := filepath.Join(stage, filepath.FromSlash(link.name))
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return err
			}
			parent, err := filepath.EvalSymlinks(filepath.Dir(dest))
			if err != nil || !pathWithin(filepath.Join(stage, link.root), parent) {
				return errors.New("backup link parent escapes stage")
			}
			if err := os.Symlink(link.target, dest); err != nil {
				return err
			}
		}
		for _, link := range links {
			resolved, err := filepath.EvalSymlinks(filepath.Join(stage, filepath.FromSlash(link.name)))
			if err != nil || !pathWithin(filepath.Join(stage, link.root), resolved) {
				return errors.New("backup link target escapes stage or is missing")
			}
		}
	}
	return nil
}
