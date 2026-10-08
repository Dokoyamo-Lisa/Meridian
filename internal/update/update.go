// Package update keeps the panel current. The panel (an unprivileged user) finds Meridian's newest
// release on GitHub, checks that its SHA256SUMS carries the maintainers' signature and that the
// archive matches it, and leaves the archive with a request in its data directory. The updater
// service (root, started by a systemd path unit when the request appears) checks all of it again with
// the keys built into the installed binary - never trusting what the panel concluded - refuses
// anything that is not newer than what runs, and installs the release with its own
// install-panel.sh --upgrade. Proxies keep running throughout; only the panel restarts.
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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repo is where Meridian's releases are published.
const Repo = "Dokoyamo-Lisa/Meridian"

// API is GitHub's API. MERIDIAN_UPDATE_API may name another (an end-to-end test's); it must be
// HTTPS, and whatever it serves must still carry a trusted signature.
var API = "https://api.github.com"

const (
	maxArchive = 256 << 20 // a release archive (about 30 MB today)
	maxSmall   = 64 << 10  // SHA256SUMS, its signature, the release description
)

// client fetches releases: HTTPS only, and redirects only to GitHub's own download hosts.
var client = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" || !trustedHost(req.URL.Hostname()) {
		return fmt.Errorf("refusing a redirect to %s", req.URL.Redacted())
	}
	return nil
}}

// trustedHost says whether a download may come from host: GitHub, its asset hosts, or the API set
// for a test.
func trustedHost(host string) bool {
	host = strings.ToLower(host)
	if host == "github.com" || host == "api.github.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return true
	}
	if u, err := url.Parse(API); err == nil && strings.EqualFold(u.Hostname(), host) {
		return true
	}
	return false
}

// Release is a published Meridian release.
type Release struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	Published int64  `json:"published"`
	Notes     string `json:"notes,omitempty"`
	assets    map[string]string
}

var versionRE = regexp.MustCompile(`^v?(\d{1,4})\.(\d{1,4})\.(\d{1,6})$`)

