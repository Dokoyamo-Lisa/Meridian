//go:build !linux

package panel

import "syscall"

// pluginProcAttr gives a plugin's program a process group of its own, so it stops together with
// whatever it starts (a development machine: Meridian runs on Linux).
func pluginProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
