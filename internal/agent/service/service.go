// Package service runs the services the agent owns under the host's init system: systemd, or
// OpenRC (Alpine Linux). Cores run as their own services, so restarting or upgrading the agent never
// touches live traffic.
//
// A service is described once (Spec) and written as a systemd unit or an OpenRC script. A template
// ("meridian-hy2@") runs one instance per id: a systemd template unit (meridian-hy2@22.service) or
// an OpenRC script with one symlink per instance (meridian-hy2.22).
package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Spec describes a service. In Args and Env, "%i" stands for the instance of a template.
type Spec struct {
	Name         string            // "meridian-xray", or a template ending in "@": "meridian-hy2@"
	Description  string            // "Meridian Xray"; a template's may contain %i
	Exec         string            // absolute path of the program
	Args         []string          // its arguments
	Env          map[string]string // its environment
	Caps         []string          // capabilities it keeps (CAP_NET_BIND_SERVICE, ...); empty = those of root
	NoFile       int               // open files limit (0 = the default)
	Unprivileged bool              // run as a throwaway unprivileged user (systemd DynamicUser; nobody on OpenRC)
	Log          string            // a file both output streams are appended to ("" = the system log)
	Sandbox      bool              // systemd: ProtectSystem=full, ProtectHome, PrivateTmp
	RestartSec   int               // pause before a restart after a crash (default 2)
}

// Init is "systemd", "openrc" or "" (neither: the agent cannot run its services here).
func Init() string {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	if _, err := os.Stat("/run/openrc"); err == nil {
		if _, err := os.Stat("/sbin/openrc-run"); err == nil {
			return "openrc"
		}
	}
	return ""
}

// Available reports whether the agent can run services on this host.
func Available() bool { return Init() != "" }

// Instance is the name of one instance of a template: Instance("meridian-hy2@", 22).
func Instance(template string, id int64) string {
	base := strings.TrimSuffix(template, "@")
	if Init() == "openrc" {
		return base + "." + strconv.FormatInt(id, 10)
	}
	return base + "@" + strconv.FormatInt(id, 10)
}

// Logs says where a service's output can be read, for messages to people.
func Logs(name string) string {
	if Init() == "openrc" {
		return "/var/log/messages (grep " + name + ")"
	}
	return "journalctl -u " + name + " -f"
}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, s)
	}
	return s, nil
}

