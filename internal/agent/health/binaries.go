package health

import (
	"path/filepath"
	"runtime"
	"strings"

	"meridian/internal/proto"
)

var coreLabels = map[string]string{"xray": "Xray", "hysteria": "Hysteria", "realm": "realm"}

// checkBinaries watches Meridian's own programs. The agent's file must be the program that runs (or
// the one the agent is upgrading itself to); a core's file must stay the one the agent installed, and
// Hysteria's must match the checksum the panel sent (Xray and realm come in archives, whose
// checksums the agent checked when it installed them).
func (sc *scan) checkBinaries() {
	m := sc.m
	sc.next.Cores = map[string]fileSum{}
	m.mu.Lock()
	if m.binWrong == nil {
		m.binWrong = map[string]int{}
	}
	running := m.selfSum
	m.mu.Unlock()
	if a := sc.own.Agent; a != "" {
		if s, ok := sumLarge(m.path(a), sc.prev.Cores[a], sc.at); ok {
			sc.next.Cores[a] = s
			wrong := running != "" && s.SHA != running && s.SHA != sc.prev.Expect
			m.mu.Lock()
			if wrong {
				m.binWrong[a]++
			} else {
				m.binWrong[a] = 0
			}
			n := m.binWrong[a]
			m.mu.Unlock()
			// a reinstall replaces the file and restarts the agent within seconds: only a file that
			// stays different from the running program for two scans counts
			if n == 2 {
				sc.event(proto.Finding{Key: "binary:agent", Kind: proto.FindBinary, Severity: proto.SevHigh,
					Title: "The agent's program file was replaced while it runs",
					Detail: a + " is not the program running now (SHA-256 " + short(s.SHA) + "…, running " + short(running) +
						"…), and the agent did not upgrade itself. Reinstall the agent with the command from the server page."})
			}
		}
	}
	for core, label := range coreLabels {
		paths, _ := filepath.Glob(m.path(filepath.Join(sc.own.Data, "cores", core, "*", core)))
		for _, full := range paths {
			ver := filepath.Base(filepath.Dir(full))
			if !versionLike(ver) { // "current" (a link to one of them), or an unfinished download
				continue
			}
			p := filepath.Join(sc.own.Data, "cores", core, ver, core)
			old, had := sc.prev.Cores[p]
			s, ok := sumLarge(full, old, sc.at)
			if !ok {
				continue
			}
			sc.next.Cores[p] = s
			if want := digest(sc.own.Digests[core+"/"+ver+"/"+core+"-linux-"+runtime.GOARCH]); core == "hysteria" && want != "" && s.SHA != want {
				sc.last(proto.Finding{Key: "binary:" + core + ":" + ver, Kind: proto.FindBinary, Severity: proto.SevCritical,
					Title: label + " " + ver + "'s program is not the published one",
					Detail: p + " does not match the checksum of the official release (SHA-256 " + short(s.SHA) + "…, published " +
						short(want) + "…). Upgrade Hysteria from the server page to install it again."})
				continue
			}
			if had && !sc.first && old.SHA != "" && old.SHA != s.SHA {
				sc.event(proto.Finding{Key: "binary:" + core + ":" + ver, Kind: proto.FindBinary, Severity: proto.SevHigh,
					Title: label + " " + ver + "'s program file changed",
					Detail: p + " is not the file the agent installed (SHA-256 " + short(s.SHA) + "…, was " + short(old.SHA) +
						"…). Upgrade " + label + " from the server page to install it again."})
			}
		}
	}
}

// digest is a checksum from the panel in the form hashFile gives ("" when it is not one).
func digest(v string) string {
	v = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(v), "sha256:"))
	if len(v) != 64 {
		return ""
	}
	return v
}

// versionLike: a folder named like a version (an upgrade's unfinished download is not one).
func versionLike(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return s != ""
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
