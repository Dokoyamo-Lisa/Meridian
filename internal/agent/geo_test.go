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
	"time"

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
// firewall as it was. A damaged copy is never used; with no table and no rule in force there is
// nothing to keep and the rest applies.
func TestGeoFailsClosed(t *testing.T) {
	dir := t.TempDir()
	old := DataDir
	DataDir = dir
	t.Cleanup(func() { DataDir = old })
	down := func(context.Context, string) ([]byte, error) { return nil, errors.New("panel unreachable") }
	ctx := context.Background()
	missing := strings.Repeat("ab", 32)

	// restart (nothing in memory), no copy on disk, the fetch fails
	rule := &proto.GeoRule{Mode: "block", List: missing}
	g, err := (&Agent{fetchGeo: down}).geoSpec(ctx, rule)
	if g != nil || err == nil {
		t.Fatalf("no list anywhere: %v %v", g, err)
	}
	if !holdFirewall(rule, geoOf(g), true) {
		t.Error("a table in force would be replaced by one without the country rule")
	}
	if holdFirewall(rule, geoOf(g), false) {
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

	// the wanted list on disk, whole: used, no fetch needed
	want := geoFile(t, dir, proto.GeoList{V4: []string{"198.51.100.0/24"}})
	if g, err := (&Agent{fetchGeo: down}).geoSpec(ctx, &proto.GeoRule{Mode: "block", List: want}); err != nil || g == nil || g.spec.V4[0] != "198.51.100.0/24" {
		t.Errorf("the wanted list on disk: %v %v", g, err)
	}
}

// TestGeoChangedOffline: the rule changes to other countries (or from block to allow) while the
// panel's list cannot be fetched. What stays is the rule in force - its own countries, mode and
// exceptions - in memory, across a restart and across a reboot (no table), never another list that
// happens to be on disk: a block list for one country applied as a rule for others would let in the
// countries kept out. A list on disk that was never applied is not a rule in force.
func TestGeoChangedOffline(t *testing.T) {
	dir := t.TempDir()
	old := DataDir
	DataDir = dir
	t.Cleanup(func() { DataDir = old })
	down := func(context.Context, string) ([]byte, error) { return nil, errors.New("panel unreachable") }
	ctx := context.Background()
	const cnNet, usNet, otherNet = "203.0.113.0/24", "198.51.100.0/24", "192.0.2.0/24"
	cn := geoFile(t, dir, proto.GeoList{V4: []string{cnNet}})
	us := strings.Repeat("cd", 32) // block US: a list the agent has not seen

	// an unproven list on disk (not applied, no record): not used for another rule
	if g, err := (&Agent{fetchGeo: down}).geoSpec(ctx, &proto.GeoRule{Mode: "block", List: us}); g != nil || err == nil {
		t.Fatalf("a list never applied was taken for a new rule: %+v", g)
	}

	// block CN is applied
	a := &Agent{fetchGeo: down}
	g, err := a.geoSpec(ctx, &proto.GeoRule{Mode: "block", List: cn, Except: []string{"192.0.2.7"}})
	if err != nil || g == nil {
		t.Fatal(err)
	}
	a.setGeoActive(g)
	// a newer list for other countries lands on disk before it is applied (fetched, then nft failed)
	stray := geoFile(t, dir, proto.GeoList{V4: []string{otherNet}})
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "geo-"+stray+".json"), future, future)

	inForce := func(what string, g *geoCache, err error) {
		t.Helper()
		if g == nil || err == nil || !strings.Contains(err.Error(), "in force stays") {
			t.Fatalf("%s: %+v %v", what, g, err)
		}
		if g.hash != cn || g.spec.Allow || len(g.spec.V4) != 1 || g.spec.V4[0] != cnNet || len(g.spec.Except) != 1 {
			t.Errorf("%s: not the rule in force (block CN): allow=%v %v except %v", what, g.spec.Allow, g.spec.V4, g.spec.Except)
		}
		if holdFirewall(&proto.GeoRule{Mode: "block", List: us}, geoOf(g), true) {
			t.Errorf("%s: held although the rule in force can be applied", what)
		}
	}
	// the panel changes the rule to block US, then to allow US; the fetch fails
	for _, r := range []*proto.GeoRule{{Mode: "block", List: us}, {Mode: "allow", List: us}, {Mode: "allow", List: stray}} {
		g, err := a.geoSpec(ctx, r)
		if r.List == stray { // on disk and whole: the panel's own list for that rule, so it is used
			if err != nil || g == nil || g.spec.V4[0] != otherNet || !g.spec.Allow {
				t.Errorf("the rule's own list on disk: %+v %v", g, err)
			}
			continue
		}
		inForce("changed to "+r.Mode+" US, in memory", g, err)
	}
	// a restart, then a reboot (nftables empty): the record and its list on disk bring block CN back
	g, err = (&Agent{fetchGeo: down}).geoSpec(ctx, &proto.GeoRule{Mode: "allow", List: us})
	inForce("after a restart", g, err)
	if holdFirewall(&proto.GeoRule{Mode: "allow", List: us}, geoOf(g), false) || geoOf(g) == nil {
		t.Error("after a reboot the table would be applied without the rule in force")
	}

	// the panel sends a body that is not the list the rule names: refused, the rule in force stays
	bad := func(context.Context, string) ([]byte, error) { return []byte(`{"v4":["0.0.0.0/0"]}`), nil }
	g, err = (&Agent{fetchGeo: bad}).geoSpec(ctx, &proto.GeoRule{Mode: "allow", List: us})
	if g == nil || g.hash != cn || err == nil {
		t.Errorf("a list that does not match its hash: %+v %v", g, err)
	}

	// the new list arrives and is applied: it is the rule in force, and the old list goes
	body, _ := json.Marshal(proto.GeoList{V4: []string{usNet}})
	sum := sha256.Sum256(body)
	us = hex.EncodeToString(sum[:])
	up := func(context.Context, string) ([]byte, error) { return body, nil }
	a = &Agent{fetchGeo: up}
	g, err = a.geoSpec(ctx, &proto.GeoRule{Mode: "block", List: us})
	if err != nil || g.spec.V4[0] != usNet {
		t.Fatalf("the new list: %+v %v", g, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "geo-"+cn+".json")); err != nil {
		t.Error("the list in force was deleted before the new one was applied")
	}
	a.setGeoActive(g)
	if _, err := os.Stat(filepath.Join(dir, "geo-"+cn+".json")); err == nil {
		t.Error("the old list is still kept")
	}
	if g := (&Agent{}).geoInForce(); g == nil || g.hash != us {
		t.Errorf("the record after the new rule: %+v", g)
	}
	// the rule is turned off and applied: no rule in force any more
	a.setGeoActive(nil)
	if g := (&Agent{}).geoInForce(); g != nil {
		t.Errorf("a rule turned off is still in force: %+v", g)
	}
}
