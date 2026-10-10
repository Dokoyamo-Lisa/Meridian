// Command build makes Meridian's guide site: the overview (site/front.md, the front page) and a page
// per document in docs/, rendered from the repository's own Markdown so the guide never drifts from
// the code. Everything it writes is static - HTML, one style sheet, one script, the screenshots - with
// relative links, so the site works from any folder.
//
//	cd site && go run ./build            # writes dist/
//	cd site && go run ./build -serve     # and serves it on 127.0.0.1:8099
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

const repoURL = "https://github.com/Dokoyamo-Lisa/Meridian"

// demoURL is the public demo: a panel anyone may look around in and nobody can change.
const demoURL = "https://deep.losantos.space"

// page is one document of the guide.
type page struct {
	Slug, Source, Title, Group, Blurb string
}

var pages = []page{
	{"index", "site/front.md", "Meridian", "Start", "Proxy and VPN servers for your team, run from one quiet panel"},
	{"getting-started", "docs/getting-started.md", "Getting started", "Start", "Install the panel, add a server, protocols and users"},
	{"operations", "docs/operations.md", "Running the panel", "Start", "The database, backups, upgrades, moving hosts"},
	{"routing", "docs/routing.md", "Traffic splitting", "Features", "Sites, countries and apps through the exits you choose"},
	{"health", "docs/health.md", "Health checks", "Features", "Signs of a break-in or abuse, flagged for you to decide"},
	{"telegram", "docs/telegram.md", "Telegram", "Features", "Notifications, commands, users' links and the Mini App"},
	{"ipv6-and-dynamic-dns", "docs/ipv6-and-dynamic-dns.md", "IPv6 and dynamic DNS", "Features", "Servers whose address changes, and IPv6-only servers"},
	{"plugins", "docs/plugins.md", "Plugins", "Extending", "Looks, pages, API routes and filters of your own"},
	{"api", "docs/api.md", "API", "Extending", "Tokens, scopes and every endpoint"},
	{"mcp", "docs/mcp.md", "AI assistants", "Extending", "The MCP server: what assistants may do, and ask first"},
	{"architecture", "docs/architecture.md", "How it works", "Extending", "Panel, agents, cores and the data in between"},
	{"changelog", "CHANGELOG.md", "Changelog", "Project", "Every release, in plain words"},
}

// Nav is how the sidebar and the pager name a page.
func (p page) Nav() string {
	if p.Slug == "index" {
		return "Overview"
	}
	return p.Title
}

// pageURL is where a page (a page or a *page) is, seen from a page in base (the guide's folder, from
// there) whose site root is root: the overview is the site's front page, the others are in guide/.
func pageURL(base, root string, v any) string {
	var p page
	switch x := v.(type) {
	case page:
		p = x
	case *page:
		p = *x
	}
	if p.Slug == "index" {
		return root + "index.html"
	}
	return base + p.Slug + ".html"
}

type tocItem struct {
	ID, Text string
	Level    int
}

type rendered struct {
	page
	Body       template.HTML
	TOC        []tocItem
	Prev, Next *page
}

type searchEntry struct {
	Page    string `json:"p"`
	Title   string `json:"t"`
	Heading string `json:"h"`
	ID      string `json:"id"`
	Text    string `json:"x"`
}

