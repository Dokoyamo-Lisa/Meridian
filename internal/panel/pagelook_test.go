package panel

import (
	"fmt"
	"html"
	"slices"
	"strings"
	"testing"
)

// TestPageLooks: the pages the panel writes by itself open in the site's look - every look has its
// colours, "auto" follows the device, anything else is Romance - and the region notice escapes the
// site's name.
func TestPageLooks(t *testing.T) {
	for _, tone := range siteTones {
		if _, ok := pageTones[tone]; !ok {
			t.Errorf("no colours for the look %s", tone)
		}
	}
	for tone := range pageTones {
		if !slices.Contains(siteTones, tone) {
			t.Errorf("colours for a look there is not: %s", tone)
		}
	}
	if css := string(lookCSS("romance")); !strings.Contains(css, "--page:#fbf0f2") || !strings.Contains(css, "--accent:#c13a66") || !strings.Contains(css, "--display-style:italic") {
		t.Errorf("romance: %s", css)
	}
	if css := string(lookCSS("auto")); !strings.Contains(css, "--page:#07090d") || !strings.Contains(css, "@media (prefers-color-scheme:light){:root{color-scheme:light;--page:#f3efe6") {
		t.Errorf("auto: %s", css)
	}
	if lookCSS("nonsense") != lookCSS("romance") || lookColor("nonsense") != "#fbf0f2" || lookColor("umbrella") != "#0a0a0b" {
		t.Error("an unknown look is not Romance")
	}
	page := fmt.Sprintf(deniedPage, lookCSS("umbrella"), html.EscapeString(`<Rosé & "co">`))
	if !strings.Contains(page, "--page:#0a0a0b") || !strings.Contains(page, "<h1>&lt;Rosé &amp; &#34;co&#34;&gt;</h1>") || strings.Contains(page, "%!") {
		t.Errorf("region notice:\n%s", page)
	}
}
