package panel

import (
	"os"
	"strings"
	"testing"
)

// TestBuiltinMarksMatchWeb: the built-in marks the panel draws (loading screens, subscription pages)
// are the ones the app draws (web/src/mark.ts), and the pages and tab icons are built from them.
func TestBuiltinMarksMatchWeb(t *testing.T) {
	read := func(path string) string {
		b, err := os.ReadFile("../../" + path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	ts := read("web/src/mark.ts")
	for name, pieces := range builtinMarks {
		for _, p := range pieces {
			if !strings.Contains(ts, "'"+p.d+"'") {
				t.Errorf("%s: mark.ts has no piece %s", name, p.d)
			}
		}
	}
	for _, c := range []string{roseFill, roseBlush, roseHeart, umbRed, umbWhite} {
		if !strings.Contains(ts, "'"+c+"'") {
			t.Errorf("mark.ts has no colour %s", c)
		}
	}
	// the loading screens show the rose assembling, as brandedBoot leaves it
	boot := markSVG("rose", "umb-mark umb-loop")
	for _, page := range []string{"web/index.html", "web/status/index.html"} {
		if !strings.Contains(read(page), "<!--logo-->"+boot+"<!--/logo-->") {
			t.Errorf("%s does not show the rose as markSVG draws it:\n%s", page, boot)
		}
	}
	for file, mark := range map[string]string{"web/public/favicon.svg": "rose", "web/public/favicon-umbrella.svg": "umbrella"} {
		icon := read(file)
		for _, p := range builtinMarks[mark] {
			if !strings.Contains(icon, p.d) {
				t.Errorf("%s has no piece %s", file, p.d)
			}
		}
	}
}
