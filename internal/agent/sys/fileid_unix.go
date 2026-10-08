//go:build unix

package sys

import (
	"os"
	"syscall"
)

// FileID is a file's inode: a log that was replaced, not just grown, has another one.
func FileID(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
