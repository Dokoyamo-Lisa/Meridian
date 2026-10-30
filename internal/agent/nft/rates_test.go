package nft

import (
	"strconv"
	"strings"
	"testing"
)

// TestRenderRates: the counting table counts only the users' protocols' ports, after the
// firewall's own drops; what this server sends to a proxy pass's exit (from a port of its own) is
// never counted as a client's.
func TestRenderRates(t *testing.T) {
	s := renderRates([]int{443, 443, 8443}, []int{51820})
	for _, want := range []string{
		"table inet meridian_rate {",
		"set ports_t { type inet_service; elements = { 443, 8443 }; }",
		"set ports_u { type inet_service; elements = { 51820 }; }",
		"type filter hook input priority filter + 10;",
		"tcp dport @ports_t update @up4 { ip saddr counter }",
		"udp dport @ports_u update @up6 { ip6 saddr counter }",
		"tcp sport @ports_t update @dn4 { ip daddr counter }",
		"udp sport @ports_u update @dn6 { ip6 daddr counter }",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "dport @ports_t update @dn") || strings.Contains(s, "sport @ports_t update @up") {
		t.Error("a direction is counted the wrong way round")
	}
}

// TestTakeIPBytes: what each address sent and received since the last read, from the table as nft
// lists it (the JSON of nftables 1.1.6); an address that expired and came back counts from zero.
func TestTakeIPBytes(t *testing.T) {
	table := func(up, down string) func() ([]byte, error) {
		return func() ([]byte, error) {
			return []byte(`{"nftables": [{"metainfo": {"version": "1.1.6"}},
				{"set": {"family": "inet", "name": "up4", "table": "meridian_rate", "elem": [` + up + `]}},
				{"set": {"family": "inet", "name": "dn6", "table": "meridian_rate", "elem": [` + down + `]}},
				{"set": {"family": "inet", "name": "ports_t", "table": "meridian_rate", "elem": [443]}}]}`), nil
		}
	}
	elem := func(ip string, bytes int) string {
		return `{"elem": {"val": "` + ip + `", "expires": 118, "counter": {"packets": 3, "bytes": ` + strconv.Itoa(bytes) + `}}}`
	}
	old := readRates
	t.Cleanup(func() { readRates = old; rateState.ports = ""; rateState.last = map[string][2]uint64{} })
	rateState.ports, rateState.last = "applied", map[string][2]uint64{}

	readRates = table(elem("198.51.100.7", 1000), elem("2001:db8::5", 300))
	got := TakeIPBytes()
	if got["198.51.100.7"] != [2]int64{1000, 0} || got["2001:db8::5"] != [2]int64{0, 300} || len(got) != 2 {
		t.Fatalf("first read: %v", got)
	}
	readRates = table(elem("198.51.100.7", 4000), elem("2001:db8::5", 300))
	if got := TakeIPBytes(); got["198.51.100.7"] != [2]int64{3000, 0} || len(got) != 1 {
		t.Errorf("second read: %v", got)
	}
	readRates = table(elem("198.51.100.7", 500), "") // expired and back
	if got := TakeIPBytes(); got["198.51.100.7"] != [2]int64{500, 0} || len(got) != 1 {
		t.Errorf("an address back after expiring: %v", got)
	}
	rateState.ports = "" // not applied here (no nftables, or the host refused the table)
	if TakeIPBytes() != nil {
		t.Error("read a table that is not there")
	}
}
