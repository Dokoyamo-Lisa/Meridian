// Package cores downloads and unpacks the proxy cores the agent runs (Xray, Hysteria, realm). It
// tries the panel's mirror first - servers with poor GitHub access then still install - and GitHub
// second.
//
// Integrity: every download is checked against a SHA-256 that comes either from the panel inside
// the signed and encrypted state, or straight from GitHub over HTTPS. A checksum is never taken
// from the mirror itself, so a tampered mirror or a man in the middle on a plain-HTTP panel
// address cannot slip in a different binary.
package cores

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	trustMu sync.RWMutex
	trusted = map[string]string{}
	// getMu lets one download (or clean-up) of the cores' directories run at a time; the engines do not
	// hold their own locks while one runs, so reports go on during a slow download
	getMu sync.Mutex
)

// SetDigests installs the checksums the panel vouched for, keyed "core/version/asset".
func SetDigests(d map[string]string) {
	trustMu.Lock()
	defer trustMu.Unlock()
	trusted = map[string]string{}
	for k, v := range d {
		v = strings.ToLower(strings.TrimPrefix(v, "sha256:"))
		if len(v) == 64 {
			trusted[k] = v
		}
	}
}

func digestFor(core, version, asset string) string {
	trustMu.RLock()
	defer trustMu.RUnlock()
	return trusted[core+"/"+version+"/"+asset]
}

// githubAssetDigest asks the GitHub API (over HTTPS) for the SHA-256 GitHub computed for a release
// asset. Empty when GitHub is unreachable or has none.
func githubAssetDigest(ctx context.Context, repo, tag, asset string) string {
	b, err := fetch(ctx, []string{fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, tag)}, 4<<20)
	if err != nil {
		return ""
	}
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if json.Unmarshal(b, &rel) != nil {
		return ""
	}
	for _, a := range rel.Assets {
		if a.Name == asset {
			if d := strings.ToLower(strings.TrimPrefix(a.Digest, "sha256:")); len(d) == 64 {
				return d
			}
		}
	}
	return ""
}

func checkSum(data []byte, want, what string) error {
	sum := sha256.Sum256(data)
	if want == "" || hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("%s download does not match its published checksum", what)
	}
	return nil
}

// Dir is where a core version lives.
func Dir(base, name, version string) string { return filepath.Join(base, "cores", name, version) }

