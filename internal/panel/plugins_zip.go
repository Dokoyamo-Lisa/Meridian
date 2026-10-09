package panel

// Unpacking an uploaded plugin. A zip is checked completely before anything is written: every name
// must be a plain path inside the plugin (no absolute paths, no "..", no backslashes), every entry a
// folder or a regular file (never a link or a device), and the number of files and their sizes are
// limited - by what the zip says and again while unpacking, so a zip that lies about its sizes stops
// at the limit. Files are then written into a new folder through os.Root, which cannot leave it.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
)

// pluginZip is an upload that passed every check.
type pluginZip struct {
	man   *pluginManifest
	files []*zip.File // the regular files to unpack
	names []string    // where each goes inside the plugin's folder
	sum   string      // SHA-256 of the upload
}

// junkEntry says whether a zip entry is something an operating system added: macOS's resource forks
// and folder settings.
func junkEntry(name string) bool {
	return strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store"
}

func openPluginZip(b []byte) (*pluginZip, error) {
	bad := func(format string, a ...any) error {
		return errStatus(http.StatusBadRequest, fmt.Sprintf(format, a...))
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, bad("that is not a zip file - a plugin is a .zip with plugin.json in it")
	}
	if len(zr.File) > pluginFileCount {
		return nil, bad("the zip has more than %d files", pluginFileCount)
	}
	var files []*zip.File
	var names []string
	for _, f := range zr.File {
		name := f.Name
		if junkEntry(name) {
			continue
		}
		dir := strings.HasSuffix(name, "/")
		if !pluginPath(strings.TrimSuffix(name, "/")) {
			return nil, bad("the zip holds %q, which is not a plain path inside the plugin - paths must be relative, without .. or backslashes",
				truncate(name, 80))
		}
		mode := f.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			return nil, bad("%s is a symbolic link - a plugin may only hold files and folders", truncate(name, 80))
		case dir || mode.IsDir():
			continue // folders are made as their files need them
		case !mode.IsRegular():
			return nil, bad("%s is not a regular file - a plugin may only hold files and folders", truncate(name, 80))
		}
		files = append(files, f)
		names = append(names, name)
	}
	// a folder zipped as a whole has everything in one top folder: its contents are the plugin
	if !slices.Contains(names, "plugin.json") {
		top, _, _ := strings.Cut(firstOf(names), "/")
		inOne := top != "" && slices.Contains(names, top+"/plugin.json")
		for _, n := range names {
			inOne = inOne && strings.HasPrefix(n, top+"/")
		}
		if !inOne {
			return nil, bad("plugin.json is missing - it must be at the top of the zip")
		}
		for i := range names {
			names[i] = strings.TrimPrefix(names[i], top+"/")
		}
	}
	sizes := map[string]int64{}
	var total uint64
	var manifest *zip.File
	for i, f := range files {
		n := names[i]
		if _, dup := sizes[n]; dup {
			return nil, bad("the zip holds %s twice", truncate(n, 80))
		}
		if f.UncompressedSize64 > pluginFileMax {
			return nil, bad("%s is larger than %d MB", truncate(n, 80), pluginFileMax>>20)
		}
		total += f.UncompressedSize64
		if total > pluginFilesMax {
			return nil, bad("unpacked, the plugin would be larger than %d MB", pluginFilesMax>>20)
		}
		sizes[n] = int64(f.UncompressedSize64)
		if n == "plugin.json" {
			manifest = f
		}
	}
	for n := range sizes { // a file and a folder of the same name cannot both be unpacked
		for d := path.Dir(n); d != "."; d = path.Dir(d) {
			if _, clash := sizes[d]; clash {
				return nil, bad("the zip holds %s both as a file and as a folder", truncate(d, 80))
			}
		}
	}
	raw, err := readZipFile(manifest, pluginJSONMax)
	if err != nil {
		return nil, bad("plugin.json cannot be read: %s", err.Error())
	}
	m, err := parsePluginManifest(raw)
	if err != nil {
		return nil, err
	}
	if err := m.check(func(name string) (int64, bool) {
		s, ok := sizes[name]
		return s, ok
	}); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return &pluginZip{man: m, files: files, names: names, sum: hex.EncodeToString(sum[:])}, nil
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// readZipFile reads one entry, at most max bytes.
func readZipFile(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("it is larger than %d KB", max>>10)
	}
	return b, nil
}

// unpack writes the plugin's files into dir, a folder that must not exist yet. Only its owner can
// read them; the program is the only file that can be run.
func (z *pluginZip) unpack(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	command := ""
	if z.man.Server != nil {
		command = z.man.Server.Command
	}
	var total int64
	for i, f := range z.files {
		name := z.names[i]
		if d := path.Dir(name); d != "." {
			if err := root.MkdirAll(d, 0o700); err != nil {
				return err
			}
		}
		mode := os.FileMode(0o600)
		if name == command {
			mode = 0o700
		}
		n, err := unpackOne(root, f, name, mode)
		if err != nil {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("%s cannot be unpacked: %s", truncate(name, 80), err.Error()))
		}
		if total += n; total > pluginFilesMax {
			return errStatus(http.StatusBadRequest, fmt.Sprintf("unpacked, the plugin would be larger than %d MB", pluginFilesMax>>20))
		}
	}
	return nil
}

func unpackOne(root *os.Root, f *zip.File, name string, mode os.FileMode) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return 0, err
	}
	// archive/zip refuses data beyond the size the entry declares; the limit holds even so
	n, err := io.Copy(out, io.LimitReader(rc, pluginFileMax+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > pluginFileMax {
		err = fmt.Errorf("it is larger than %d MB", pluginFileMax>>20)
	}
	if err == nil {
		err = root.Chmod(name, mode) // whatever the umask took away
	}
	return n, err
}