func main() {
	serve := flag.Bool("serve", false, "serve the site after building it")
	flag.Parse()
	root, err := filepath.Abs("..") // the repository
	check(err)
	out := "dist"
	check(os.RemoveAll(out))
	check(os.MkdirAll(filepath.Join(out, "guide"), 0o755))

	tpl := template.Must(template.New("").Funcs(template.FuncMap{
		"groups":  groups,
		"icon":    icon,
		"pageURL": pageURL,
		"asset":   func(root, name string) string { return root + "assets/" + name + version("assets/"+name) },
	}).ParseGlob("templates/*.html"))
	version := "1.0.0"
	if b, err := os.ReadFile(filepath.Join(root, "VERSION")); err == nil {
		version = strings.TrimSpace(string(b))
	}

	md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	var index []searchEntry
	var all []rendered
	for i, p := range pages {
		src, err := os.ReadFile(filepath.Join(root, p.Source))
		check(err)
		base, root := "", "../"
		if p.Slug == "index" {
			base, root = "guide/", ""
		}
		r, entries := render(md, src, p, base, root)
		if i > 0 {
			r.Prev = &pages[i-1]
		}
		if i < len(pages)-1 {
			r.Next = &pages[i+1]
		}
		all = append(all, r)
		index = append(index, entries...)
	}
	for _, r := range all {
		data := map[string]any{"Root": "../", "Base": "", "Nav": "guide", "Page": r, "Pages": pages, "Repo": repoURL, "Demo": demoURL,
			"Title": r.Title + " · Meridian", "Description": r.Blurb, "Version": version}
		file := filepath.Join(out, "guide", r.Slug+".html")
		if r.Slug == "index" {
			data["Root"], data["Base"], data["Nav"], file = "", "guide/", "overview", filepath.Join(out, "index.html")
			data["Title"] = "Meridian - proxy and VPN servers for your team, from one quiet panel"
			data["Description"] = "Meridian sets up VLESS, VMess, Trojan, Shadowsocks, Hysteria2 and WireGuard on your own Linux servers, gives every user a link and a page of their own, and shows every server on a live globe."
		}
		var b bytes.Buffer
		check(tpl.ExecuteTemplate(&b, "doc.html", data))
		check(os.WriteFile(file, b.Bytes(), 0o644))
	}
	js, _ := json.Marshal(index)
	check(os.WriteFile(filepath.Join(out, "search.json"), js, 0o644))
	check(copyDir("assets", filepath.Join(out, "assets")))
	if _, err := os.Stat("img"); err == nil {
		check(copyDir("img", filepath.Join(out, "img")))
	}
	check(os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o644)) // GitHub Pages: serve files as they are
	log.Printf("wrote %s: the overview and %d guide pages", out, len(all)-1)
	if *serve {
		log.Printf("serving on http://127.0.0.1:8099")
		log.Fatal(http.ListenAndServe("127.0.0.1:8099", http.FileServer(http.Dir(out))))
	}
}

// render turns one Markdown document into its page: links to other documents point at their pages,
// links into the repository at GitHub, the first heading becomes the title.
func render(md goldmark.Markdown, src []byte, p page, base, root string) (rendered, []searchEntry) {
	doc := md.Parser().Parse(text.NewReader(src))
	r := rendered{page: p}
	var entries []searchEntry
	var cur *searchEntry
	var drop []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch x := n.(type) {
		case *ast.Heading:
			id, _ := x.AttributeString("id")
			idStr := fmt.Sprint(string(id.([]byte)))
			label := plain(x, src)
			if x.Level == 1 {
				drop = append(drop, x) // the page's header shows it
				return ast.WalkSkipChildren, nil
			}
			if x.Level <= 3 {
				r.TOC = append(r.TOC, tocItem{ID: idStr, Text: label, Level: x.Level})
			}
			entries = append(entries, searchEntry{Page: p.Slug, Title: p.Title, Heading: label, ID: idStr})
			cur = &entries[len(entries)-1]
		case *ast.Paragraph, *ast.ListItem:
			if cur != nil && len(cur.Text) < 400 {
				cur.Text = strings.TrimSpace(cur.Text + " " + plain(x, src))
				if len(cur.Text) > 400 {
					cur.Text = cur.Text[:400]
				}
			}
		case *ast.Link:
			x.Destination = []byte(rewriteLink(string(x.Destination), p.Source, base, root))
		case *ast.Image:
			x.Destination = []byte(rewriteLink(string(x.Destination), p.Source, base, root))
		}
		return ast.WalkContinue, nil
	})
	for _, n := range drop {
		n.Parent().RemoveChild(n.Parent(), n)
	}
	var b bytes.Buffer
	check(md.Renderer().Render(&b, src, doc))
	html := b.String()
	// tables scroll on their own on phones; code blocks get a copy button (site.js)
	html = strings.ReplaceAll(html, "<table>", `<div class="tbl"><table>`)
	html = strings.ReplaceAll(html, "</table>", "</table></div>")
	r.Body = template.HTML(html)
	return r, entries
}

// plain is a node's text.
func plain(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			switch t := c.(type) {
			case *ast.Text:
				b.Write(t.Segment.Value(src))
				if t.SoftLineBreak() {
					b.WriteByte(' ')
				}
			case *ast.String:
				b.Write(t.Value)
			case *ast.CodeSpan:
				for k := t.FirstChild(); k != nil; k = k.NextSibling() {
					if s, ok := k.(*ast.Text); ok {
						b.Write(s.Segment.Value(src))
					}
				}
				return ast.WalkSkipChildren, nil
			}
		}
		return ast.WalkContinue, nil
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

var external = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)