// writeFile replaces path with content atomically; it reports whether the content changed.
func writeFile(path, content string, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(content)) {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm() == mode {
			return false, nil
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}

// Define writes a service or a template; it reports whether anything changed.
func Define(s Spec) (bool, error) {
	if s.RestartSec == 0 {
		s.RestartSec = 2
	}
	if Init() == "openrc" {
		changed, err := writeFile(openrcPath(s.Name), openrcScript(s), 0o755)
		if changed && err == nil {
			refreshDeps()
		}
		return changed, err
	}
	changed, err := writeFile(filepath.Join(systemdDir, systemdName(s.Name)), systemdUnit(s), 0o644)
	if err != nil || !changed {
		return changed, err
	}
	_, err = run("systemctl", "daemon-reload")
	return true, err
}

// Undefine removes a service's or a template's definition (stop its instances first).
func Undefine(name string) {
	if Init() == "openrc" {
		os.Remove(openrcPath(name))
		return
	}
	os.Remove(filepath.Join(systemdDir, systemdName(name)))
	_, _ = run("systemctl", "daemon-reload")
}

// EnableNow starts a service (or instance) now and at every boot.
func EnableNow(name string) error {
	if Init() == "openrc" {
		if err := linkInstance(name); err != nil {
			return err
		}
		if _, err := run("rc-update", "add", name, "default"); err != nil {
			return err
		}
		refreshDeps()
		// started, but its process died without supervise-daemon starting it again: "start" would do
		// nothing, so it starts afresh (it was down anyway - nobody is disconnected by this)
		if _, err := os.Lstat(filepath.Join("/run/openrc/started", name)); err == nil && !IsActive(name) {
			_, err := run("rc-service", name, "restart")
			return err
		}
		_, err := run("rc-service", name, "start")
		return err
	}
	_, err := run("systemctl", "enable", "--now", systemdName(name))
	return err
}

// refreshDeps rebuilds OpenRC's dependency cache. OpenRC notices new scripts by their times, to the
// second: a service written in the same second as the cache would be left out of it (and of
// rc-status) until the next boot.
func refreshDeps() { _, _ = run("rc-update", "-u") }

func Restart(name string) error {
	if Init() == "openrc" {
		_, err := run("rc-service", name, "restart")
		return err
	}
	_, err := run("systemctl", "restart", systemdName(name))
	return err
}

func Stop(name string) error {
	if Init() == "openrc" {
		_, err := run("rc-service", name, "stop")
		return err
	}
	_, err := run("systemctl", "stop", systemdName(name))
	return err
}

// DisableNow stops a service and keeps it from starting at boot.
func DisableNow(name string) error {
	if Init() == "openrc" {
		_, err := run("rc-service", name, "stop")
		_, _ = run("rc-update", "del", name, "default")
		return err
	}
	_, err := run("systemctl", "disable", "--now", systemdName(name))
	return err
}

// Remove stops a service or instance, keeps it from starting at boot and, for an instance, removes
// it (the template stays).
func Remove(name string) {
	_ = DisableNow(name)
	if Init() == "openrc" && strings.Contains(name, ".") {
		os.Remove(openrcPath(name))
	}
}

// IsActive reports whether a service is running.
func IsActive(name string) bool {
	if Init() == "openrc" {
		if _, err := os.Lstat(filepath.Join("/run/openrc/started", name)); err != nil {
			return false
		}
		pid, _ := Status(name)
		return pid > 0
	}
	out, _ := run("systemctl", "is-active", systemdName(name))
	return out == "active"
}

// Status returns a running service's main PID and when it started.
func Status(name string) (pid int, since int64) {
	if Init() == "openrc" {
		b, err := os.ReadFile(filepath.Join("/run/openrc/options", name, "child_pid"))
		if err != nil {
			return 0, 0
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		if !alive(pid) {
			return 0, 0
		}
		return pid, processStart(pid)
	}
	out, err := run("systemctl", "show", systemdName(name), "-p", "MainPID", "-p", "ActiveEnterTimestamp")
	if err != nil {
		return 0, 0
	}
	for _, l := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "MainPID":
			pid, _ = strconv.Atoi(v)
		case "ActiveEnterTimestamp":
			for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "Mon 2006-01-02 15:04:05 -0700"} {
				if t, err := time.Parse(layout, v); err == nil {
					since = t.Unix()
					break
				}
			}
		}
	}
	return pid, since
}

// ---------------------------------------------------------------- systemd

const systemdDir = "/etc/systemd/system"

// systemdName adds ".service": "meridian-hy2@22" -> "meridian-hy2@22.service", "meridian-hy2@" ->
// "meridian-hy2@.service".
func systemdName(name string) string {
	if strings.HasSuffix(name, ".service") {
		return name
	}
	return name + ".service"
}

func systemdUnit(s Spec) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...); b.WriteByte('\n') }
	w("[Unit]")
	w("Description=%s", s.Description)
	w("After=network-online.target")
	w("Wants=network-online.target")
	w("StartLimitIntervalSec=0")
	w("")
	w("[Service]")
	for _, k := range sortedKeys(s.Env) {
		w("Environment=%s=%s", k, s.Env[k])
	}
	w("ExecStart=%s", strings.Join(append([]string{s.Exec}, s.Args...), " "))
	w("Restart=always")
	w("RestartSec=%d", s.RestartSec)
	if s.NoFile > 0 {
		w("LimitNOFILE=%d", s.NoFile)
	}
	if s.Log != "" {
		w("StandardOutput=append:%s", s.Log)
		w("StandardError=append:%s", s.Log)
	}
	if s.Unprivileged {
		w("DynamicUser=yes")
	}
	if len(s.Caps) > 0 {
		caps := strings.Join(s.Caps, " ")
		w("CapabilityBoundingSet=%s", caps)
		w("AmbientCapabilities=%s", caps)
	}
	if len(s.Caps) > 0 || s.Unprivileged {
		w("NoNewPrivileges=true")
	}
	if s.Sandbox {
		w("ProtectSystem=full")
		w("ProtectHome=true")
		w("PrivateTmp=true")
	}
	w("")
	w("[Install]")
	w("WantedBy=multi-user.target")
	return b.String()
}

// ---------------------------------------------------------------- OpenRC

const openrcDir = "/etc/init.d"

