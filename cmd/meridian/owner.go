package main

import (
	"os"
	"syscall"
)

// fileOwner is the user id that owns a file.
func fileOwner(st os.FileInfo) (uint32, bool) {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Uid, true
	}
	return 0, false
}
