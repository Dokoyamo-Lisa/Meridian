package cores

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCheckVersion(t *testing.T) {
	for _, ok := range []string{"26.3.27", "2.13.0", "2.9.6", "1.0"} {
		if checkVersion("x", ok) != nil {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{"", "../../etc", "26.3.27/../../x", "v26.3.27", "26", "1.2.3.4.5", "26.3.27\n"} {
		if checkVersion("x", bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	if got := parseDgst([]byte("MD5= 123\nSHA1= 456\nSHA2-256= " + strings.ToUpper(sum) + "\nSHA2-512= 789\n")); got != sum {
		t.Errorf("parseDgst = %q", got)
	}
	if got := parseDgst([]byte("SHA2-256= short")); got != "" {
		t.Errorf("parseDgst accepted a short digest: %q", got)
	}
	hashes := []byte(sum + "  build/hysteria-linux-amd64\n" + strings.Repeat("cd", 32) + "  build/hysteria-linux-arm64\n")
	if got := parseHashes(hashes, "hysteria-linux-amd64"); got != sum {
		t.Errorf("parseHashes = %q", got)
	}
	if got := parseHashes(hashes, "hysteria-linux-mips"); got != "" {
		t.Errorf("parseHashes found a missing asset: %q", got)
	}
}

func TestTrustedDigests(t *testing.T) {
	sum := strings.Repeat("ef", 32)
	SetDigests(map[string]string{"xray/26.3.27/Xray-linux-64.zip": "sha256:" + strings.ToUpper(sum), "bad/1.0/x": "nope"})
	if got := digestFor("xray", "26.3.27", "Xray-linux-64.zip"); got != sum {
		t.Errorf("digestFor = %q", got)
	}
	if got := digestFor("bad", "1.0", "x"); got != "" {
		t.Errorf("a malformed digest was trusted: %q", got)
	}
	if checkSum([]byte("data"), sum, "test") == nil {
		t.Error("checkSum accepted the wrong data")
	}
	if checkSum([]byte("data"), "", "test") == nil {
		t.Error("checkSum accepted a missing digest")
	}
	SetDigests(nil)
}

// TestMain keeps the tests off the internet: "GitHub" answers 404, so every download comes from
// the tests' own mirror - and a test that needs GitHub's answer says so.
func TestMain(m *testing.M) {
	gh := httptest.NewServer(http.NotFoundHandler())
	githubDL, githubAPI = gh.URL, gh.URL
	code := m.Run()
	gh.Close()
	os.Exit(code)
}

// TestDownloadOrder: the official release first; the panel's mirror only when it does not answer,
// or gives a file that does not match the checksum - and every download is told to Progress.
func TestDownloadOrder(t *testing.T) {
	file := []byte("the real thing")
	sum := sha256.Sum256(file)
	want := hex.EncodeToString(sum[:])
	var hits []string
	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "official")
		if r.URL.Path == "/tampered" {
			w.Write([]byte("something else"))
			return
		}
		w.Write(file)
	}))
	defer official.Close()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "mirror")
		w.Write(file)
	}))
	defer mirror.Close()
	var told []int64
	SetProgress(func(core string, done, total int64) {
		if core == "xray" {
			told = append(told, done)
		}
	})
	defer SetProgress(nil)

	b, err := fetchCore(context.Background(), "xray", []string{official.URL + "/ok", mirror.URL}, 1<<20, want)
	if err != nil || string(b) != string(file) || strings.Join(hits, ",") != "official" {
		t.Fatalf("the official file: %q %v %v", b, err, hits)
	}
	if len(told) < 2 || told[len(told)-1] != int64(len(file)) {
		t.Errorf("progress told %v", told)
	}
	hits = nil
	if b, err = fetchCore(context.Background(), "xray", []string{official.URL + "/tampered", mirror.URL}, 1<<20, want); err != nil ||
		string(b) != string(file) || strings.Join(hits, ",") != "official,mirror" {
		t.Fatalf("a file that does not match: %q %v %v", b, err, hits)
	}
	// a source that does not answer in time moves on to the next
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	old := answerWait
	answerWait = 200 * time.Millisecond
	defer func() { answerWait = old }()
	hits = nil
	start := time.Now()
	if b, err = fetchCore(context.Background(), "xray", []string{slow.URL, mirror.URL}, 1<<20, want); err != nil || string(b) != string(file) {
		t.Fatalf("after a source that did not answer: %q %v", b, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("waited %s for a source that did not answer", time.Since(start))
	}
	if _, err := fetchCore(context.Background(), "xray", []string{official.URL + "/tampered"}, 1<<20, want); err == nil {
		t.Error("a file that does not match was taken")
	}
}