// ParseVersion reads "0.7.1" or "v0.7.1" (no pre-releases: only those are offered).
func ParseVersion(s string) ([3]int, bool) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// Newer says whether version a comes after b. Anything unreadable is never newer.
func Newer(a, b string) bool {
	va, ok1 := ParseVersion(a)
	vb, ok2 := ParseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func get(ctx context.Context, raw string, limit int64, agent string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !trustedHost(u.Hostname()) {
		return nil, fmt.Errorf("refusing to download from %q", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", agent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", path.Base(u.Path))
	}
	return b, nil
}

// Latest is Meridian's newest release (GitHub leaves out drafts and pre-releases).
func Latest(ctx context.Context, current string) (*Release, error) {
	body, err := get(ctx, strings.TrimRight(API, "/")+"/repos/"+Repo+"/releases/latest", 4<<20, "Meridian/"+current)
	if err != nil {
		return nil, fmt.Errorf("checking for a new release: %w", err)
	}
	var gh struct {
		Tag       string    `json:"tag_name"`
		URL       string    `json:"html_url"`
		Body      string    `json:"body"`
		Published time.Time `json:"published_at"`
		Draft     bool      `json:"draft"`
		Pre       bool      `json:"prerelease"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &gh); err != nil {
		return nil, fmt.Errorf("checking for a new release: %w", err)
	}
	v, ok := ParseVersion(gh.Tag)
	if !ok || gh.Draft || gh.Pre {
		return nil, fmt.Errorf("the newest release %q is not a version Meridian installs", gh.Tag)
	}
	rel := &Release{Version: fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]), URL: gh.URL, Published: gh.Published.Unix(),
		assets: map[string]string{}}
	if len(gh.Body) > maxSmall {
		gh.Body = gh.Body[:maxSmall]
	}
	rel.Notes = strings.TrimSpace(gh.Body)
	for _, a := range gh.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel, nil
}

// ArchiveName is a release's archive for a CPU architecture.
func ArchiveName(version, arch string) string {
	return fmt.Sprintf("meridian-%s-linux-%s.tar.gz", version, arch)
}

// Request is what the panel leaves for the updater service.
type Request struct {
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Agents  bool   `json:"agents"` // upgrade every server's agent once the new panel runs
	At      int64  `json:"at"`
}

// Result is what the updater service leaves for the panel.
type Result struct {
	From   string `json:"from"`
	To     string `json:"to"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Agents bool   `json:"agents"`
	At     int64  `json:"at"`
}

// Files in the update directory.
const (
	RequestFile = "request.json"
	ResultFile  = "result.json"
	sumsFile    = "SHA256SUMS"
	sigFile     = "SHA256SUMS.sig"
)

// Stage downloads a release for arch into dir, checks it, and leaves the request for the updater
// service (which checks everything again). The newest release only: nothing older than current.
func Stage(ctx context.Context, rel *Release, current, dir, arch string, agents bool) error {
	if !Newer(rel.Version, current) {
		return fmt.Errorf("%s is not newer than the running %s", rel.Version, current)
	}
	name := ArchiveName(rel.Version, arch)
	for _, n := range []string{sumsFile, sigFile, name} {
		if rel.assets[n] == "" {
			return fmt.Errorf("release %s has no %s - it cannot be installed from the panel (a release made before signed updates, or a missing file)", rel.Version, n)
		}
	}
	agent := "Meridian/" + current
	sums, err := get(ctx, rel.assets[sumsFile], maxSmall, agent)
	if err != nil {
		return err
	}
	sig, err := get(ctx, rel.assets[sigFile], maxSmall, agent)
	if err != nil {
		return err
	}
	if err := Verify(sums, sig); err != nil {
		return err
	}
	archive, err := get(ctx, rel.assets[name], maxArchive, agent)
	if err != nil {
		return err
	}
	if err := checkSum(sums, name, archive); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for n, b := range map[string][]byte{sumsFile: sums, sigFile: sig, name: archive} {
		if err := writeFile(dir, n, b); err != nil {
			return err
		}
	}
	req, _ := json.Marshal(Request{Version: rel.Version, Arch: arch, Agents: agents, At: time.Now().Unix()})
	return writeFile(dir, RequestFile, req) // last: the updater service starts when it appears
}

// writeFile replaces dir/name atomically.
func writeFile(dir, name string, b []byte) error {
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), dir+"/"+name)
}

// checkSum finds name in a SHA256SUMS file and compares it with data.
func checkSum(sums []byte, name string, data []byte) error {
	sum := sha256.Sum256(data)
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return fmt.Errorf("%s is not listed in SHA256SUMS", name)
	}
	if want != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%s does not match its checksum - it was damaged or changed", name)
	}
	return nil
}

// ---------------------------------------------------------------- signatures

// Verify checks that sums carries a signature (base64 ed25519) by one of the trusted keys.
func Verify(sums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return errors.New("the release's signature cannot be read")
	}
	for _, k := range keys() {
		if ed25519.Verify(k, sums, raw) {
			return nil
		}
	}
	return errors.New("the release is not signed by Meridian's release key - it was not installed")
}

func keys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, s := range append(append([]string{}, trusted...), testKey) {
		if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == ed25519.PublicKeySize {
			out = append(out, ed25519.PublicKey(b))
		}
	}
	return out
}

// ---------------------------------------------------------------- archives

// extract unpacks the release directory of an archive into dest: regular files and directories under
// top only - no links, no absolute paths, nothing outside.
func extract(archive []byte, top, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(h.Name)
		if name != top && !strings.HasPrefix(name, top+"/") || strings.Contains(name, "..") || path.IsAbs(name) {
			return fmt.Errorf("unexpected entry %q in the release", h.Name)
		}
		target := dest + "/" + name
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(path.Dir(target), 0o700); err != nil {
				return err
			}
			total += h.Size
			if total > 4*maxArchive {
				return errors.New("the release unpacks to more than expected")
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0o755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, io.LimitReader(tr, h.Size)); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected entry %q in the release", h.Name)
		}
	}
}