// rewriteLink points a link of document from (a path in the repository) at its page in the guide, at
// the site's own files (site/...), or at GitHub for anything else in the repository. base and root are
// where the guide's folder and the site's root are, seen from the page.
func rewriteLink(dest, from, base, root string) string {
	if dest == "" || strings.HasPrefix(dest, "#") || external.MatchString(dest) {
		return dest
	}
	path, frag, _ := strings.Cut(dest, "#")
	target := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(from), path)))
	for _, p := range pages {
		if p.Source == target {
			if frag != "" {
				return pageURL(base, root, p) + "#" + frag
			}
			return pageURL(base, root, p)
		}
	}
	if target == "README.md" {
		return root + "index.html"
	}
	if rest, ok := strings.CutPrefix(target, "site/"); ok { // the site's own files: the screenshots
		return root + rest + version(rest)
	}
	kind := "blob"
	if st, err := os.Stat(filepath.Join("..", target)); err == nil && st.IsDir() {
		kind = "tree"
	}
	u := repoURL + "/" + kind + "/main/" + target
	if frag != "" {
		u += "#" + frag
	}
	return u
}

// version is a query string that changes with a file's content, so caches in front of the site (a
// CDN) never keep an old style sheet, script or screenshot: "?v=" and the first bytes of its SHA-256.
func version(file string) string {
	b, err := os.ReadFile(file)
	check(err)
	sum := sha256.Sum256(b)
	return "?v=" + hex.EncodeToString(sum[:5])
}

// groups orders the guide's pages under their headings for the sidebar.
func groups(list []page) [][]page {
	var out [][]page
	for _, p := range list {
		if len(out) == 0 || out[len(out)-1][0].Group != p.Group {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], p)
	}
	return out
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		f, err := os.Create(to)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(f, in)
		return err
	})
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// icons are the panel's own (web/src/icons.tsx), drawn with the current colour.
var icons = map[string]string{
	"server":   "M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01",
	"link":     "M10 14a4 4 0 0 0 5.66 0l3-3a4 4 0 0 0-5.66-5.66l-1 1M14 10a4 4 0 0 0-5.66 0l-3 3a4 4 0 0 0 5.66 5.66l1-1",
	"activity": "M3 12h4l3-8 4 16 3-8h4",
	"users":    "M16 20v-1.5A3.5 3.5 0 0 0 12.5 15h-5A3.5 3.5 0 0 0 4 18.5V20M10 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7z",
	"copy":     "M9 9h11v11H9zM5 15H4V4h11v1",
	"check":    "M5 12.5l4.5 4.5L19 7.5",
	"pause":    "M9 5v14M15 5v14",
	"more":     "M4 7h16M4 12h16M4 17h16",
	"search":   "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
	"shield":   "M12 3l7.5 3v5.5c0 4.6-3.2 8.3-7.5 9.5-4.3-1.2-7.5-4.9-7.5-9.5V6z",
	"globe":    "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.5 3.8 5.5 3.8 9s-1.3 6.5-3.8 9c-2.5-2.5-3.8-5.5-3.8-9S9.5 5.5 12 3z",
	"external": "M14 4h6v6M20 4l-9 9M18 14v6H4V6h6",
	"zap":      "M13 3L5 13.5h6L10.5 21 19 10.5h-6z",
	"route":    "M6 19a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM18 9a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM6 15V9.5A2.5 2.5 0 0 1 8.5 7H16M18 9v5.5a2.5 2.5 0 0 1-2.5 2.5H8",
	"forward":  "M4 12h14M13 6l6 6-6 6",
	"back":     "M19 12H5M11 6l-6 6 6 6",
	"palette":  "M12 21a9 9 0 1 1 9-9c0 2.5-2 3.5-3.5 3.5H15a2 2 0 0 0-1.5 3.3c.4.5.5 1.2.1 1.7-.4.4-1 .5-1.6.5zM7.5 11.5h.01M10 7.5h.01M14.5 7.5h.01",
	"book":     "M4 5.5A2.5 2.5 0 0 1 6.5 3H20v15H6.5A2.5 2.5 0 0 0 4 20.5zM4 20.5A2.5 2.5 0 0 0 6.5 23H20v-5",
	"close":    "M6 6l12 12M18 6L6 18",
}

func icon(name string) template.HTML {
	d, ok := icons[name]
	if !ok {
		panic("no icon " + name)
	}
	return template.HTML(`<svg class="ic" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="` + d + `"/></svg>`)
}
