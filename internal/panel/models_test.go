package panel

import "testing"

// TestCoreVersionsAreRaisedPastSecurityFixes: a saved Xray or mieru older than the last release with
// a security fix becomes today's default; a newer one, or another core, stays as it was.
func TestCoreVersionsAreRaisedPastSecurityFixes(t *testing.T) {
	d := defaultSettings()
	s := defaultSettings()
	s.XrayVersion, s.MitaVersion, s.HysteriaVersion = "v26.3.27", "3.38.0", "2.12.3"
	s.normalize()
	if s.XrayVersion != d.XrayVersion || s.MitaVersion != d.MitaVersion {
		t.Errorf("not raised: xray %s, mita %s", s.XrayVersion, s.MitaVersion)
	}
	if s.HysteriaVersion != "2.12.3" {
		t.Errorf("hysteria changed: %s", s.HysteriaVersion)
	}
	s.XrayVersion, s.MitaVersion = "26.12.1", "3.41.0"
	s.normalize()
	if s.XrayVersion != "26.12.1" || s.MitaVersion != "3.41.0" {
		t.Errorf("a newer version was lowered: xray %s, mita %s", s.XrayVersion, s.MitaVersion)
	}
}
