package panel

import (
	"fmt"
	"strings"
)

// The built-in marks, drawn as pieces that fly in from outside and settle into place (web/src/mark.ts
// draws the same; TestBuiltinMarksMatchWeb keeps them alike):
//   - the rose, Rosélune's own: five rose petals, a blush bloom inside them, the moon at its heart
//   - the umbrella: a canopy seen from above, eight panels alternating red and white
//   - confetti, in the Postmodernism look's colours: a canary sun with a black rim, a cobalt crescent
//     moon cradling it, a mint zigzag, a pink triangle and a cobalt dot
//   - the pixel rose: the rose on a grid of 16 by 16, in square pixels
//   - the drawbridge: DrawBridge's own mark (web/src/bridgemark.tsx) with its leaves down
// The pages that are not the app - the loading screens before it starts, subscription pages - get
// them from here.

type markPiece struct {
	d, fill   string
	white     bool    // a light piece on the edge: a faint outline keeps it on light pages
	dx, dy, i float64 // where it comes from, and its place in the order (the animation's delay)
}

// logoMarks are the built-in marks, the default first.
var logoMarks = []string{"rose", "umbrella", "confetti", "pixel", "bridge"}

const (
	roseFill  = "#c13a66"
	roseBlush = "#fbe1e9"
	roseHeart = "#9e2a50"
	pxLight   = "#e76a93"
	umbRed    = "#d8232a"
	umbWhite  = "#f5f3ef"
	umbD      = 0.7071
	cfEdge    = "#17151a"
	cfSun     = "#ffd23f"
	cfCobalt  = "#2346d8"
	cfPink    = "#ff9dc2"
	cfMint    = "#6fdcb9"
	brSteel   = "#8193ad"
	brSoft    = "#8193ad8c"
	brFaint   = "#8193ad59"
	brGlow    = "#8fb8ff"
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
	"confetti": {
		{"M9 8.4A6.6 6.6 0 1 1 22.2 8.4A6.6 6.6 0 1 1 9 8.4Z", cfEdge, false, umbD, -umbD, 0},
		{"M10 8.4A5.6 5.6 0 1 1 21.2 8.4A5.6 5.6 0 1 1 10 8.4Z", cfSun, false, umbD, -umbD, 0.6},
		{"M1.8 4.55L3.5 1.85L5.2 4.55L6.9 1.85L8.6 4.55L8.6 6.25L6.9 3.55L5.2 6.25L3.5 3.55L1.8 6.25Z", cfMint, true, -umbD, -umbD, 1.4},
		{"M14.4 22.3L22.3 22.3L22.3 14.4Z", cfPink, true, umbD, umbD, 2.2},
		{"M7.01 6.87A7.9 7.9 0 1 0 16.82 17.12A7.1 7.1 0 0 1 7.01 6.87Z", cfCobalt, false, -1, 0, 3},
		{"M9.05 2.1A1.25 1.25 0 1 1 11.55 2.1A1.25 1.25 0 1 1 9.05 2.1Z", cfCobalt, false, 0, -1, 3.8},
	},
	"pixel": {
		{"M9 0h6v1.5h-6zM3 1.5h6v1.5h-6zM15 1.5h6v1.5h-6z", roseHeart, false, 0, -1, 0},
		{"M9 1.5h6v1.5h-6zM3 3h7.5v1.5h-7.5zM13.5 3h7.5v1.5h-7.5zM6 4.5h12v1.5h-12zM6 6h3v1.5h-3zM15 6h3v1.5h-3z", roseFill, false, 0, -1, 0},
		{"M10.5 3h3v1.5h-3zM4.5 4.5h1.5v1.5h-1.5zM18 4.5h1.5v1.5h-1.5z", pxLight, false, 0, -1, 0},
		{"M21 3h1.5v1.5h-1.5zM21 4.5h1.5v1.5h-1.5zM21 6h1.5v1.5h-1.5zM19.5 7.5h1.5v1.5h-1.5zM21 9h1.5v1.5h-1.5zM22.5 10.5h1.5v1.5h-1.5zM22.5 12h1.5v1.5h-1.5zM21 13.5h1.5v1.5h-1.5zM19.5 15h1.5v1.5h-1.5zM21 16.5h1.5v1.5h-1.5zM21 18h1.5v1.5h-1.5zM21 19.5h1.5v1.5h-1.5z", roseHeart, false, 1, 0, 1},
		{"M19.5 4.5h1.5v1.5h-1.5zM18 6h3v1.5h-3zM16.5 7.5h3v1.5h-3zM18 9h3v1.5h-3zM18 10.5h4.5v1.5h-4.5zM18 12h1.5v1.5h-1.5zM21 12h1.5v1.5h-1.5zM18 13.5h3v1.5h-3zM16.5 15h3v1.5h-3zM18 16.5h3v1.5h-3zM19.5 18h1.5v1.5h-1.5z", roseFill, false, 1, 0, 1},
		{"M19.5 12h1.5v1.5h-1.5z", pxLight, false, 1, 0, 1},
		{"M6 16.5h3v1.5h-3zM15 16.5h3v1.5h-3zM6 18h12v1.5h-12zM3 19.5h6v1.5h-6zM10.5 19.5h3v1.5h-3zM15 19.5h6v1.5h-6zM9 21h1.5v1.5h-1.5zM13.5 21h1.5v1.5h-1.5z", roseFill, false, 0, 1, 2},
		{"M4.5 18h1.5v1.5h-1.5zM18 18h1.5v1.5h-1.5zM10.5 21h3v1.5h-3z", pxLight, false, 0, 1, 2},
		{"M9 19.5h1.5v1.5h-1.5zM13.5 19.5h1.5v1.5h-1.5zM3 21h6v1.5h-6zM15 21h6v1.5h-6zM9 22.5h6v1.5h-6z", roseHeart, false, 0, 1, 2},
		{"M1.5 3h1.5v1.5h-1.5zM1.5 4.5h1.5v1.5h-1.5zM1.5 6h1.5v1.5h-1.5zM3 7.5h1.5v1.5h-1.5zM1.5 9h1.5v1.5h-1.5zM0 10.5h1.5v1.5h-1.5zM0 12h1.5v1.5h-1.5zM1.5 13.5h1.5v1.5h-1.5zM3 15h1.5v1.5h-1.5zM1.5 16.5h1.5v1.5h-1.5zM1.5 18h1.5v1.5h-1.5zM1.5 19.5h1.5v1.5h-1.5z", roseHeart, false, -1, 0, 3},
		{"M3 4.5h1.5v1.5h-1.5zM3 6h3v1.5h-3zM4.5 7.5h3v1.5h-3zM3 9h3v1.5h-3zM1.5 10.5h4.5v1.5h-4.5zM1.5 12h1.5v1.5h-1.5zM4.5 12h1.5v1.5h-1.5zM3 13.5h3v1.5h-3zM4.5 15h3v1.5h-3zM3 16.5h3v1.5h-3zM3 18h1.5v1.5h-1.5z", roseFill, false, -1, 0, 3},
		{"M3 12h1.5v1.5h-1.5z", pxLight, false, -1, 0, 3},
		{"M9 6h6v1.5h-6zM7.5 7.5h3v1.5h-3zM13.5 7.5h3v1.5h-3zM6 9h3v1.5h-3zM12 9h6v1.5h-6zM6 10.5h3v1.5h-3zM12 10.5h6v1.5h-6zM6 12h3v1.5h-3zM12 12h6v1.5h-6zM6 13.5h4.5v1.5h-4.5zM13.5 13.5h4.5v1.5h-4.5zM7.5 15h4.5v1.5h-4.5zM15 15h1.5v1.5h-1.5zM9 16.5h6v1.5h-6z", roseBlush, true, 0, 0, 5},
		{"M10.5 7.5h3v1.5h-3zM9 9h3v1.5h-3zM9 10.5h3v1.5h-3zM9 12h3v1.5h-3zM10.5 13.5h3v1.5h-3zM12 15h3v1.5h-3z", roseHeart, false, 0, 0, 6},
	},
	"bridge": {
		{"M1.8 16.33A10.2 10.2 0 0 1 22.2 16.33L21.92 16.33A9.92 9.92 0 0 0 2.08 16.33Z", brSteel, false, 0, -1, 0},
		{"M3.04 16.33A8.96 8.96 0 0 1 20.96 16.33L20.83 16.33A8.83 8.83 0 0 0 3.17 16.33Z", brFaint, false, 0, -1, 0.4},
		{"M11.92 6.27L11.92 9.31L12.08 9.31L12.08 6.27Z", brSoft, false, 0, -1, 0.8},
		{"M3.93 16.33L4.1 10.95L4.63 10.13L5.15 10.95L5.33 16.33ZM4.51 14.34L6.38 14.34L6.38 15.04L4.51 15.04ZM5.89 14.69A0.49 0.49 0 1 1 6.87 14.69A0.49 0.49 0 1 1 5.89 14.69ZM6.08 14.69A0.3 0.3 0 1 0 6.69 14.69A0.3 0.3 0 1 0 6.08 14.69Z", brSteel, false, -1, 0, 1.2},
		{"M18.67 16.33L18.85 10.95L19.37 10.13L19.9 10.95L20.07 16.33ZM17.62 14.34L19.49 14.34L19.49 15.04L17.62 15.04ZM17.13 14.69A0.49 0.49 0 1 1 18.11 14.69A0.49 0.49 0 1 1 17.13 14.69ZM17.31 14.69A0.3 0.3 0 1 0 17.92 14.69A0.3 0.3 0 1 0 17.31 14.69Z", brSteel, false, 1, 0, 1.5},
		{"M4.6 10.52L8.71 13.77L8.77 13.69L4.66 10.44ZM4.6 10.52L10.4 13.82L10.45 13.74L4.65 10.44ZM4.61 10.52L11.87 13.86L11.91 13.78L4.65 10.44Z", brSoft, false, -0.7071, -0.7071, 2.1},
		{"M19.34 10.44L15.23 13.69L15.29 13.77L19.4 10.52ZM19.35 10.44L13.55 13.74L13.6 13.82L19.4 10.52ZM19.35 10.44L12.09 13.78L12.13 13.86L19.39 10.52Z", brSoft, false, 0.7071, -0.7071, 2.4},
		{"M6.03 14.15L12 14.41L12 14.97L6.03 15.23Z", brSteel, false, -0.7071, 0.7071, 2.9},
		{"M17.97 14.15L12 14.41L12 14.97L17.97 15.23Z", brSteel, false, 0.7071, 0.7071, 3.2},
		{"M6.61 13.58L11.76 13.81L11.77 13.7L6.62 13.46ZM7.14 14.22L7.14 13.59L7.03 13.59L7.03 14.22ZM8.19 14.26L8.19 13.63L8.09 13.63L8.09 14.26ZM9.24 14.29L9.24 13.66L9.14 13.66L9.14 14.29ZM10.3 14.33L10.3 13.7L10.19 13.7L10.19 14.33ZM11.35 14.36L11.35 13.73L11.25 13.73L11.25 14.36Z", brSoft, false, -0.7071, 0.7071, 3.5},
		{"M17.38 13.46L12.23 13.7L12.24 13.81L17.39 13.58ZM16.97 14.22L16.97 13.59L16.86 13.59L16.86 14.22ZM15.91 14.26L15.91 13.63L15.81 13.63L15.81 14.26ZM14.86 14.29L14.86 13.66L14.76 13.66L14.76 14.29ZM13.81 14.33L13.81 13.7L13.7 13.7L13.7 14.33ZM12.75 14.36L12.75 13.73L12.65 13.73L12.65 14.36Z", brSoft, false, 0.7071, 0.7071, 3.8},
		{"M10.95 14.69A1.05 0.42 0 1 1 13.05 14.69A1.05 0.42 0 1 1 10.95 14.69Z", brGlow, false, 0, 0, 4.4},
		{"M1 16.41L23 16.41L23 16.25L1 16.25Z", brSoft, false, 0, 1, 4.8},
		{"M4.28 17.08L4.98 17.08L4.98 16.98L4.28 16.98ZM5.56 17.08L6.27 17.08L6.27 16.98L5.56 16.98ZM6.85 17.08L7.55 17.08L7.55 16.98L6.85 16.98ZM8.14 17.08L8.84 17.08L8.84 16.98L8.14 16.98ZM9.43 17.08L10.13 17.08L10.13 16.98L9.43 16.98ZM13.64 17.08L14.34 17.08L14.34 16.98L13.64 16.98ZM14.93 17.08L15.63 17.08L15.63 16.98L14.93 16.98ZM16.21 17.08L16.91 17.08L16.91 16.98L16.21 16.98ZM17.5 17.08L18.2 17.08L18.2 16.98L17.5 16.98ZM18.79 17.08L19.49 17.08L19.49 16.98L18.79 16.98ZM8.02 17.79L8.72 17.79L8.72 17.68L8.02 17.68ZM9.31 17.79L10.01 17.79L10.01 17.68L9.31 17.68ZM10.6 17.79L11.3 17.79L11.3 17.68L10.6 17.68ZM11.88 17.79L12.59 17.79L12.59 17.68L11.88 17.68ZM13.17 17.79L13.87 17.79L13.87 17.68L13.17 17.68ZM14.46 17.79L15.16 17.79L15.16 17.68L14.46 17.68ZM15.74 17.79L15.98 17.79L15.98 17.68L15.74 17.68Z", brFaint, false, 0, 1, 5.2},
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
