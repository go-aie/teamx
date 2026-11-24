package room

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type computerCredentials struct{ home string }

func (s computerCredentials) put(value string) (string, error) {
	if len(value) < 32 || len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
		return "", invalidField("credential", "管理密钥需要 32–4096 个字符。")
	}
	dir := filepath.Join(s.home, "computer-credentials")
	if err := makeDurableDir(dir); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", errors.New("Computer credential directory must be private (0700)")
	}
	ref := "computer-" + newComputerID()
	path := filepath.Join(dir, ref)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("cannot create private Computer credential")
	}
	_, err = f.WriteString(value)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = syncDirectory(dir)
	}
	if err != nil {
		_ = os.Remove(path)
		return "", errors.New("cannot save private Computer credential")
	}
	return ref, nil
}
func (s computerCredentials) get(ref string) (string, error) {
	fail := func() (string, error) {
		return "", errors.New("Computer credential unavailable; enter the management key again")
	}
	if !validComputerCredentialRef(ref) {
		return fail()
	}
	dir := filepath.Join(s.home, "computer-credentials")
	d, err := os.Lstat(dir)
	if err != nil || !d.IsDir() || d.Mode().Perm() != 0700 {
		return fail()
	}
	path := filepath.Join(dir, ref)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
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
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) < 32 || len(raw) > 4096 || strings.TrimSpace(string(raw)) != string(raw) || strings.ContainsAny(string(raw), "\r\n\x00") {
		return fail()
	}
	return string(raw), nil
}
