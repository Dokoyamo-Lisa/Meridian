package main

import "syscall"

// privateUmask makes new files readable by their owner only and returns a function restoring the
// previous mask.
func privateUmask() func() {
	old := syscall.Umask(0o077)
	return func() { syscall.Umask(old) }
}
