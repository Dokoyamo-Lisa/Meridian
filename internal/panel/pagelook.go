package panel

import (
	"fmt"
	"html/template"
)

// The pages the panel writes by itself - subscription pages, the region notice - open in the site's
// look as well, with the colours below (the app's are in web/src/app.css and themes.css; these are
// the few such a page needs). "auto" follows the device: Ice, or Paper on one set to light.

type pageTone struct {
	scheme                               string // light | dark
	page, surface, line, line2           string
	ink, ink2, ink3, accent, good, warn  string
	display, displayStyle, displayWeight string // headings and figures: font, style, the figures' weight
	glow                                 string // the page's background light
}

const (
	sansDisplay  = `-apple-system,"SF Pro Display",system-ui,sans-serif`
	serifDisplay = `"Iowan Old Style","Palatino Linotype",Palatino,"Book Antiqua",Georgia,serif`
)

var pageTones = map[string]pageTone{
	"romance": {"light", "#fbf0f2", "#fffafb", "rgba(122,30,64,.1)", "rgba(122,30,64,.19)", "#3e1d2b", "#6c4253", "#876071", "#c13a66", "#3d7a4b", "#96600e",
		serifDisplay, "italic", "400",
		"radial-gradient(48vw 34vw at 6% -6%,rgba(255,196,212,.8),transparent 70%),radial-gradient(40vw 30vw at 102% 4%,rgba(226,210,255,.5),transparent 70%),radial-gradient(52vw 40vw at 96% 108%,rgba(255,214,190,.55),transparent 70%)"},
	"umbrella": {"dark", "#0a0a0b", "#111113", "rgba(255,255,255,.085)", "rgba(255,255,255,.17)", "#f3f1ee", "#bcb8b3", "#86827d", "#ff5b63", "#5cc48a", "#e7b544",
		`"Helvetica Neue","Arial Narrow","Roboto Condensed",Arial,sans-serif`, "normal", "300",
		"radial-gradient(70vw 34vw at 50% -12%,rgba(212,42,51,.13),transparent 70%)"},
	"ice": {"dark", "#07090d", "#0c1017", "rgba(205,220,240,.075)", "rgba(205,220,240,.15)", "#e6ebf1", "#a6b1be", "#6f7a89", "#8cc0ff", "#4cc38a", "#e3b341",
		sansDisplay, "normal", "300", "radial-gradient(60vw 40vw at 50% -10%,rgba(90,140,210,.06),transparent 70%)"},
	"celadon": {"dark", "#111413", "#171b1a", "rgba(220,236,230,.07)", "rgba(220,236,230,.14)", "#e7ecea", "#a9b1ae", "#78817e", "#98c6bc", "#8fc589", "#dfb97c",
		sansDisplay, "normal", "300", "none"},
	"ink": {"dark", "#13110f", "#1a1714", "rgba(236,229,216,.09)", "rgba(236,229,216,.2)", "#ece5d8", "#c4bcad", "#958d7f", "#93bde3", "#8dbd84", "#dcaa55",
		`"New York","Charter",Georgia,serif`, "normal", "400", "none"},
	"paper": {"light", "#f3efe6", "#fbf8f2", "rgba(29,27,23,.09)", "rgba(29,27,23,.17)", "#1d1b17", "#4b463e", "#7b746a", "#2c6aa3", "#3b7a44", "#a2690f",
		sansDisplay, "normal", "300", "none"},
	"mist": {"light", "#eef2f1", "#fafcfb", "rgba(24,32,30,.09)", "rgba(24,32,30,.17)", "#18201e", "#43504c", "#74807c", "#487d73", "#448247", "#a8762c",
		sansDisplay, "normal", "300", "none"},
}

func (t pageTone) vars() string {
	return fmt.Sprintf("color-scheme:%s;--page:%s;--surface:%s;--line:%s;--line2:%s;--ink:%s;--ink2:%s;--ink3:%s;--accent:%s;--good:%s;--warn:%s;"+
		"--display:%s;--display-style:%s;--display-weight:%s;--glow:%s",
		t.scheme, t.page, t.surface, t.line, t.line2, t.ink, t.ink2, t.ink3, t.accent, t.good, t.warn, t.display, t.displayStyle, t.displayWeight, t.glow)
}

// lookCSS is the :root rules of a site's look for a page the panel writes.
func lookCSS(tone string) template.CSS {
	if tone == "auto" {
		return template.CSS(":root{" + pageTones["ice"].vars() + "}@media (prefers-color-scheme:light){:root{" + pageTones["paper"].vars() + "}}")
	}
	t, ok := pageTones[tone]
	if !ok {
		t = pageTones[siteTones[0]]
	}
	return template.CSS(":root{" + t.vars() + "}")
}

// lookColor is the colour browsers may paint around a page in a look (its theme-color).
func lookColor(tone string) string {
	if t, ok := pageTones[tone]; ok {
		return t.page
	}
	if tone == "auto" {
		return pageTones["ice"].page
	}
	return pageTones[siteTones[0]].page
}
