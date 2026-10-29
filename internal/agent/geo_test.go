package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meridian/internal/proto"
)

// geoFile writes a country list as the agent keeps it (geo-<sha256>.json) and returns its hash.
func geoFile(t *testing.T, dir string, list proto.GeoList) string {
	t.Helper()
	body, _ := json.Marshal(list)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, "geo-"+hash+".json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return hash
}

// TestGeoFailsClosed: after a restart, with the country list neither on disk nor to be fetched, the
// agent does not apply a table without the country rule over the one in force - it leaves the
// firewall as it was. A list that changed while the panel was out of reach keeps the last good one
// on disk in force; a damaged copy is never used; with no table in force there is nothing to keep
// and the rest applies.
func TestGeoFailsClosed(t *testing.T) {
	dir := t.TempDir()
	old := DataDir
	DataDir = dir
	t.Cleanup(func() { DataDir = old })
	down := func(context.Context, string) ([]byte, error) { return nil, errors.New("panel unreachable") }
	ctx := context.Background()
	missing := strings.Repeat("ab", 32)

	// restart (nothing in memory), no copy on disk, the fetch fails
	a := &Agent{fetchGeo: down}
	rule := &proto.GeoRule{Mode: "block", List: missing}
	g, err := a.geoSpec(ctx, rule)
	if g != nil || err == nil {
		t.Fatalf("no list anywhere: %v %v", g, err)
	}
	if !holdFirewall(rule, g, true) {
		t.Error("a table in force would be replaced by one without the country rule")
	}
	if holdFirewall(rule, g, false) {
		t.Error("with no table in force, nothing would apply at all")
	}
	if holdFirewall(nil, nil, true) || holdFirewall(&proto.GeoRule{Mode: "off"}, nil, true) {
		t.Error("no country rule wanted, yet the firewall is held")
	}

	// a damaged copy of the wanted list, and the fetch fails: still nothing to build from
	if err := os.WriteFile(filepath.Join(dir, "geo-"+missing+".json"), []byte(`{"v4":["198.51.100.0/24"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if g, err := (&Agent{fetchGeo: down}).geoSpec(ctx, rule); g != nil || err == nil {
		t.Errorf("a damaged copy was used: %v", g)
	}

	// the rule's list changed while the panel was out of reach: the previous, whole list on disk
	// stays in force (and is reported), across a restart
	prev := geoFile(t, dir, proto.GeoList{V4: []string{"203.0.113.0/24"}})
	a = &Agent{fetchGeo: down}
	g, err = a.geoSpec(ctx, &proto.GeoRule{Mode: "allow", List: missing, Except: []string{"192.0.2.7"}})
	if g == nil || err == nil || !strings.Contains(err.Error(), "keeping the previous") {
		t.Fatalf("the previous list on disk: %v %v", g, err)
	}
	if !g.Allow || len(g.V4) != 1 || g.V4[0] != "203.0.113.0/24" || len(g.Except) != 1 {
		t.Errorf("the previous list's rule: %+v", g)
	}
	if holdFirewall(rule, g, true) {
		t.Error("held although a list is there to apply")
	}
	// later applies keep it while the fetch still fails
	if g2, err := a.geoSpec(ctx, &proto.GeoRule{Mode: "allow", List: missing}); g2 == nil || err == nil || a.geo.hash != prev {
		t.Errorf("the next apply: %v %v", g2, err)
	}

	// the wanted list on disk, whole: used, no fetch needed
	want := geoFile(t, dir, proto.GeoList{V4: []string{"198.51.100.0/24"}})
	if g, err := (&Agent{fetchGeo: down}).geoSpec(ctx, &proto.GeoRule{Mode: "block", List: want}); err != nil || g == nil || g.V4[0] != "198.51.100.0/24" {
		t.Errorf("the wanted list on disk: %v %v", g, err)
	}
}
