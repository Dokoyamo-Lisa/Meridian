package panel

// The built UI's files, served from memory: each with an ETag (so browsers revalidate cheaply) and,
// for text, gzip-compressed for browsers that accept it - the panel's script, the status page and
// its map data shrink to a third.

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

type staticFile struct {
	body, gz []byte
	etag     string
	ctype    string
}

type staticCache struct {
	mu sync.Mutex
	m  map[string]*staticFile
}

func (c *staticCache) get(fsys fs.FS, name string) (*staticFile, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.m[name]; f != nil {
		return f, nil
	}
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	if c.m == nil {
		c.m = map[string]*staticFile{}
	}
	f := newStaticFile(name, body)
	c.m[name] = f
	return f, nil
}

var compressible = map[string]bool{".js": true, ".css": true, ".json": true, ".svg": true, ".html": true, ".txt": true, ".map": true}

func newStaticFile(name string, body []byte) *staticFile {
	sum := sha256.Sum256(body)
	ext := strings.ToLower(path.Ext(name))
	f := &staticFile{body: body, etag: `"` + hex.EncodeToString(sum[:10]) + `"`, ctype: mime.TypeByExtension(ext)}
	if f.ctype == "" {
		f.ctype = "application/octet-stream"
	}
	if compressible[ext] && len(body) > 1024 {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = zw.Write(body)
		_ = zw.Close()
		if b.Len() < len(body)*9/10 {
			f.gz = b.Bytes()
		}
	}
	return f
}

// serve answers with the file, or 304 when the browser already has it.
func (f *staticFile) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", f.ctype)
	h.Set("ETag", f.etag)
	h.Set("Vary", "Accept-Encoding")
	if etagMatches(r.Header.Get("If-None-Match"), f.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := f.body
	if f.gz != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = f.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			q := strings.ReplaceAll(params, " ", "")
			return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
		}
	}
	return false
}

func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == etag || t == "*" {
			return true
		}
	}
	return false
}
