package panel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"meridian/internal/backup"
)

// davStore is a WebDAV folder in memory. hide and keep make it a broken one: it lists no files, or
// answers a removal without removing.
type davStore struct {
	mu         sync.Mutex
	files      map[string][]byte
	hide, keep bool
}

func (d *davStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	switch r.Method {
	case "PUT":
		b, _ := io.ReadAll(r.Body)
		d.files[name] = b
		w.WriteHeader(201)
	case "GET":
		if b, ok := d.files[name]; ok {
			w.Write(b)
			return
		}
		w.WriteHeader(404)
	case "DELETE":
		if !d.keep {
			delete(d.files, name)
		}
		w.WriteHeader(204)
	case "PROPFIND":
		w.WriteHeader(207)
		io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
		for n, b := range d.files {
			if d.hide {
				break
			}
			fmt.Fprintf(w, `<d:response><d:href>/dav/%s</d:href><d:propstat><d:prop><d:getcontentlength>%d</d:getcontentlength><d:getlastmodified>Fri, 09 Oct 2026 10:00:00 GMT</d:getlastmodified></d:prop></d:propstat></d:response>`, n, len(b))
		}
		io.WriteString(w, `</d:multistatus>`)
	}
}

// TestBackups: settings need the browser and HTTPS; a download needs the password and holds the
// database; backups go to the destination encrypted and the oldest are removed; a restore - from the
// destination or uploaded - is checked and staged, and the panel restarts for it.
func TestBackups(t *testing.T) {
	h := newHarness(t)
	b := h.browser()
	b.login("owner", "owner-password-1")
	var restarts atomic.Int32
	restartHook.Store(func() { restarts.Add(1) })
	t.Cleanup(func() { restartHook.Store(func() {}) })
	dav := &davStore{files: map[string][]byte{}}
	srv := httptest.NewTLSServer(dav)
	defer srv.Close()
	oldClient := backup.Client
	backup.Client = srv.Client()
	t.Cleanup(func() { backup.Client = oldClient })

	full := b.must("POST", "/api/tokens", map[string]any{"name": "rw", "scope": "full"}, 201)["token"].(string)
	if code, _, _ := h.bearer(full).do("PUT", "/api/backups", map[string]any{"kind": "webdav"}); code != 403 {
		t.Errorf("an API token changed the backup settings: %d", code)
	}
	for _, bad := range []map[string]any{
		{"kind": "webdav", "webdav_url": "http://dav.example.com/x"},
		{"passphrase": "short"},
		{"schedule": "daily"}, // no destination or passphrase yet
	} {
		if code, _, raw := b.do("PUT", "/api/backups", bad); code != 400 {
			t.Errorf("%v: %d %s", bad, code, raw)
		}
	}
	v := b.must("PUT", "/api/backups", map[string]any{"kind": "webdav", "webdav_url": srv.URL + "/dav/", "webdav_user": "u",
		"webdav_password": "dav-secret", "passphrase": "correct horse battery staple", "keep": 1, "schedule": "daily"}, 200)
	if v["passphrase_set"] != true || v["secret_set"] != true || strings.Contains(fmt.Sprint(v), "dav-secret") || strings.Contains(fmt.Sprint(v), "battery") {
		t.Fatalf("view: %v", v)
	}
	b.must("POST", "/api/backups/test", nil, 200)

	// a download needs the password
	if code, _, _ := b.do("POST", "/api/backups/download", map[string]any{"password": "wrong-password"}); code != 403 {
		t.Errorf("download with a wrong password: %d", code)
	}
	code, _, raw := b.do("POST", "/api/backups/download", map[string]any{"password": "owner-password-1"})
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if code != 200 || err != nil {
		t.Fatalf("download: %d %v", code, err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["manifest.json"] || !names["meridian.db"] {
		t.Errorf("the backup holds %v", names)
	}
	_, _, raw = b.do("POST", "/api/backups/download", map[string]any{"password": "owner-password-1", "passphrase": "another long passphrase"})
	if !backup.IsEncrypted(raw) {
		t.Error("a download with a passphrase is not encrypted")
	}

	// to the destination, encrypted; the oldest beyond keep go
	b.must("POST", "/api/backups/run", nil, 200)
	b.must("POST", "/api/backups/run", nil, 200)
	list := b.must("GET", "/api/backups/remote", nil, 200)["backups"].([]any)
	if len(list) != 1 {
		t.Fatalf("kept: %v", list)
	}
	name := list[0].(map[string]any)["name"].(string)
	dav.mu.Lock()
	enc := backup.IsEncrypted(dav.files[name])
	dav.mu.Unlock()
	if !enc || !strings.HasSuffix(name, ".zip.age") {
		t.Errorf("an unencrypted backup left the panel: %s", name)
	}

	// restore from the destination: staged, then the panel restarts
	if code, _, _ := b.do("POST", "/api/backups/restore", map[string]any{"password": "nope", "name": name}); code != 403 {
		t.Errorf("restore without the password: %d", code)
	}
	r := b.must("POST", "/api/backups/restore", map[string]any{"password": "owner-password-1", "name": name}, 202)
	if r["restarting"] != true {
		t.Errorf("restore: %v", r)
	}
	if _, ok := backup.Pending(h.p.cfg.DataDir); !ok {
		t.Fatal("nothing staged")
	}
	if v := b.must("GET", "/api/backups", nil, 200); v["pending"] == nil {
		t.Error("the view does not show the staged restore")
	}
	b.must("DELETE", "/api/backups/restore", nil, 200)
	if _, ok := backup.Pending(h.p.cfg.DataDir); ok {
		t.Error("the staged restore was not dropped")
	}

	// an upload: the password is checked before the file is taken in; a wrong passphrase stages nothing
	upload := func(pw, pass string, file []byte) (int, string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("password", pw)
		mw.WriteField("passphrase", pass)
		fw, _ := mw.CreateFormFile("file", "backup.zip.age")
		fw.Write(file)
		mw.Close()
		req, _ := http.NewRequest("POST", h.srv.URL+"/api/backups/restore", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-Meridian", "1")
		resp, err := b.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	dav.mu.Lock()
	file := dav.files[name]
	dav.mu.Unlock()
	if code, body := upload("owner-password-1", "a wrong passphrase!!", file); code != 400 || !strings.Contains(body, "passphrase") {
		t.Errorf("a wrong passphrase: %d %s", code, body)
	}
	if code, _ := upload("wrong", "correct horse battery staple", file); code != 403 {
		t.Errorf("an upload without the password: %d", code)
	}
	if code, body := upload("owner-password-1", "correct horse battery staple", file); code != 202 {
		t.Errorf("upload: %d %s", code, body)
	}
	var info backup.Info
	got, _ := backup.Pending(h.p.cfg.DataDir)
	b2, _ := json.Marshal(got)
	json.Unmarshal(b2, &info)
	if info.Version != Version {
		t.Errorf("staged: %+v", info)
	}
	backup.Discard(h.p.cfg.DataDir)
	for i := 0; i < 30 && restarts.Load() < 2; i++ { // each restart runs a second after its answer
		waitShort()
	}
	if restarts.Load() != 2 {
		t.Errorf("restarts for two restores: %d", restarts.Load())
	}
}

func waitShort() { time.Sleep(100 * time.Millisecond) }

// TestStaleBackups: retention counts and removes only this panel's encrypted backups. Another
// panel's in the same folder or bucket - even one whose host starts like this one's - and plain
// ZIPs neither take a kept place nor go.
func TestStaleBackups(t *testing.T) {
	obj := func(names ...string) []backup.Object {
		var l []backup.Object
		for _, n := range names {
			l = append(l, backup.Object{Name: n})
		}
		return l
	}
	const me = "meridian-a.example.com-"
	// a shared destination, listed in no particular order
	shared := obj(
		"meridian-b.example.com-20261029-040000.zip.age", // another panel's, newer than all of mine
		me+"20261027-040000.zip.age",
		"meridian-b.example.com-20261028-040000.zip.age",
		me+"20261029-040000.zip.age",
		"meridian-a.example.com-x-20261029-050000.zip.age", // a host that starts like mine
		me+"20261028-040000.zip.age",
		me+"notes.zip.age", // not a time
	)
	got := staleBackups(shared, me, 2)
	if len(got) != 1 || got[0] != me+"20261027-040000.zip.age" {
		t.Errorf("shared destination, keep 2: %v", got)
	}
	// mixed: the newest encrypted, a plain ZIP, an older encrypted - keep 2 keeps both encrypted
	mixed := obj(me+"20261029-040000.zip.age", me+"20261028-120000.zip", me+"20261027-040000.zip.age")
	if got := staleBackups(mixed, me, 2); len(got) != 0 {
		t.Errorf("a plain ZIP took an encrypted backup's place: %v", got)
	}
	if got := staleBackups(mixed, me, 1); len(got) != 1 || got[0] != me+"20261027-040000.zip.age" {
		t.Errorf("mixed, keep 1: %v", got)
	}
	if got := staleBackups(mixed, me, 0); got != nil {
		t.Errorf("keep 0 removes nothing: %v", got)
	}
}

// davBackups points the panel's backups at a WebDAV store in memory, keeping keep of them.
func davBackups(t *testing.T, b *client, dav *davStore, keep int) {
	t.Helper()
	srv := httptest.NewTLSServer(dav)
	t.Cleanup(srv.Close)
	oldClient := backup.Client
	backup.Client = srv.Client()
	t.Cleanup(func() { backup.Client = oldClient })
	b.must("PUT", "/api/backups", map[string]any{"kind": "webdav", "webdav_url": srv.URL + "/dav/", "webdav_user": "u",
		"webdav_password": "dav-secret", "passphrase": "correct horse battery staple", "keep": keep}, 200)
}

// TestBackupNamesUnique: backups made in quick succession - in the same second, even the same
// millisecond - get names of their own, later and later, so a destination keeps each one; retention
// orders names stamped to the second (before 1.4.2) and to the millisecond together.
func TestBackupNamesUnique(t *testing.T) {
	h := newHarness(t)
	prefix := h.p.backupPrefix()
	seen := map[string]bool{}
	var last time.Time
	for i := 0; i < 200; i++ {
		n := h.p.backupName(true)
		at, ok := backupStampOf(n, prefix, ".zip.age")
		if seen[n] || !ok || !at.After(last) {
			t.Fatalf("name %d: %s (repeated %v, a time %v, later %v)", i, n, seen[n], ok, at.After(last))
		}
		seen[n], last = true, at
	}

	// two backups run at once after another reach the destination as two
	b := h.browser()
	b.login("owner", "owner-password-1")
	dav := &davStore{files: map[string][]byte{}}
	davBackups(t, b, dav, 5)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := h.p.backupNow(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	dav.mu.Lock()
	n := len(dav.files)
	dav.mu.Unlock()
	if n != 2 {
		t.Errorf("two quick backups left %d file(s) at the destination", n)
	}

	// old and new names together: by time, not by text
	obj := func(names ...string) []backup.Object {
		var l []backup.Object
		for _, x := range names {
			l = append(l, backup.Object{Name: x})
		}
		return l
	}
	mixed := obj(prefix+"20261029-152015.zip.age", prefix+"20261029-152015.500.zip.age", prefix+"20261029-152016.zip.age",
		prefix+"20261029-152014.999.zip.age")
	if got := staleBackups(mixed, prefix, 2); len(got) != 2 || got[0] != prefix+"20261029-152015.zip.age" || got[1] != prefix+"20261029-152014.999.zip.age" {
		t.Errorf("old and new names: %v", got)
	}
}

// TestBackupDestinationCheck: "test" says a destination works only when the file it writes shows in
// the listing and is gone after removing it - a destination that takes files but lists none, or that
// answers a removal but keeps the file, is said not to work, and why.
func TestBackupDestinationCheck(t *testing.T) {
	for _, c := range []struct {
		name       string
		hide, keep bool
		code       int
		says       string
	}{
		{"a working destination", false, false, 200, "can write, list and remove"},
		{"one that lists nothing", true, false, 502, "does not list it"},
		{"one that keeps what it removes", false, true, 502, "still lists it"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			b := h.browser()
			b.login("owner", "owner-password-1")
			dav := &davStore{files: map[string][]byte{}, hide: c.hide, keep: c.keep}
			davBackups(t, b, dav, 3)
			code, m, raw := b.do("POST", "/api/backups/test", nil)
			if code != c.code || !strings.Contains(string(raw), c.says) {
				t.Errorf("%d %v", code, m)
			}
			dav.mu.Lock()
			left := len(dav.files)
			dav.mu.Unlock()
			if !c.keep && left != 0 {
				t.Errorf("the test file was left behind (%d)", left)
			}
		})
	}
}
