package geo

import (
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCountriesWithRealDatabase runs when MERIDIAN_GEO_TEST_DIR points at a data directory holding
// a downloaded dbip-country-lite database (geo/dbip-country-lite-*.mmdb).
func TestCountriesWithRealDatabase(t *testing.T) {
	dir := os.Getenv("MERIDIAN_GEO_TEST_DIR")
	if dir == "" {
		t.Skip("set MERIDIAN_GEO_TEST_DIR to a data directory with a country database")
	}
	d := Open(dir)
	if !d.CountryReady() {
		t.Fatal("country database not loaded")
	}
	start := time.Now()
	l, err := d.Countries([]string{"cn", "US", "xx", "US"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CN+US: %d IPv4 ranges, %d IPv6 ranges, %d KB, built in %s", len(l.V4), len(l.V6), len(l.Body)/1024, time.Since(start))
	// a lookup names the country even when only the country database is there
	if g := d.Lookup("::ffff:8.8.8.8"); g == nil || g.Country != "US" {
		t.Errorf("lookup: %+v", g)
	}
	if strings.Join(l.Countries, ",") != "CN,US" || len(l.V4) < 1000 || len(l.V6) < 100 || len(l.Hash) != 64 {
		t.Fatalf("unexpected list: %v %d %d", l.Countries, len(l.V4), len(l.V6))
	}
	// ranges are well formed, ordered and do not overlap
	var prev netip.Addr
	for _, r := range l.V4 {
		from, to, ok := strings.Cut(r, "-")
		a := netip.MustParseAddr(from)
		b := a
		if ok {
			b = netip.MustParseAddr(to)
		}
		if !a.Is4() || b.Less(a) || (prev.IsValid() && !prev.Less(a)) {
			t.Fatalf("bad range %q after %s", r, prev)
		}
		prev = b
	}
	// a second call with the same countries is cached
	if l2, _ := d.Countries([]string{"US", "CN"}); l2 != l {
		t.Fatal("not cached")
	}
	// well-known addresses land in the right list
	in := func(list []string, ip string) bool {
		x := netip.MustParseAddr(ip)
		for _, r := range list {
			from, to, ok := strings.Cut(r, "-")
			a := netip.MustParseAddr(from)
			b := a
			if ok {
				b = netip.MustParseAddr(to)
			}
			if !x.Less(a) && !b.Less(x) {
				return true
			}
		}
		return false
	}
	if !in(l.V4, "8.8.8.8") || !in(l.V4, "114.114.114.114") || in(l.V4, "1.1.1.1") || in(l.V4, "193.0.14.129") {
		t.Fatal("well-known addresses in the wrong place")
	}
	if got := lastAddr(netip.MustParsePrefix("10.1.2.0/23")); got.String() != "10.1.3.255" {
		t.Fatalf("lastAddr = %s", got)
	}
}
