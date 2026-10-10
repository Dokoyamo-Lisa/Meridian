// Package backup makes and restores the panel's backups. A backup is a ZIP holding manifest.json, a
// consistent snapshot of the database (meridian.db) and the plugins' files (plugins/..., and their
// data, plugin-data/...). It holds every key and credential the panel has, so a backup that leaves
// the panel's host is encrypted with a passphrase - with age (https://age-encryption.org), which the
// age command line tool opens too: age -d -o backup.zip backup.zip.age
//
// A restore is staged: the backup is checked and unpacked next to the data (restore-pending), and the
// panel puts it in place the next time it starts, before it opens the database, keeping what it
// replaces (before-restore-<time>).
package backup

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"filippo.io/age"
)

const (
	Format       = 1
	manifestName = "manifest.json"
	dbName       = "meridian.db"
	pendingDir   = "restore-pending"
	readyName    = "READY"

	maxDB      = 4 << 30   // the database
	maxFile    = 256 << 20 // any other file
	maxTotal   = 8 << 30   // everything unpacked
	maxEntries = 50000
)

// dirs are the folders of the data directory a backup carries besides the database.
var dirs = []string{"plugins", "plugin-data"}

// Info is a backup's manifest.
type Info struct {
	Format    int    `json:"format"`
	Version   string `json:"version"`    // the Meridian that made it
	Schema    int    `json:"schema"`     // its database's schema version
	CreatedAt int64  `json:"created_at"` // Unix seconds
	Panel     string `json:"panel"`      // the panel's public address, to tell backups of several panels apart
}

// Write writes a backup of dataDir to w. snapshot writes a consistent copy of the database to a new
// file at the path it is given (VACUUM INTO).
func Write(w io.Writer, dataDir string, info Info, snapshot func(path string) error) error {
	tmp, err := os.MkdirTemp(dataDir, ".backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, dbName)
	if err := snapshot(snap); err != nil {
		return fmt.Errorf("copying the database: %w", err)
	}
	info.Format = Format
	if info.CreatedAt == 0 {
		info.CreatedAt = time.Now().Unix()
	}
	z := zip.NewWriter(w)
	m, _ := json.MarshalIndent(info, "", "  ")
	if err := add(z, manifestName, bytes.NewReader(m), time.Unix(info.CreatedAt, 0)); err != nil {
		return err
	}
	if err := addFile(z, dbName, snap); err != nil {
		return err
	}
	for _, d := range dirs {
		root := filepath.Join(dataDir, d)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if !e.Type().IsRegular() { // folders come with their files; links and devices never
				return nil
			}
			rel, err := filepath.Rel(dataDir, p)
			if err != nil {
				return err
			}
			return addFile(z, filepath.ToSlash(rel), p)
		})
		if err != nil {
			return err
		}
	}
	return z.Close()
}

func add(z *zip.Writer, name string, r io.Reader, t time.Time) error {
	f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: t})
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	return err
}

func addFile(z *zip.Writer, name, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	h, err := zip.FileInfoHeader(st)
	if err != nil {
		return err
	}
	h.Name, h.Method = name, zip.Deflate
	mode := os.FileMode(0o600)
	if st.Mode()&0o100 != 0 { // a plugin's program stays a program
		mode = 0o700
	}
	h.SetMode(mode)
	out, err := z.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	return err
}

// ---------------------------------------------------------------- encryption

// Encrypt returns a writer that encrypts what is written with passphrase; Close finishes the file.
func Encrypt(w io.Writer, passphrase string) (io.WriteCloser, error) {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	return age.Encrypt(w, r)
}

// IsEncrypted says whether a file is an age file (what the start of it says).
func IsEncrypted(head []byte) bool { return bytes.HasPrefix(head, []byte("age-encryption.org/")) }

// ErrPassphrase: the passphrase does not open the backup.
var ErrPassphrase = errors.New("the passphrase does not open this backup")

// decryptTo decrypts src into a new file dst.
func decryptTo(src, dst, passphrase string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return err
	}
	id.SetMaxWorkFactor(22)
	r, err := age.Decrypt(bufio.NewReader(in), id)
	if err != nil {
		var nm *age.NoIdentityMatchError
		if errors.As(err, &nm) {
			return ErrPassphrase
		}
		return fmt.Errorf("this is not a backup the panel can open: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(r, maxTotal+1)); err != nil {
		out.Close()
		return fmt.Errorf("decrypting: %w", err)
	}
	return out.Close()
}

// ---------------------------------------------------------------- restore

// entryOK says whether a ZIP entry may be part of a backup: the manifest, the database, or a plain
// path inside one of the carried folders.
func entryOK(name string) bool {
	if name == manifestName || name == dbName {
		return true
	}
	if !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name ||
		!filepath.IsLocal(filepath.FromSlash(name)) {
		return false
	}
	top, _, ok := strings.Cut(name, "/")
	if !ok {
		return false
	}
	for _, d := range dirs {
		if top == d {
			return true
		}
	}
	return false
}

