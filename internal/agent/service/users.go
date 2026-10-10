package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
)

// SoloAccount is the account users' own servers run as (mieru, Snell, AnyTLS: agent/solo): no
// password, no home, no shell, no rights. nftables keeps it from reaching this server itself and
// private networks.
const SoloAccount = "meridian-solo"

var systemUserRE = regexp.MustCompile(`^meridian-[a-z]{2,16}$`)

// EnsureSystemUser makes sure a system account for running services exists - no password, no home,
// no shell, a group of its own - and returns its user and group ids. Only the agent's own accounts
// (meridian-...) are made.
func EnsureSystemUser(name string) (uid, gid int, err error) {
	if !systemUserRE.MatchString(name) {
		return 0, 0, fmt.Errorf("not an account the agent makes: %q", name)
	}
	if u, err := user.Lookup(name); err == nil {
		return ids(u)
	}
	nologin := "/usr/sbin/nologin"
	for _, p := range []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false"} {
		if _, err := os.Stat(p); err == nil {
			nologin = p
			break
		}
	}
	var out []byte
	if path, lookErr := exec.LookPath("useradd"); lookErr == nil {
		out, err = exec.Command(path, "--system", "--no-create-home", "--home-dir", "/nonexistent", "--shell", nologin,
			"--user-group", name).CombinedOutput()
	} else if path, lookErr := exec.LookPath("adduser"); lookErr == nil { // Alpine (BusyBox)
		if g, gerr := exec.LookPath("addgroup"); gerr == nil {
			_ = exec.Command(g, "-S", name).Run()
		}
		out, err = exec.Command(path, "-S", "-D", "-H", "-h", "/nonexistent", "-s", nologin, "-G", name, name).CombinedOutput()
	} else {
		return 0, 0, fmt.Errorf("cannot make the system account %s: neither useradd nor adduser is here", name)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("making the system account %s: %v: %s", name, err, out)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	return ids(u)
}

func ids(u *user.User) (int, int, error) {
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil || uid == 0 || gid == 0 {
		return 0, 0, fmt.Errorf("the account %s has unusable ids (%s:%s)", u.Username, u.Uid, u.Gid)
	}
	return uid, gid, nil
}
