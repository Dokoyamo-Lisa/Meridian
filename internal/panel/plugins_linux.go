package panel

import "syscall"

// pluginProcAttr gives a plugin's program a process group of its own, so it stops together with
// whatever it starts, and ends it when the panel's process ends, however that happens.
func pluginProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
