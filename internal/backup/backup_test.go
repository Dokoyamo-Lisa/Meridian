package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSigV4: the signer gives AWS's own published signatures (S3 documentation, "Signature
// Calculations for the Authorization Header", GET Object and GET Bucket examples).
func TestSigV4(t *testing.T) {
	s := &S3{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	s.sign(req, emptySHA256, at)
	if a := req.Header.Get("Authorization"); !strings.Contains(a, "SignedHeaders=host;range;x-amz-content-sha256;x-amz-date") ||
		!strings.HasSuffix(a, "Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41") {
		t.Errorf("GET Object: %s", a)
	}
	req, _ = http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J", nil)
	s.sign(req, emptySHA256, at)
	if a := req.Header.Get("Authorization"); !strings.HasSuffix(a, "Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7") {
		t.Errorf("GET Bucket: %s", a)
	}
}

func dataDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(d, "meridian.db"), []byte("OLD DATABASE"), 0o600))
	must(os.MkdirAll(filepath.Join(d, "plugins", "theme"), 0o700))
	must(os.WriteFile(filepath.Join(d, "plugins", "theme", "plugin.json"), []byte(`{"id":"theme"}`), 0o600))
	must(os.WriteFile(filepath.Join(d, "plugins", "theme", "run"), []byte("#!/bin/sh"), 0o700))
	return d
}

func snapshot(content string) func(string) error {
	return func(p string) error { return os.WriteFile(p, []byte(content), 0o600) }
}

func okCheck(string) error { return nil }

// TestRoundTrip: a backup (plain and encrypted) is staged and put in place, keeping what it replaced;
// a wrong passphrase opens nothing; a plugin's program stays a program.
func TestRoundTrip(t *testing.T) {
	for _, pass := range []string{"", "correct horse battery staple"} {
		src := dataDir(t)
		var buf bytes.Buffer
		var w io.Writer = &buf
		var enc io.WriteCloser
		if pass != "" {
			var err error
			if enc, err = Encrypt(&buf, pass); err != nil {
				t.Fatal(err)
			}
			w = enc
		}
		if err := Write(w, src, Info{Version: "1.0.0", Schema: 18, Panel: "https://panel.example.com"}, snapshot("SNAPSHOT")); err != nil {
			t.Fatal(err)
		}
		if enc != nil {
			enc.Close()
		}
		file := filepath.Join(t.TempDir(), "backup")
		os.WriteFile(file, buf.Bytes(), 0o600)

		dst := dataDir(t)
		os.WriteFile(filepath.Join(dst, "meridian.db"), []byte("CURRENT"), 0o600)
		os.WriteFile(filepath.Join(dst, "meridian.db-wal"), []byte("WAL"), 0o600)
		if pass != "" {
			if _, err := Stage(dst, file, "wrong passphrase", okCheck); !errors.Is(err, ErrPassphrase) {
				t.Fatalf("wrong passphrase: %v", err)
			}
			if _, err := Stage(dst, file, "", okCheck); err == nil {
				t.Fatal("an encrypted backup staged without its passphrase")
			}
		}
		var checked string
		info, err := Stage(dst, file, pass, func(p string) error { b, _ := os.ReadFile(p); checked = string(b); return nil })
		if err != nil || info.Schema != 18 || info.Panel != "https://panel.example.com" || checked != "SNAPSHOT" {
			t.Fatalf("stage: %+v %v %q", info, err, checked)
		}
		if got, ok := Pending(dst); !ok || got.Version != "1.0.0" {
			t.Fatalf("pending: %+v %v", got, ok)
		}
		if b, _ := os.ReadFile(filepath.Join(dst, "meridian.db")); string(b) != "CURRENT" {
			t.Fatal("staging changed the live database")
		}
		_, ok, err := ApplyPending(dst)
		if err != nil || !ok {
			t.Fatalf("apply: %v %v", ok, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dst, "meridian.db")); string(b) != "SNAPSHOT" {
			t.Errorf("restored database: %q", b)
		}
		if _, err := os.Stat(filepath.Join(dst, "meridian.db-wal")); err == nil {
			t.Error("the old write-ahead log was left next to the restored database")
		}
		if st, err := os.Stat(filepath.Join(dst, "plugins", "theme", "run")); err != nil || st.Mode()&0o100 == 0 {
			t.Errorf("a plugin's program after the restore: %v %v", st, err)
		}
		kept, _ := filepath.Glob(filepath.Join(dst, "before-restore-*", "meridian.db"))
		if len(kept) != 1 {
			t.Fatalf("what was replaced is not kept: %v", kept)
		}
		if b, _ := os.ReadFile(kept[0]); string(b) != "CURRENT" {
			t.Errorf("kept: %q", b)
		}
		if _, ok, _ := ApplyPending(dst); ok {
			t.Error("applied twice")
		}
	}
}