// Stage checks a backup (a ZIP, or an age file opened with passphrase) and unpacks it into
// dataDir/restore-pending; check is given the unpacked database to look at. What was staged before
// is replaced. The panel puts it in place when it next starts (ApplyPending).
func Stage(dataDir, file, passphrase string, check func(dbPath string) error) (Info, error) {
	var info Info
	head := make([]byte, 32)
	if f, err := os.Open(file); err == nil {
		n, _ := io.ReadFull(f, head)
		head = head[:n]
		f.Close()
	} else {
		return info, err
	}
	pending := filepath.Join(dataDir, pendingDir)
	if err := os.RemoveAll(pending); err != nil {
		return info, err
	}
	if err := os.Mkdir(pending, 0o700); err != nil {
		return info, err
	}
	fail := func(err error) (Info, error) {
		os.RemoveAll(pending)
		return Info{}, err
	}
	src := file
	if IsEncrypted(head) {
		if passphrase == "" {
			return fail(errors.New("this backup is encrypted - give its passphrase"))
		}
		src = filepath.Join(pending, ".backup.zip")
		if err := decryptTo(file, src, passphrase); err != nil {
			return fail(err)
		}
		defer os.Remove(src)
	} else if !bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		return fail(errors.New("this is not a Rosélune backup (neither a ZIP nor an encrypted backup)"))
	}
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fail(errors.New("this is not a Rosélune backup (the ZIP cannot be read)"))
	}
	defer zr.Close()
	if len(zr.File) > maxEntries {
		return fail(fmt.Errorf("the backup holds more than %d files", maxEntries))
	}
	var total uint64
	seen := map[string]bool{}
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") || f.Mode().IsDir() {
			continue
		}
		if !entryOK(name) || !f.Mode().IsRegular() || seen[name] {
			return fail(fmt.Errorf("the backup holds %q, which no Rosélune backup has", truncate(name, 80)))
		}
		seen[name] = true
		limit := uint64(maxFile)
		if name == dbName {
			limit = maxDB
		}
		if f.UncompressedSize64 > limit {
			return fail(fmt.Errorf("%s is too large for a backup", truncate(name, 80)))
		}
		if total += f.UncompressedSize64; total > maxTotal {
			return fail(errors.New("the backup is too large"))
		}
	}
	if !seen[manifestName] || !seen[dbName] {
		return fail(errors.New("this is not a Rosélune backup (its manifest or database is missing)"))
	}
	root, err := os.OpenRoot(pending)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") || f.Mode().IsDir() {
			continue
		}
		if err := unpack(root, f); err != nil {
			return fail(fmt.Errorf("unpacking %s: %w", truncate(f.Name, 80), err))
		}
	}
	m, err := os.ReadFile(filepath.Join(pending, manifestName))
	if err != nil || json.Unmarshal(m, &info) != nil || info.Format != Format {
		return fail(errors.New("this is not a Rosélune backup (its manifest cannot be read)"))
	}
	if err := check(filepath.Join(pending, dbName)); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(pending, readyName), m, 0o600); err != nil {
		return fail(err)
	}
	return info, nil
}

func unpack(root *os.Root, f *zip.File) error {
	if d := path.Dir(f.Name); d != "." {
		if err := root.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := os.FileMode(0o600)
	if f.Mode()&0o100 != 0 && f.Name != dbName {
		mode = 0o700
	}
	out, err := root.OpenFile(f.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	limit := int64(maxFile)
	if f.Name == dbName {
		limit = maxDB
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("larger than it says")
	}
	if err == nil {
		err = root.Chmod(f.Name, mode) // whatever the umask took away
	}
	return err
}

// Pending says what restore is staged, if any.
func Pending(dataDir string) (Info, bool) {
	var info Info
	m, err := os.ReadFile(filepath.Join(dataDir, pendingDir, readyName))
	if err != nil || json.Unmarshal(m, &info) != nil {
		return info, false
	}
	return info, true
}

// Discard drops a staged restore.
func Discard(dataDir string) error { return os.RemoveAll(filepath.Join(dataDir, pendingDir)) }

// ApplyPending puts a staged restore in place - run it before the database is opened, with the data
// directory locked. The database it replaces (with its write-ahead log) and the carried folders are
// kept in before-restore-<time>. It says what it restored; ok is false when nothing was staged.
func ApplyPending(dataDir string) (info Info, ok bool, err error) {
	info, ok = Pending(dataDir)
	if !ok {
		return info, false, nil
	}
	pending := filepath.Join(dataDir, pendingDir)
	keep := filepath.Join(dataDir, "before-restore-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.Mkdir(keep, 0o700); err != nil {
		return info, false, err
	}
	db := filepath.Join(dataDir, dbName)
	// every move, so a failure half way can put everything back where it was
	type move struct{ from, to string }
	var done []move
	mv := func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		done = append(done, move{from, to})
		return nil
	}
	undo := func(err error) (Info, bool, error) {
		for i := len(done) - 1; i >= 0; i-- {
			_ = os.Rename(done[i].to, done[i].from)
		}
		return info, false, fmt.Errorf("the staged restore could not be put in place (the panel keeps its data): %w", err)
	}
	for _, ext := range []string{"", "-wal", "-shm"} {
		if err := mv(db+ext, filepath.Join(keep, dbName+ext)); err != nil {
			return undo(err)
		}
	}
	for _, d := range dirs {
		if err := mv(filepath.Join(dataDir, d), filepath.Join(keep, d)); err != nil {
			return undo(err)
		}
	}
	if err := mv(filepath.Join(pending, dbName), db); err != nil {
		return undo(err)
	}
	if _, err := os.Stat(db); err != nil {
		return undo(errors.New("the staged database is missing"))
	}
	for _, d := range dirs {
		if err := mv(filepath.Join(pending, d), filepath.Join(dataDir, d)); err != nil {
			return undo(err)
		}
	}
	_ = os.Remove(db + "-journal")
	return info, true, os.RemoveAll(pending)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
