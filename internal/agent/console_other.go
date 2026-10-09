//go:build !linux

package agent

import (
	"errors"
	"os"
)

// The console needs Linux; the agent builds elsewhere only for development.
type shell struct{ pty *os.File }

func startShell(cols, rows int) (*shell, error) {
	return nil, errors.New("the console runs on Linux only")
}

func (s *shell) resize(cols, rows int) {}
func (s *shell) wait() int             { return 0 }
func (s *shell) close()                {}
