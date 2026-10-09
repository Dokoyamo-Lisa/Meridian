package agent

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// shell is a login shell on a terminal of its own.
type shell struct {
	pty  *os.File
	cmd  *exec.Cmd
	once sync.Once
	code int
	done chan struct{}
}

// openPTY opens a new pseudo-terminal: the controller and the terminal end the shell gets.
func openPTY() (ctl, term *os.File, err error) {
	ctl, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := int(ctl.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil { // unlockpt
		ctl.Close()
		return nil, nil, err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN) // ptsname
	if err != nil {
		ctl.Close()
		return nil, nil, err
	}
	term, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		ctl.Close()
		return nil, nil, err
	}
	return ctl, term, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// startShell starts root's login shell with a terminal of cols x rows. Under systemd the shell gets a
// scope of its own, so what is started from it outlives an agent restart.
func startShell(cols, rows int) (*shell, error) {
	ctl, term, err := openPTY()
	if err != nil {
		return nil, err
	}
	defer term.Close()
	sh := "/bin/sh"
	if exists("/bin/bash") {
		sh = "/bin/bash"
	}
	argv := []string{sh, "-l"}
	if exists("/run/systemd/system") {
		if p, err := exec.LookPath("systemd-run"); err == nil {
			argv = append([]string{p, "--scope", "--quiet", "--collect", "--description=Meridian console", "--"}, argv...)
		}
	}
	home := "/root"
	if !exists(home) {
		home = "/"
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = home
	cmd.Env = []string{"TERM=xterm-256color", "LANG=C.UTF-8", "HOME=" + home, "USER=root", "LOGNAME=root", "SHELL=" + sh,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = term, term, term
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	s := &shell{pty: ctl, cmd: cmd, done: make(chan struct{})}
	s.resize(cols, rows)
	if err := cmd.Start(); err != nil {
		ctl.Close()
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			s.code = ee.ExitCode()
		}
		close(s.done)
	}()
	return s, nil
}

func (s *shell) resize(cols, rows int) {
	if cols < 1 || rows < 1 || cols > 1000 || rows > 1000 {
		return
	}
	_ = unix.IoctlSetWinsize(int(s.pty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
}

// wait waits for the shell to end and says how it ended.
func (s *shell) wait() int {
	<-s.done
	return s.code
}

// close ends the shell: a hang-up to its terminal's processes, then, if they linger, a kill.
func (s *shell) close() {
	s.once.Do(func() {
		s.pty.Close()
		if p := s.cmd.Process; p != nil {
			_ = syscall.Kill(-p.Pid, syscall.SIGHUP)
			select {
			case <-s.done:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
			}
		}
	})
}
