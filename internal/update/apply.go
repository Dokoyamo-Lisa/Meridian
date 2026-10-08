package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Where install-panel.sh puts the panel. The updater keeps the version it replaces next to them, to
// put it back when the new one does not stay up.
var (
	binPath  = "/usr/local/bin/meridian"
	libDir   = "/usr/local/lib/meridian"
	unitName = "meridian"
)

// Apply is the updater service's work, as root: it takes the request the panel left in
// dataDir/update, checks the release again - its signature against the keys built into this
// binary, its checksum, that it is newer than current - installs it with its own
// install-panel.sh --upgrade, makes sure the new panel stays up (or puts the old one back), and leaves
// the result for the panel.
func Apply(dataDir, current, arch string) error {
	if os.Geteuid() != 0 {
		return errors.New("the updater runs as root (systemd starts it: meridian-update.service)")
	}
	dir := filepath.Join(dataDir, "update")
	raw, err := readFile(filepath.Join(dir, RequestFile), maxSmall)
	if errors.Is(err, os.ErrNotExist) {
		return nil // nothing was asked for
	} else if err != nil {
		return err
	}
	// taken: the path unit must not start the updater again for the same request
	_ = os.Remove(filepath.Join(dir, RequestFile))
	res := Result{From: current}
	defer func() {
		res.At = time.Now().Unix()
		writeResult(dir, res)
		clean(dir)
	}()
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		res.Error = "the update request cannot be read"
		return errors.New(res.Error)
	}
	res.To, res.Agents = req.Version, req.Agents
	if err := install(dir, req, current, arch); err != nil {
		res.Error = err.Error()
		return err
	}
	res.OK = true
	return nil
}

func install(dir string, req Request, current, arch string) error {
	if !Newer(req.Version, current) {
		return fmt.Errorf("%s is not newer than the running %s - nothing was installed", req.Version, current)
	}
	if req.Arch != arch {
		return fmt.Errorf("the release is for %s, this server is %s", req.Arch, arch)
	}
	name := ArchiveName(req.Version, arch)
	sums, err := readFile(filepath.Join(dir, sumsFile), maxSmall)
	if err != nil {
		return err
	}
	sig, err := readFile(filepath.Join(dir, sigFile), maxSmall)
	if err != nil {
		return err
	}
	if err := Verify(sums, sig); err != nil {
		return err
	}
	archive, err := readFile(filepath.Join(dir, name), maxArchive)
	if err != nil {
		return err
	}
	if err := checkSum(sums, name, archive); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "meridian-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	top := strings.TrimSuffix(name, ".tar.gz")
	if err := extract(archive, top, tmp); err != nil {
		return fmt.Errorf("unpacking the release: %w", err)
	}
	rel := filepath.Join(tmp, top)
	out, err := exec.Command(filepath.Join(rel, "meridian"), "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "meridian "+req.Version {
		return fmt.Errorf("the release's panel does not run here (%s)", strings.TrimSpace(string(out)))
	}
	agentDir := os.Getenv("MERIDIAN_AGENT_DIR")
	if agentDir == "" {
		agentDir = filepath.Join(libDir, "agent")
	}
	prev := filepath.Join(libDir, "previous")
	if err := keep(prev, agentDir); err != nil {
		return fmt.Errorf("keeping the running version: %w", err)
	}
	before := restarts()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "./install-panel.sh", "--upgrade")
	cmd.Dir = rel
	if out, err := cmd.CombinedOutput(); err != nil {
		putBack(prev, agentDir)
		return fmt.Errorf("installing %s failed (the previous version runs again): %s", req.Version, lastLine(string(out)))
	}
	if !stays(before) {
		putBack(prev, agentDir)
		return fmt.Errorf("the new version (%s) did not stay up - the previous version was put back (journalctl -u meridian shows why)", req.Version)
	}
	return nil
}

// restarts is how often systemd has restarted the panel.
func restarts() string {
	out, _ := exec.Command("systemctl", "show", "-p", "NRestarts", "--value", unitName).Output()
	return strings.TrimSpace(string(out))
}

// stays watches the restarted panel for a while: it must be running, and not crashing over and over.
func stays(before string) bool {
	time.Sleep(3 * time.Second)
	first := restarts()
	for i := 0; i < 6; i++ {
		if exec.Command("systemctl", "is-active", "--quiet", unitName).Run() != nil {
			return false
		}
		time.Sleep(2 * time.Second)
	}
	return restarts() == first
}

// keep copies the running panel and agents into prev.
func keep(prev, agentDir string) error {
	if err := os.RemoveAll(prev); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(prev, "agent"), 0o755); err != nil {
		return err
	}
	if err := copyFile(binPath, filepath.Join(prev, "meridian"), 0o755); err != nil {
		return err
	}
	for _, a := range []string{"amd64", "arm64"} {
		n := "meridian-agent-linux-" + a
		if err := copyFile(filepath.Join(agentDir, n), filepath.Join(prev, "agent", n), 0o644); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// putBack restores what keep saved and restarts the panel.
func putBack(prev, agentDir string) {
	_ = copyFile(filepath.Join(prev, "meridian"), binPath, 0o755)
	for _, a := range []string{"amd64", "arm64"} {
		n := "meridian-agent-linux-" + a
		_ = copyFile(filepath.Join(prev, "agent", n), filepath.Join(agentDir, n), 0o644)
	}
	_ = exec.Command("systemctl", "restart", unitName).Run()
}

// copyFile replaces dst with src atomically.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// readFile reads a file the panel left, never through a link, up to limit bytes.
func readFile(p string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plain file", filepath.Base(p))
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", filepath.Base(p))
	}
	return b, nil
}

// writeResult leaves the result for the panel, owned by the panel's user (the directory's owner).
func writeResult(dir string, res Result) {
	b, _ := json.Marshal(res)
	p := filepath.Join(dir, ResultFile)
	_ = os.Remove(p)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(b)
	f.Close()
	if st, err := os.Stat(dir); err == nil {
		if s, ok := st.Sys().(*syscall.Stat_t); ok {
			_ = os.Lchown(p, int(s.Uid), int(s.Gid))
		}
	}
}

// clean removes the downloaded release from the update directory.
func clean(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		n := e.Name()
		if n == sumsFile || n == sigFile || strings.HasPrefix(n, ".tmp-") ||
			(strings.HasPrefix(n, "meridian-") && strings.HasSuffix(n, ".tar.gz")) {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
