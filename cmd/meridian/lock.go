package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// errLocked: another process holds the data directory (a running panel, or a restore).
var errLocked = errors.New("locked")

// lockData takes the data directory for this process: the panel holds it while it runs, so a restore
// can tell that it must not replace the database. It returns the release.
func lockData(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "meridian.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// chownLike gives path the owner of ref (a restore run as root writes the panel user's database).
func chownLike(path, ref string) error {
	st, err := os.Stat(ref)
	if err != nil {
		return err
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || os.Geteuid() != 0 {
		return nil
	}
	return os.Chown(path, int(s.Uid), int(s.Gid))
}