var versionRE = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,4}){1,3}$`)

// checkVersion refuses anything but a plain version number: versions become paths and URLs.
func checkVersion(core, version string) error {
	if !versionRE.MatchString(version) {
		return fmt.Errorf("invalid %s version %q", core, version)
	}
	return nil
}

func xrayAsset() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "Xray-linux-64.zip", nil
	case "arm64":
		return "Xray-linux-arm64-v8a.zip", nil
	}
	return "", fmt.Errorf("no Xray build for %s", runtime.GOARCH)
}

func realmAsset() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "realm-x86_64-unknown-linux-musl.tar.gz", nil
	case "arm64":
		return "realm-aarch64-unknown-linux-musl.tar.gz", nil
	}
	return "", fmt.Errorf("no realm build for %s", runtime.GOARCH)
}

// transport is how downloads go out (nil: Go's default). The agent sets one that takes the panel's
// mirror through its relay to the panel, when it has one.
var transport struct {
	sync.Mutex
	rt http.RoundTripper
}

// SetTransport sets how downloads go out.
func SetTransport(rt http.RoundTripper) {
	transport.Lock()
	transport.rt = rt
	transport.Unlock()
}

// fetch downloads the first URL that works.
func fetch(ctx context.Context, urls []string, limit int64) ([]byte, error) {
	var errs []string
	transport.Lock()
	client := &http.Client{Timeout: 5 * time.Minute, Transport: transport.rt}
	transport.Unlock()
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "meridian-agent")
		resp, err := client.Do(req)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s: %s", u, resp.Status))
			continue
		}
		return b, nil
	}
	return nil, errors.New("download failed: " + strings.Join(errs, "; "))
}

// EnsureXray makes sure version is unpacked and returns its directory.
func EnsureXray(ctx context.Context, base, version, mirror string) (string, error) {
	getMu.Lock()
	defer getMu.Unlock()
	if err := checkVersion("Xray", version); err != nil {
		return "", err
	}
	dir := Dir(base, "xray", version)
	if _, err := os.Stat(filepath.Join(dir, "xray")); err == nil {
		return dir, nil
	}
	asset, err := xrayAsset()
	if err != nil {
		return "", err
	}
	gh := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/v%s/%s", version, asset)
	want := digestFor("xray", version, asset)
	if want == "" { // the panel had none: ask GitHub directly, never the mirror
		if dgst, err := fetch(ctx, []string{gh + ".dgst"}, 1<<16); err == nil {
			want = parseDgst(dgst)
		}
	}
	if want == "" {
		want = githubAssetDigest(ctx, "XTLS/Xray-core", "v"+version, asset)
	}
	if want == "" {
		return "", errors.New("cannot verify the Xray download: no checksum from the panel or GitHub")
	}
	var urls []string
	if mirror != "" {
		urls = append(urls, fmt.Sprintf("%s/xray/%s/%s", mirror, version, asset))
	}
	urls = append(urls, gh)
	zipped, err := fetch(ctx, urls, 200<<20)
	if err != nil {
		return "", err
	}
	if err := checkSum(zipped, want, "Xray"); err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil {
		return "", err
	}
	tmp := dir + ".part"
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		if name != "xray" && name != "geoip.dat" && name != "geosite.dat" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		mode := os.FileMode(0o644)
		if name == "xray" {
			mode = 0o755
		}
		err = writeFile(filepath.Join(tmp, name), rc, mode)
		rc.Close()
		if err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "xray")); err != nil {
		return "", errors.New("the Xray archive has no xray binary")
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// EnsureHysteria makes sure the official Hysteria server version is in place and returns its path.
func EnsureHysteria(ctx context.Context, base, version, mirror string) (string, error) {
	getMu.Lock()
	defer getMu.Unlock()
	if err := checkVersion("Hysteria", version); err != nil {
		return "", err
	}
	dir := Dir(base, "hysteria", version)
	bin := filepath.Join(dir, "hysteria")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	var asset string
	switch runtime.GOARCH {
	case "amd64":
		asset = "hysteria-linux-amd64"
	case "arm64":
		asset = "hysteria-linux-arm64"
	default:
		return "", fmt.Errorf("no Hysteria build for %s", runtime.GOARCH)
	}
	gh := fmt.Sprintf("https://github.com/apernet/hysteria/releases/download/app%%2Fv%s/", version)
	want := digestFor("hysteria", version, asset)
	if want == "" { // the panel had none: ask GitHub directly, never the mirror
		if hashes, err := fetch(ctx, []string{gh + "hashes.txt"}, 1<<20); err == nil {
			want = parseHashes(hashes, asset)
		}
	}
	if want == "" {
		want = githubAssetDigest(ctx, "apernet/hysteria", "app/v"+version, asset)
	}
	if want == "" {
		return "", errors.New("cannot verify the Hysteria download: no checksum from the panel or GitHub")
	}
	var urls []string
	if mirror != "" {
		urls = append(urls, fmt.Sprintf("%s/hysteria/%s/%s", mirror, version, asset))
	}
	urls = append(urls, gh+asset)
	data, err := fetch(ctx, urls, 100<<20)
	if err != nil {
		return "", err
	}
	if err := checkSum(data, want, "Hysteria"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := writeFile(bin, bytes.NewReader(data), 0o755); err != nil {
		return "", err
	}
	return bin, nil
}

// EnsureRealm makes sure realm version is unpacked and returns the binary path.
func EnsureRealm(ctx context.Context, base, version, mirror string) (string, error) {
	getMu.Lock()
	defer getMu.Unlock()
	if err := checkVersion("realm", version); err != nil {
		return "", err
	}
	dir := Dir(base, "realm", version)
	bin := filepath.Join(dir, "realm")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	asset, err := realmAsset()
	if err != nil {
		return "", err
	}
	gh := fmt.Sprintf("https://github.com/zhboner/realm/releases/download/v%s/%s", version, asset)
	want := digestFor("realm", version, asset)
	if want == "" {
		want = githubAssetDigest(ctx, "zhboner/realm", "v"+version, asset)
	}
	var urls []string
	if mirror != "" && want != "" { // the mirror is used only when the result can be verified
		urls = append(urls, fmt.Sprintf("%s/realm/%s/%s", mirror, version, asset))
	}
	urls = append(urls, gh)
	gz, err := fetch(ctx, urls, 100<<20)
	if err != nil {
		return "", err
	}
	if want != "" {
		if err := checkSum(gz, want, "realm"); err != nil {
			return "", err
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(zr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if filepath.Base(h.Name) == "realm" && h.Typeflag == tar.TypeReg {
			if err := writeFile(bin, tr, 0o755); err != nil {
				return "", err
			}
			return bin, nil
		}
	}
	return "", errors.New("the realm archive has no realm binary")
}

// parseDgst reads the SHA2-256 line of an Xray .dgst file.
func parseDgst(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "SHA2-256="); ok {
			if v = strings.ToLower(strings.TrimSpace(v)); len(v) == 64 {
				return v
			}
		}
	}
	return ""
}

// parseHashes reads "<sha256>  <file>" lines (Hysteria's hashes.txt).
func parseHashes(b []byte, asset string) string {
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && filepath.Base(f[1]) == asset && len(f[0]) == 64 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(r, 300<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// currentLink is a core's "current" link: the version this host runs. Units point at it, so they
// never change when the version does.
func currentLink(base, name string) string { return filepath.Join(base, "cores", name, "current") }

// Current is the version of a core this host runs ("" before its first install).
func Current(base, name string) string {
	target, err := os.Readlink(currentLink(base, name))
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// InUse is the binary of the version of Hysteria or realm this host runs, through the "current" link.
// Without one, the newest version an older agent installed is adopted, or want is installed (a first
// install). A new version in the panel's settings never changes what runs - Upgrade does, when asked.
func InUse(ctx context.Context, base, name, want, mirror string) (string, error) {
	bin := filepath.Join(currentLink(base, name), name)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	v := newestInstalled(base, name)
	if v == "" {
		if _, err := ensure(ctx, base, name, want, mirror); err != nil {
			return "", err
		}
		v = want
	}
	if err := switchCore(base, name, v); err != nil {
		return "", err
	}
	return bin, nil
}

// Upgrade installs version of Hysteria or realm and makes it the one in use; running processes keep
// the old one until they restart. It returns the version in use before.
func Upgrade(ctx context.Context, base, name, version, mirror string) (string, error) {
	prev := Current(base, name)
	if _, err := ensure(ctx, base, name, version, mirror); err != nil {
		return prev, err
	}
	return prev, switchCore(base, name, version)
}

func ensure(ctx context.Context, base, name, version, mirror string) (string, error) {
	switch name {
	case "hysteria":
		return EnsureHysteria(ctx, base, version, mirror)
	case "realm":
		return EnsureRealm(ctx, base, version, mirror)
	}
	return "", fmt.Errorf("unknown core %q", name)
}

// switchCore points a core's "current" link at version, atomically.
func switchCore(base, name, version string) error {
	link := currentLink(base, name)
	tmp := link + ".new"
	os.Remove(tmp)
	if err := os.Symlink(Dir(base, name, version), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

// newestInstalled is the newest version of a core with its binary in place ("" when none is).
func newestInstalled(base, name string) string {
	entries, _ := os.ReadDir(filepath.Join(base, "cores", name))
	best := ""
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "current" {
			continue
		}
		if _, err := os.Stat(filepath.Join(base, "cores", name, e.Name(), name)); err != nil {
			continue
		}
		if best == "" || versionLess(best, e.Name()) {
			best = e.Name()
		}
	}
	return best
}

// versionLess compares dotted versions number by number ("2.9.10" comes after "2.9.6").
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, errA := strconv.Atoi(pa[i])
		y, errB := strconv.Atoi(pb[i])
		if errA != nil || errB != nil {
			if pa[i] != pb[i] {
				return pa[i] < pb[i]
			}
			continue
		}
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

// Prune removes versions of a core other than keep.
func Prune(base, name string, keep ...string) {
	getMu.Lock()
	defer getMu.Unlock()
	entries, _ := os.ReadDir(filepath.Join(base, "cores", name))
	for _, e := range entries {
		k := false
		for _, v := range keep {
			if e.Name() == v {
				k = true
			}
		}
		if !k && e.Name() != "current" {
			os.RemoveAll(filepath.Join(base, "cores", name, e.Name()))
		}
	}
}
