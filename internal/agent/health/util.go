package health

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// fileSum is what the check knows of a file: enough to tell it changed without reading it again
// (large files are re-read only when these change, and once a day anyway).
type fileSum struct {
	Size  int64    `json:"size"`
	Mod   int64    `json:"mod"` // modification time, ns
	Chg   int64    `json:"chg"` // status change time, ns (Linux)
	Inode uint64   `json:"inode"`
	SHA   string   `json:"sha"`
	At    int64    `json:"at"`              // when SHA was computed
	Lines []string `json:"lines,omitempty"` // short digests of the lines that count (not comments or blank lines)
}

const (
	maxSmall  = 1 << 20 // watched files are read whole up to this size
	maxLines  = 500
	rehashAge = 24 * time.Hour
	hashRate  = 32 << 20 // bytes per second the check reads a large file at, at most
)

// readSmall reads a regular file of at most limit bytes (an error when it is larger). It never
// waits on a FIFO or a device someone put where a file belongs (opened without blocking, then checked).
func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("too large")
	}
	return b, nil
}

// cleanText keeps text from the server short and printable: it ends up in the panel and in
// notifications.
func cleanText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > n {
		cut := n
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func stamp(fi os.FileInfo) fileSum {
	return fileSum{Size: fi.Size(), Mod: fi.ModTime().UnixNano(), Chg: changeTime(fi), Inode: inode(fi)}
}

// sumSmall hashes a small watched file and, with lines, the digests of its lines. It returns the
// content too (for ld.so.preload). A file larger than maxSmall gets its hash only.
func sumSmall(path string, fi os.FileInfo, lines bool) (fileSum, []byte) {
	s := stamp(fi)
	if fi.Size() > maxSmall {
		s.SHA, _ = hashFile(path, true)
		return s, nil
	}
	b, err := readSmall(path, maxSmall)
	if err != nil {
		return s, nil
	}
	sum := sha256.Sum256(b)
	s.SHA = hex.EncodeToString(sum[:])
	if lines {
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.Join(strings.Fields(l), " ")
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			d := sha256.Sum256([]byte(l))
			s.Lines = append(s.Lines, hex.EncodeToString(d[:6]))
			if len(s.Lines) == maxLines {
				break
			}
		}
	}
	return s, b
}

// hashFile is the SHA-256 of a file. slow reads it at hashRate at most (large programs).
func hashFile(path string, slow bool) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	var r io.Reader = f
	if slow {
		r = &paced{r: f, start: time.Now()}
	}
	if _, err := io.Copy(h, io.LimitReader(r, 512<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// paced reads no faster than hashRate, so hashing a large program never hogs the disk.
type paced struct {
	r     io.Reader
	start time.Time
	n     int64
}

func (p *paced) Read(b []byte) (int, error) {
	if len(b) > 1<<20 {
		b = b[:1<<20]
	}
	n, err := p.r.Read(b)
	p.n += int64(n)
	if ahead := time.Duration(float64(p.n)/hashRate*float64(time.Second)) - time.Since(p.start); ahead > 0 {
		time.Sleep(ahead)
	}
	return n, err
}

// sumLarge hashes a program, reusing the last hash while the file looks the same and is less than
// a day old.
func sumLarge(path string, old fileSum, now time.Time) (fileSum, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return fileSum{}, false
	}
	s := stamp(fi)
	if old.SHA != "" && old.Size == s.Size && old.Mod == s.Mod && old.Chg == s.Chg && old.Inode == s.Inode &&
		now.Sub(time.Unix(old.At, 0)) < rehashAge {
		s.SHA, s.At = old.SHA, old.At
		return s, true
	}
	sha, err := hashFile(path, true)
	if err != nil {
		return fileSum{}, false
	}
	s.SHA, s.At = sha, now.Unix()
	return s, true
}
