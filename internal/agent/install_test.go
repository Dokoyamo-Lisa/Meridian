package agent

import (
	"net"
	"strconv"
	"testing"
)

// The agent takes two loopback ports in a row; a pair with either port taken is skipped.
func TestFreePortSkipsTakenPairs(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	taken := l.Addr().(*net.TCPAddr).Port
	for _, start := range []int{taken, taken - 1} { // taken as the first port, then as the second
		got := freePort(start)
		if got == taken || got+1 == taken {
			t.Fatalf("freePort(%d) = %d, overlapping the taken port %d", start, got, taken)
		}
		for _, p := range []int{got, got + 1} {
			c, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
			if err != nil {
				t.Fatalf("freePort(%d) = %d, but %d is not free: %v", start, got, p, err)
			}
			c.Close()
		}
	}
	if DefaultAPIPort != 50000 {
		t.Errorf("default port %d", DefaultAPIPort)
	}
}