// TestHostileBackups: a ZIP with paths out of the folder, links, files no backup has, or no
// database is refused, and nothing is staged.
func TestHostileBackups(t *testing.T) {
	cases := map[string]func(z *zip.Writer){
		"traversal": func(z *zip.Writer) { w, _ := z.Create("plugins/../../etc/passwd"); w.Write([]byte("x")) },
		"absolute":  func(z *zip.Writer) { w, _ := z.Create("/etc/passwd"); w.Write([]byte("x")) },
		"stranger":  func(z *zip.Writer) { w, _ := z.Create("geo/countries.mmdb"); w.Write([]byte("x")) },
		"link": func(z *zip.Writer) {
			h := &zip.FileHeader{Name: "plugins/x/link"}
			h.SetMode(os.ModeSymlink | 0o777)
			w, _ := z.CreateHeader(h)
			w.Write([]byte("/etc/shadow"))
		},
		"no database": func(z *zip.Writer) {},
	}
	for name, add := range cases {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		w, _ := z.Create("manifest.json")
		w.Write([]byte(`{"format":1,"version":"1.0.0","schema":18}`))
		if name != "no database" {
			w, _ = z.Create("meridian.db")
			w.Write([]byte("DB"))
		}
		add(z)
		z.Close()
		file := filepath.Join(t.TempDir(), "b.zip")
		os.WriteFile(file, buf.Bytes(), 0o600)
		dst := dataDir(t)
		if _, err := Stage(dst, file, "", okCheck); err == nil {
			t.Errorf("%s: staged", name)
		}
		if _, ok := Pending(dst); ok {
			t.Errorf("%s: something is pending", name)
		}
		if _, err := os.Stat(filepath.Join(dst, pendingDir)); err == nil {
			t.Errorf("%s: the staging folder stayed", name)
		}
	}
	// a database the check refuses
	src := dataDir(t)
	var buf bytes.Buffer
	Write(&buf, src, Info{}, snapshot("BROKEN"))
	file := filepath.Join(t.TempDir(), "b.zip")
	os.WriteFile(file, buf.Bytes(), 0o600)
	if _, err := Stage(dataDir(t), file, "", func(string) error { return errors.New("the database is damaged") }); err == nil ||
		!strings.Contains(err.Error(), "damaged") {
		t.Errorf("a damaged database: %v", err)
	}
}

// TestApplyRollsBack: when the staged database is gone, nothing changes.
func TestApplyRollsBack(t *testing.T) {
	src := dataDir(t)
	var buf bytes.Buffer
	Write(&buf, src, Info{}, snapshot("SNAPSHOT"))
	file := filepath.Join(t.TempDir(), "b.zip")
	os.WriteFile(file, buf.Bytes(), 0o600)
	dst := dataDir(t)
	if _, err := Stage(dst, file, "", okCheck); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(dst, pendingDir, "meridian.db"))
	if _, ok, err := ApplyPending(dst); ok || err == nil {
		t.Fatalf("applied without a database: %v %v", ok, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "meridian.db")); string(b) != "OLD DATABASE" {
		t.Errorf("the live database after a failed restore: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "plugins", "theme", "plugin.json")); err != nil {
		t.Errorf("the plugins after a failed restore: %v", err)
	}
}

