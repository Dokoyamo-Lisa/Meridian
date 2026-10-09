//go:build linux

package health

import (
	"os"
	"runtime"
	"syscall"
)

func changeTime(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Sec*1e9 + st.Ctim.Nsec
	}
	return 0
}

func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

// lowerPriority gives the calling goroutine a thread of its own at the lowest CPU priority (nice
// 19) and the idle disk class. The thread is never handed back: it ends with the goroutine, so no
// other work ever runs at that priority by accident.
func lowerPriority() {
	runtime.LockOSThread()
	tid := syscall.Gettid()
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, tid, 19)
	const whoProcess, classIdle = 1, 3
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, whoProcess, uintptr(tid), classIdle<<13)
}
