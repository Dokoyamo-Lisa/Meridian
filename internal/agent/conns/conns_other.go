//go:build !linux

package conns

import "errors"

// ErrUnsupported: connections are cut on Linux only (the agent runs nowhere else).
var ErrUnsupported = errors.New("closing connections needs Linux")

// Open lists the TCP connections open now on the given local ports (Linux only).
func Open(map[int]bool) (map[Target]bool, error) { return nil, ErrUnsupported }

// Kill closes the server's side of these TCP connections (Linux only).
func Kill(ts []Target, froms ...From) (int, error) {
	if len(wanted(ts)) == 0 && len(froms) == 0 {
		return 0, nil
	}
	return 0, ErrUnsupported
}