// fakeRemote serves WebDAV and S3 well enough to test the clients.
type fakeRemote struct {
	mu    sync.Mutex
	files map[string][]byte
	auth  []string
}

func (f *fakeRemote) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	switch r.Method {
	case "PUT":
		b, _ := io.ReadAll(r.Body)
		f.files[name] = b
		w.WriteHeader(201)
	case "GET":
		if r.URL.Query().Get("list-type") == "2" {
			io.WriteString(w, `<ListBucketResult>`)
			for n, b := range f.files {
				io.WriteString(w, `<Contents><Key>meridian/`+n+`</Key><Size>`+strconv.Itoa(len(b))+`</Size><LastModified>2026-10-09T10:00:00.000Z</LastModified></Contents>`)
			}
			io.WriteString(w, `</ListBucketResult>`)
			return
		}
		if b, ok := f.files[name]; ok {
			w.Write(b)
			return
		}
		w.WriteHeader(404)
	case "PROPFIND":
		w.WriteHeader(207)
		io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/backups/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response>`)
		for n, b := range f.files {
			io.WriteString(w, `<d:response><d:href>/dav/backups/`+n+`</d:href><d:propstat><d:prop><d:getcontentlength>`+strconv.Itoa(len(b))+`</d:getcontentlength><d:getlastmodified>Fri, 09 Oct 2026 10:00:00 GMT</d:getlastmodified><d:resourcetype/></d:prop></d:propstat></d:response>`)
		}
		io.WriteString(w, `</d:multistatus>`)
	case "DELETE":
		delete(f.files, name)
		w.WriteHeader(204)
	}
}

// TestRemotes: both clients put, list (only the panel's backups, newest first), get and delete,
// with credentials on every request.
func TestRemotes(t *testing.T) {
	f := &fakeRemote{files: map[string][]byte{"notes.txt": []byte("not ours")}}
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	old := Client
	Client = srv.Client()
	Client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { Client = old }()
	ctx := context.Background()
	remotes := map[string]Remote{
		"webdav": &WebDAV{URL: srv.URL + "/dav/backups", User: "u", Password: "p"},
		"s3":     &S3{Endpoint: srv.URL, Region: "auto", Bucket: "b", Prefix: "meridian/", AccessKey: "AK", SecretKey: "SK", PathStyle: true},
	}
	for kind, r := range remotes {
		f.mu.Lock()
		f.files = map[string][]byte{"notes.txt": []byte("not ours")}
		f.auth = nil
		f.mu.Unlock()
		name := "meridian-panel-20261009-100000.zip.age"
		if err := r.Put(ctx, name, strings.NewReader("BACKUP"), 6); err != nil {
			t.Fatalf("%s put: %v", kind, err)
		}
		list, err := r.List(ctx)
		if err != nil || len(list) != 1 || list[0].Name != name || list[0].Size != 6 {
			t.Fatalf("%s list: %+v %v", kind, list, err)
		}
		rc, err := r.Get(ctx, name)
		if err != nil {
			t.Fatalf("%s get: %v", kind, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != "BACKUP" {
			t.Errorf("%s got %q", kind, b)
		}
		if err := r.Delete(ctx, name); err != nil {
			t.Fatalf("%s delete: %v", kind, err)
		}
		if err := r.Put(ctx, "../escape", strings.NewReader("x"), 1); err == nil {
			t.Errorf("%s: a name with a path", kind)
		}
		f.mu.Lock()
		for _, a := range f.auth {
			if kind == "webdav" && !strings.HasPrefix(a, "Basic ") || kind == "s3" && !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AK/") {
				t.Errorf("%s: a request without credentials: %q", kind, a)
			}
		}
		f.mu.Unlock()
	}
	if err := (&WebDAV{URL: "http://plain.example.com/dav"}).Put(ctx, "meridian-x.zip.age", strings.NewReader("x"), 1); err == nil ||
		!strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("plain http accepted: %v", err)
	}
}
