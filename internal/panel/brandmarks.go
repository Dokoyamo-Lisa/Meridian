package panel

import (
	"fmt"
	"strings"
)

// The built-in marks, drawn as pieces that fly in from outside and settle into place (web/src/mark.ts
// draws the same; TestBuiltinMarksMatchWeb keeps them alike):
//   - the rose, Rosélune's own: five rose petals, a blush bloom inside them, the moon at its heart
//   - the umbrella: a canopy seen from above, eight panels alternating red and white
// The pages that are not the app - the loading screens before it starts, subscription pages - get
// them from here.

type markPiece struct {
	d, fill   string
	white     bool    // a light piece on the edge: a faint outline keeps it on light pages
	dx, dy, i float64 // where it comes from, and its place in the order (the animation's delay)
}

// logoMarks are the built-in marks, the default first.
var logoMarks = []string{"rose", "umbrella"}

const (
	roseFill  = "#c13a66"
	roseBlush = "#fbe1e9"
	roseHeart = "#9e2a50"
	umbRed    = "#d8232a"
	umbWhite  = "#f5f3ef"
	umbD      = 0.7071
)

var builtinMarks = map[string][]markPiece{
	"rose": {
		{"M12 12C9.41 10.24 6.92 6.32 8.62 3.38C9.74 2.15 11.44 2.18 12 2.9C12.56 2.18 14.26 2.15 15.38 3.38C17.08 6.32 14.59 10.24 12 12Z", roseFill, false, 0, -1, 0},
		{"M12 12C12.88 9 15.84 5.42 19.16 6.12C20.67 6.81 21.17 8.43 20.65 9.19C21.51 9.5 22.07 11.1 21.25 12.55C18.97 15.07 14.48 13.91 12 12Z", roseFill, false, 0.9511, -0.309, 1},
		{"M12 12C15.13 11.91 19.45 13.61 19.81 16.99C19.61 18.64 18.23 19.61 17.35 19.36C17.32 20.28 15.96 21.29 14.33 20.97C11.23 19.58 10.95 14.95 12 12Z", roseFill, false, 0.5878, 0.809, 2},
		{"M12 12C13.05 14.95 12.77 19.58 9.67 20.97C8.04 21.29 6.68 20.28 6.65 19.36C5.77 19.61 4.39 18.64 4.19 16.99C4.55 13.61 8.87 11.91 12 12Z", roseFill, false, -0.5878, 0.809, 3},
		{"M12 12C9.52 13.91 5.03 15.07 2.75 12.55C1.93 11.1 2.49 9.5 3.35 9.19C2.83 8.43 3.33 6.81 4.84 6.12C8.16 5.42 11.12 9 12 12Z", roseFill, false, -0.9511, -0.309, 4},
		{"M12 12C11.32 10.13 11.49 7.19 13.46 6.32C14.51 6.1 15.36 6.76 15.38 7.35C15.95 7.18 16.84 7.79 16.95 8.86C16.73 11 13.99 12.07 12 12Z", roseBlush, false, 0.5878, -0.809, 2.5},
		{"M12 12C13.57 10.78 16.42 10.03 17.86 11.63C18.39 12.56 18.03 13.58 17.47 13.78C17.8 14.26 17.5 15.3 16.52 15.74C14.42 16.19 12.55 13.91 12 12Z", roseBlush, false, 0.9511, 0.309, 3.5},
		{"M12 12C13.65 13.12 15.24 15.6 14.16 17.46C13.44 18.25 12.36 18.22 12 17.75C11.64 18.22 10.56 18.25 9.84 17.46C8.76 15.6 10.35 13.12 12 12Z", roseBlush, false, 0, 1, 4.5},
		{"M12 12C11.45 13.91 9.58 16.19 7.48 15.74C6.5 15.3 6.2 14.26 6.53 13.78C5.97 13.58 5.61 12.56 6.14 11.63C7.58 10.03 10.43 10.78 12 12Z", roseBlush, false, -0.9511, 0.309, 5.5},
		{"M12 12C10.01 12.07 7.27 11 7.05 8.86C7.16 7.79 8.05 7.18 8.62 7.35C8.64 6.76 9.49 6.1 10.54 6.32C12.51 7.19 12.68 10.13 12 12Z", roseBlush, false, -0.5878, -0.809, 6.5},
		{"M12.2 9.71A2.3 2.3 0 1 0 14.09 12.97A1.89 1.89 0 1 1 12.2 9.71Z", roseHeart, false, 0, 0, 7.5},
	},
	"umbrella": {
		{"M12 12L16.02 2.3L7.98 2.3Z", umbRed, false, 0, -1, 0},
		{"M12 12L21.7 7.98L16.02 2.3Z", umbWhite, true, umbD, -umbD, 1},
		{"M12 12L21.7 16.02L21.7 7.98Z", umbRed, false, 1, 0, 2},
		{"M12 12L16.02 21.7L21.7 16.02Z", umbWhite, true, umbD, umbD, 3},
		{"M12 12L7.98 21.7L16.02 21.7Z", umbRed, false, 0, 1, 4},
		{"M12 12L2.3 16.02L7.98 21.7Z", umbWhite, true, -umbD, umbD, 5},
		{"M12 12L2.3 7.98L2.3 16.02Z", umbRed, false, -1, 0, 6},
		{"M12 12L7.98 2.3L2.3 7.98Z", umbWhite, true, -umbD, -umbD, 7},
	},
}

func markOf(mark string) []markPiece {
	if p, ok := builtinMarks[mark]; ok {
		return p
	}
	return builtinMarks[logoMarks[0]]
}

// markSVG is a built-in mark as the app draws it: each piece with what the assemble animation needs
// (mark.css). cls is the svg's class, e.g. "umb-mark umb-loop" on a loading screen.
func markSVG(mark, cls string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="%s" viewBox="0 0 24 24" aria-hidden="true" focusable="false">`, cls)
	for _, p := range markOf(mark) {
		w := "w"
		if p.white {
			w = "w white"
		}
		fmt.Fprintf(&b, `<path class="%s" d="%s" fill="%s" style="--i:%g;--dx:%g;--dy:%g"/>`, w, p.d, p.fill, p.i, p.dx, p.dy)
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// markStill is a built-in mark as a plain drawing, for a page without the app's styles (subscription
// pages): its pieces by colour, and the umbrella's edge so its white panels hold on light pages.
func markStill(mark string) string {
	var b strings.Builder
	b.WriteString(`<svg class="mark" viewBox="0 0 24 24" aria-hidden="true">`)
	var fills []string
	paths := map[string]string{}
	for _, p := range markOf(mark) {
		if _, ok := paths[p.fill]; !ok {
			fills = append(fills, p.fill)
		}
		paths[p.fill] += p.d
	}
	for _, f := range fills {
		fmt.Fprintf(&b, `<path fill="%s" d="%s"/>`, f, paths[f])
	}
	if mark == "umbrella" {
		b.WriteString(`<path fill="none" stroke="currentColor" stroke-opacity=".35" stroke-width=".7" stroke-linejoin="round" d="M16.02 2.3L7.98 2.3L2.3 7.98L2.3 16.02L7.98 21.7L16.02 21.7L21.7 16.02L21.7 7.98Z"/>`)
	}
	b.WriteString(`</svg>`)
	return b.String()
}
