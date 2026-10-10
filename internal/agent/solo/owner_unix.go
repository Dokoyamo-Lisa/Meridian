//go:build unix

package solo

import (
	"os"
	"syscall"
)

// fileOwner is the owner and group of a file.
func fileOwner(st os.FileInfo) (uid, gid int, ok bool) {
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(s.Uid), int(s.Gid), true
}
