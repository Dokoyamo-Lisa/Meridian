package cores

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestEnsureSingBox: sing-box comes from the mirror only when its checksum is the one vouched for, and
// the binary is taken out of the release's folder.
func TestEnsureSingBox(t *testing.T) {
	asset, err := SingBoxAsset("1.14.2")
	if err != nil {
		t.Skip(err) // not amd64 or arm64
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho sing-box\n")
	dir := strings.TrimSuffix(asset, ".tar.gz") + "/"
	_ = tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: dir + "LICENSE", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2})
	_, _ = tw.Write([]byte("ok"))
	_ = tw.WriteHeader(&tar.Header{Name: dir + "sing-box", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
	_, _ = tw.Write(body)
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sing-box/1.14.2/"+asset {
			http.NotFound(w, r)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()
	sum := sha256.Sum256(archive)

	base := t.TempDir()
	SetDigests(map[string]string{"sing-box/1.14.2/" + asset: "sha256:" + strings.Repeat("00", 32)})
	if _, err := EnsureSingBox(context.Background(), base, "1.14.2", srv.URL); err == nil {
		t.Fatal("a download with another checksum was installed")
	}
	SetDigests(map[string]string{"sing-box/1.14.2/" + asset: "sha256:" + hex.EncodeToString(sum[:])})
	defer SetDigests(nil)
	bin, err := EnsureSingBox(context.Background(), base, "1.14.2", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(bin); err != nil || !bytes.Equal(b, body) || !strings.HasSuffix(bin, "/cores/sing-box/1.14.2/sing-box") {
		t.Fatalf("installed %s: %q %v", bin, b, err)
	}
	if _, err := EnsureSingBox(context.Background(), base, "../1.14.2", srv.URL); err == nil {
		t.Error("a version that is a path was accepted")
	}
}
