package service

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestAliveZombie: a process that exited but was not reaped yet is not running.
func TestAliveZombie(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	running := exec.Command("sleep", "5")
	if err := running.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { _ = running.Process.Kill(); _ = running.Wait() }()
	if !alive(running.Process.Pid) {
		t.Error("a running process counted as dead")
	}
	dead := exec.Command("true")
	if err := dead.Start(); err != nil {
		t.Skip(err)
	}
	// not waited for: it stays a zombie until Wait
	for i := 0; i < 50 && alive(dead.Process.Pid); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(dead.Process.Pid) {
		t.Error("a zombie counted as running")
	}
	_ = dead.Wait()
	if alive(0) || alive(-1) || alive(os.Getpid()) == false {
		t.Error("edge cases")
	}
}