// openrcPath: a template "meridian-hy2@" is the script "meridian-hy2"; an instance
// "meridian-hy2.22" is a symlink to it.
func openrcPath(name string) string {
	return filepath.Join(openrcDir, strings.TrimSuffix(name, "@"))
}

// linkInstance creates the symlink an OpenRC instance runs as ("meridian-hy2.22" -> "meridian-hy2").
func linkInstance(name string) error {
	base, _, ok := strings.Cut(name, ".")
	if !ok {
		return nil
	}
	path := openrcPath(name)
	if target, err := os.Readlink(path); err == nil && target == base {
		return nil
	}
	os.Remove(path)
	return os.Symlink(base, path)
}

// openrcQuote quotes a value for an openrc-run script (POSIX sh, double quotes).
func openrcQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	return `"` + r.Replace(s) + `"`
}

// openrcScript renders a supervised OpenRC service. supervise-daemon restarts it after a crash; the
// output goes to the system log (or to Spec.Log); an instance takes its id from its name. env runs
// the program in place, so the supervised process is the program itself. The arguments are paths
// and words the agent chose (no spaces or shell characters); they are quoted anyway.
func openrcScript(s Spec) string {
	inst := func(v string) string { return strings.ReplaceAll(v, "%i", "${instance}") }
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...); b.WriteByte('\n') }
	w("#!/sbin/openrc-run")
	w("# written by meridian-agent - changes are overwritten")
	if strings.HasSuffix(s.Name, "@") {
		w(`instance="${RC_SVCNAME#*.}"`)
	}
	w("description=%s", inst(openrcQuote(s.Description)))
	w("supervisor=supervise-daemon")
	args := []string{}
	for _, k := range sortedKeys(s.Env) {
		args = append(args, k+"="+s.Env[k])
	}
	args = append(append(args, s.Exec), s.Args...)
	w(`command="/usr/bin/env"`)
	w("command_args=%s", inst(openrcQuote(strings.Join(args, " "))))
	if s.Unprivileged {
		w(`command_user="nobody:nobody"`)
	}
	// as root, OpenRC's capabilities add without taking anything away, so root services run as root
	// with no_new_privs; an unprivileged user gets exactly the capabilities it needs
	if s.Unprivileged && len(s.Caps) > 0 {
		caps := make([]string, len(s.Caps))
		for i, c := range s.Caps {
			caps[i] = "^" + strings.ToLower(c)
		}
		w("capabilities=%s", openrcQuote(strings.Join(caps, ",")))
	}
	if len(s.Caps) > 0 || s.Unprivileged {
		w(`no_new_privs="yes"`)
	}
	if s.NoFile > 0 {
		w(`rc_ulimit="-n %d"`, s.NoFile)
	}
	w("respawn_delay=%d", s.RestartSec)
	w("respawn_max=0")
	if s.Log != "" {
		w("output_log=%s", inst(openrcQuote(s.Log)))
		w("error_log=%s", inst(openrcQuote(s.Log)))
	} else {
		name := strings.TrimSuffix(s.Name, "@")
		w(`output_logger="logger -t %s -p daemon.info"`, name)
		w(`error_logger="logger -t %s -p daemon.info"`, name) // Go programs log to stderr, errors or not
	}
	w("")
	w("depend() {")
	w("\tneed net")
	w("\tafter firewall")
	w("}")
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// alive says whether a process runs: it exists and is not a zombie - a dead child its supervisor
// has not reaped (OpenRC's supervise-daemon can keep one around without starting a new process, and
// signal 0 still reaches a zombie).
func alive(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')') // the command can contain spaces and parentheses
	if i < 0 || i+2 >= len(s) {
		return true
	}
	switch s[i+2] {
	case 'Z', 'X', 'x':
		return false
	}
	return true
}

// processStart is when a process started (Unix seconds), from /proc.
func processStart(pid int) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// the command can contain spaces and parentheses: fields start after the last ')'
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0
	}
	ticks, err := strconv.ParseInt(f[19], 10, 64) // field 22: starttime, in clock ticks since boot
	if err != nil {
		return 0
	}
	boot := bootTime()
	if boot == 0 {
		return 0
	}
	return boot + ticks/100 // USER_HZ is 100 on Linux
}

func bootTime() int64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			t, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return t
		}
	}
	return 0
}
