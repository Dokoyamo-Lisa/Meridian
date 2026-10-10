package panel

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAdoptIdentity: settings from before Rosélune move to it - a panel still called Meridian takes the
// new name, the rose and the Romance look (a look picked by hand stays); a panel with its own name
// keeps all it showed; settings written since are left alone.
func TestAdoptIdentity(t *testing.T) {
	load := func(raw string) (Settings, string) {
		s := defaultSettings()
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatal(err)
		}
		msg := adoptIdentity([]byte(raw), &s)
		s.normalize()
		return s, msg
	}
	for _, c := range []struct {
		raw, title, tone, mark, says string
	}{
		{`{"site_title":"Meridian","default_tone":""}`, "Rosélune", "romance", "rose", "took its new name"},
		{`{"site_title":"Meridian","default_tone":"paper"}`, "Rosélune", "paper", "rose", "took its new name"},
		{`{"site_title":"","default_tone":""}`, "Rosélune", "romance", "rose", "took its new name"},
		{`{"site_title":"Umbrella","default_tone":""}`, "Umbrella", "auto", "umbrella", "keeps its name"},
		{`{"site_title":"Umbrella","default_tone":"umbrella"}`, "Umbrella", "umbrella", "umbrella", "keeps its name"},
		{`{"site_title":"Meridian","default_tone":"ice","logo_mark":"rose"}`, "Meridian", "ice", "rose", ""},
		{`{"site_title":"Umbrella","default_tone":"auto","logo_mark":"rose"}`, "Umbrella", "auto", "rose", ""},
	} {
		s, msg := load(c.raw)
		if s.SiteTitle != c.title || s.DefaultTone != c.tone || s.LogoMark != c.mark || (c.says == "") != (msg == "") || !strings.Contains(msg, c.says) {
			t.Errorf("%s: %q %q %q - %q", c.raw, s.SiteTitle, s.DefaultTone, s.LogoMark, msg)
		}
	}
}

// TestIdentityOnStart: a panel that starts with settings from before 1.3 stores what it adopted, says
// so in its events once, and keeps it from then on.
func TestIdentityOnStart(t *testing.T) {
	h := newHarness(t)
	if _, err := h.p.db.Exec1(`UPDATE settings SET value = ? WHERE key = 'panel'`, `{"site_title":"Umbrella","default_tone":"","timezone":"UTC"}`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.p.loadSettings(); err != nil {
			t.Fatal(err)
		}
	}
	s := h.p.settings()
	if s.SiteTitle != "Umbrella" || s.DefaultTone != "auto" || s.LogoMark != "umbrella" {
		t.Errorf("after the move: %+v", s)
	}
	var n int
	if err := h.p.db.QueryRow(`SELECT COUNT(*) FROM events WHERE message LIKE 'Meridian is now called Rosélune%'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("events about the move: %d %v", n, err)
	}
	var raw string
	if err := h.p.db.QueryRow(`SELECT value FROM settings WHERE key = 'panel'`).Scan(&raw); err != nil || !strings.Contains(raw, `"logo_mark":"umbrella"`) {
		t.Errorf("stored: %s %v", raw, err)
	}
}
