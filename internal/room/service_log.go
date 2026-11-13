package room

import (
	"errors"
	"os"
	"strconv"
	"sync"
)

// The launchd supervisor owns both child output streams and reopens the log
// after rotation. No copytruncate race or signal to a running Agent is needed.
type serviceLog struct {
	mu    sync.Mutex
	path  string
	limit int64
	keep  int
}

func (l *serviceLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	info, err := os.Lstat(l.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return 0, errors.New("service log must be a private regular file")
		}
		if info.Size() >= l.limit {
			for i := l.keep; i > 0; i-- {
				old := l.path
				if i > 1 {
					old += "." + strconv.Itoa(i-1)
				}
				if err := os.Rename(old, l.path+"."+strconv.Itoa(i)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return 0, err
				}
			}
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	n, err := f.Write(b)
	return n, errors.Join(err, f.Close())
}
