package cores

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// mieru's server (mita) comes from its GitHub releases, checked against the checksum the panel
// vouched for or GitHub's own. snell-server is closed source and comes only from Surge's site
// (dl.nssurge.com - the supervisor's choice): there is no checksum to ask anyone for, so each
// version Meridian knows is pinned here (and by the panel), and any other file is refused.

// SnellDigests are the SHA-256 of the snell-server archives Meridian knows: version/asset -> sum.
var SnellDigests = map[string]string{
	"5.0.1/snell-server-v5.0.1-linux-amd64.zip":   "9bea1c2b9e35b73b31634856c04d18c393072b9e5dcde6a32781d8b8f908c539",
	"5.0.1/snell-server-v5.0.1-linux-aarch64.zip": "2f178bf5ac468ce1a130454efa40a0603fbbe4e47ecc4880a989f4abc7f824cf",
}

// MitaAsset is mieru's server archive for this machine.
func MitaAsset(version string) (string, error) {
	switch runtime.GOARCH {
	case "amd64", "arm64":
		return fmt.Sprintf("mita_%s_linux_%s.tar.gz", version, runtime.GOARCH), nil
	}
	return "", fmt.Errorf("no mieru server build for %s", runtime.GOARCH)
}

// SnellAsset is snell-server's archive for this machine.
func SnellAsset(version string) (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return fmt.Sprintf("snell-server-v%s-linux-amd64.zip", version), nil
	case "arm64":
		return fmt.Sprintf("snell-server-v%s-linux-aarch64.zip", version), nil
	}
	return "", fmt.Errorf("no snell-server build for %s", runtime.GOARCH)
}

// EnsureMita makes sure mieru's server version is unpacked and returns the binary's path.
func EnsureMita(ctx context.Context, base, version, mirror string) (string, error) {
	getMu.Lock()
	defer getMu.Unlock()
	if err := checkVersion("mieru", version); err != nil {
		return "", err
	}
	dir := Dir(base, "mita", version)
	bin := filepath.Join(dir, "mita")
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	asset, err := MitaAsset(version)
	if err != nil {
		return "", err
	}
	gh := fmt.Sprintf("https://github.com/enfein/mieru/releases/download/v%s/", version)
	want := digestFor("mita", version, asset)
	if want == "" { // the panel had none: ask GitHub directly, never the mirror
		want = githubAssetDigest(ctx, "enfein/mieru", "v"+version, asset)
	}
	if want == "" {
		if b, err := fetch(ctx, []string{gh + asset + ".sha256.txt"}, 4096); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 && len(f[0]) == 64 {
				want = strings.ToLower(f[0])
			}
		}
	}
	if want == "" {
		return "", errors.New("cannot verify the mieru server download: no checksum from the panel or GitHub")
	}
	var urls []string
	if mirror != "" {
		urls = append(urls, fmt.Sprintf("%s/mita/%s/%s", mirror, version, asset))
	}
	urls = append(urls, gh+asset)
	gz, err := fetch(ctx, urls, 100<<20)
	if err != nil {
		return "", err
	}
	if err := checkSum(gz, want, "mieru server"); err != nil {
		return "", err
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
		if filepath.Base(h.Name) == "mita" && h.Typeflag == tar.TypeReg {
			if err := writeFile(bin, io.LimitReader(tr, 200<<20), 0o755); err != nil {
				return "", err
			}
			return bin, nil
		}
	}
	return "", errors.New("the mieru server archive has no mita binary")
}

// EnsureSnell makes sure snell-server version is unpacked and returns the binary's path. Only a
// version whose checksum is pinned (by the panel or here) is downloaded.
func EnsureSnell(ctx context.Context, base, version, mirror string) (string, error) {
	getMu.Lock()
	defer getMu.Unlock()
	if err := checkVersion("snell-server", version); err != nil {
		return "", err
	}
	dir := Dir(base, "snell", version)
	bin := filepath.Join(dir, "snell") // the core's own name, as InUse looks for it
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	asset, err := SnellAsset(version)
	if err != nil {
		return "", err
	}
	want := digestFor("snell", version, asset)
	if want == "" {
		want = SnellDigests[version+"/"+asset]
	}
	if want == "" {
		return "", fmt.Errorf("snell-server %s is not a version this agent can verify - pick a known one in Settings", version)
	}
	var urls []string
	if mirror != "" {
		urls = append(urls, fmt.Sprintf("%s/snell/%s/%s", mirror, version, asset))
	}
	urls = append(urls, "https://dl.nssurge.com/snell/"+asset)
	data, err := fetch(ctx, urls, 50<<20)
	if err != nil {
		return "", err
	}
	if err := checkSum(data, want, "snell-server"); err != nil {
		return "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != "snell-server" || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		err = writeFile(bin, io.LimitReader(rc, 100<<20), 0o755)
		rc.Close()
		if err != nil {
			return "", err
		}
		return bin, nil
	}
	return "", errors.New("the snell-server archive has no snell-server binary")
}
