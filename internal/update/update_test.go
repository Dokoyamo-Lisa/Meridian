package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.7.1", "0.7.0", true}, {"v0.10.0", "0.9.9", true}, {"1.0.0", "0.99.99", true},
		{"0.7.0", "0.7.0", false}, {"0.6.9", "0.7.0", false}, {"0.7.1-rc1", "0.7.0", false}, {"", "0.7.0", false},
		{"0.7.1", "dev", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// testSigner makes a key the package trusts for the test.
func testSigner(t *testing.T) ed25519.PrivateKey {
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := testKey
	testKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { testKey = old })
	return priv
}

func sign(priv ed25519.PrivateKey, b []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b)) + "\n")
}

func TestVerify(t *testing.T) {
	priv := testSigner(t)
	sums := []byte("abc  meridian-0.7.1-linux-amd64.tar.gz\n")
	if err := Verify(sums, sign(priv, sums)); err != nil {
		t.Fatal(err)
	}
	if err := Verify(append(sums, 'x'), sign(priv, sums)); err == nil {
		t.Error("a changed SHA256SUMS passed")
	}
	_, other, _ := ed25519.GenerateKey(nil)
	if err := Verify(sums, sign(other, sums)); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Errorf("another key: %v", err)
	}
	if err := Verify(sums, []byte("garbage")); err == nil {
		t.Error("garbage passed")
	}
	if len(keys()) != 2 { // the release key, and this test's
		t.Errorf("keys: %d", len(keys()))
	}
}

// tarball builds a release archive with the given entries (name -> content; "->x" makes a link to x).
func tarball(t *testing.T, entries map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		h := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if target, ok := strings.CutPrefix(body, "->"); ok {
			h = &tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtract(t *testing.T) {
	top := "meridian-0.7.1-linux-amd64"
	good := tarball(t, map[string]string{top + "/meridian": "bin", top + "/agent/meridian-agent-linux-arm64": "a"})
	dir := t.TempDir()
	if err := extract(good, top, dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, top, "agent/meridian-agent-linux-arm64")); string(b) != "a" {
		t.Errorf("unpacked: %q", b)
	}
	for name, entries := range map[string]map[string]string{
		"outside":   {top + "/../evil": "x"},
		"absolute":  {"/etc/evil": "x"},
		"other top": {"other/meridian": "x"},
		"link":      {top + "/meridian": "->/etc/shadow"},
	} {
		if err := extract(tarball(t, entries), top, t.TempDir()); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckSum(t *testing.T) {
	data := []byte("archive")
	sum := sha256.Sum256(data)
	sums := []byte(hex.EncodeToString(sum[:]) + "  meridian-0.7.1-linux-amd64.tar.gz\n")
	if err := checkSum(sums, "meridian-0.7.1-linux-amd64.tar.gz", data); err != nil {
		t.Fatal(err)
	}
	if err := checkSum(sums, "meridian-0.7.1-linux-amd64.tar.gz", []byte("changed")); err == nil {
		t.Error("a changed archive passed")
	}
	if err := checkSum(sums, "meridian-0.7.1-linux-arm64.tar.gz", data); err == nil {
		t.Error("an archive that is not listed passed")
	}
}

// TestStage: the newest release is found, checked and left with a request - only when it is newer,
// signed, and matches its checksum.
func TestStage(t *testing.T) {
	priv := testSigner(t)
	archive := tarball(t, map[string]string{"meridian-0.7.1-linux-amd64/meridian": "new panel"})
	sum := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(sum[:]) + "  meridian-0.7.1-linux-amd64.tar.gz\n")
	files := map[string][]byte{"SHA256SUMS": sums, "SHA256SUMS.sig": sign(priv, sums), "meridian-0.7.1-linux-amd64.tar.gz": archive}
	var ts *httptest.Server
	ts = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+Repo+"/releases/latest" {
			var assets []map[string]string
			for n := range files {
				assets = append(assets, map[string]string{"name": n, "browser_download_url": ts.URL + "/dl/" + n})
			}
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.7.1", "html_url": "https://github.com/" + Repo,
				"published_at": "2026-10-08T01:00:00Z", "body": "Notes", "assets": assets})
			return
		}
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	oldAPI, oldTransport := API, client.Transport
	API, client.Transport = ts.URL, ts.Client().Transport
	defer func() { API, client.Transport = oldAPI, oldTransport }()
	ctx := context.Background()

	rel, err := Latest(ctx, "0.7.0")
	if err != nil || rel.Version != "0.7.1" || rel.Notes != "Notes" {
		t.Fatalf("latest: %+v %v", rel, err)
	}
	dir := filepath.Join(t.TempDir(), "update")
	if err := Stage(ctx, rel, "0.7.1", dir, "amd64", true); err == nil {
		t.Error("staged a release that is not newer")
	}
	if err := Stage(ctx, rel, "0.7.0", dir, "arm64", true); err == nil || !strings.Contains(err.Error(), "no meridian-0.7.1-linux-arm64") {
		t.Errorf("another architecture: %v", err)
	}
	if err := Stage(ctx, rel, "0.7.0", dir, "amd64", true); err != nil {
		t.Fatal(err)
	}
	var req Request
	raw, _ := os.ReadFile(filepath.Join(dir, RequestFile))
	if json.Unmarshal(raw, &req) != nil || req.Version != "0.7.1" || !req.Agents || req.Arch != "amd64" {
		t.Errorf("request: %s", raw)
	}
	// a changed archive, or a signature by another key, is never staged
	os.RemoveAll(dir)
	files["meridian-0.7.1-linux-amd64.tar.gz"] = append(append([]byte{}, archive...), 0)
	if err := Stage(ctx, rel, "0.7.0", dir, "amd64", true); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("changed archive: %v", err)
	}
	files["meridian-0.7.1-linux-amd64.tar.gz"] = archive
	_, other, _ := ed25519.GenerateKey(nil)
	files["SHA256SUMS.sig"] = sign(other, sums)
	if err := Stage(ctx, rel, "0.7.0", dir, "amd64", true); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Errorf("another key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, RequestFile)); err == nil {
		t.Error("a request was left for a release that failed its checks")
	}
	// downloads go to GitHub (or the API's host) over HTTPS only
	if _, err := get(ctx, "http://github.com/x", 10, "t"); err == nil {
		t.Error("plain HTTP")
	}
	if _, err := get(ctx, "https://evil.example.com/x", 10, "t"); err == nil {
		t.Error("another host")
	}
}
