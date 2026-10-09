package sys

import (
	"net"
	"os"
	"strings"
	"testing"
)

func TestEchoParsers(t *testing.T) {
	trace := "fl=1f1\nh=1.1.1.1\nip=203.0.113.7\nts=1791370000.1\nvisit_scheme=https\n"
	bodies := []string{trace, `{"ipAddress":"203.0.113.7","continentCode":"NA","countryCode":"US","city":"Los Angeles"}`}
	if len(echoServices) != 2 || !strings.Contains(echoServices[0].url, "/cdn-cgi/trace") || !strings.Contains(echoServices[1].url, "db-ip.com") {
		t.Fatalf("Cloudflare's trace is asked first, DB-IP second: %v", echoServices)
	}
	for i, s := range echoServices {
		if s.url[:8] != "https://" {
			t.Errorf("%s is not HTTPS", s.url)
		}
		if got := s.parse([]byte(bodies[i])); got != "203.0.113.7" {
			t.Errorf("%s: %q", s.url, got)
		}
	}
	for _, s := range echoServices6 {
		if s.url[:8] != "https://" {
			t.Errorf("%s is not HTTPS", s.url)
		}
		if got := s.parse([]byte(strings.Replace(trace, "203.0.113.7", "2001:db8::7", 1))); got != "2001:db8::7" {
			t.Errorf("%s: %q", s.url, got)
		}
	}
	if !isPublic(net.ParseIP("203.0.113.7")) || isPublic(net.ParseIP("10.1.2.3")) || isPublic(net.ParseIP("100.64.1.1")) ||
		isPublic(net.ParseIP("fd00::1")) || !isPublic(net.ParseIP("2001:db8::1")) {
		t.Error("isPublic")
	}
}

// TestLookupPublicLive asks the real services; set MERIDIAN_NET_TEST=1 to run it.
func TestLookupPublicLive(t *testing.T) {
	if os.Getenv("MERIDIAN_NET_TEST") == "" {
		t.Skip("set MERIDIAN_NET_TEST=1 to ask the real echo services")
	}
	for _, s := range echoServices {
		if ip := echoIP(t.Context(), s, false); ip == "" {
			t.Errorf("%s gave no address", s.url)
		} else {
			t.Logf("%s: %s", s.url, ip)
		}
	}
}

func TestWholeDiskNames(t *testing.T) {
	for name, want := range map[string]bool{"sda": true, "sda1": false, "vda": true, "vdb2": false, "xvda": true, "nvme0n1": true, "nvme0n1p2": false,
		"mmcblk0": true, "mmcblk0p1": false, "loop0": false, "dm-0": false, "md0": false, "sr0": false, "zram0": false} {
		if wholeDisk(name) != want {
			t.Errorf("%s: %v", name, !want)
		}
	}
}

func TestDetectVirt(t *testing.T) {
	cases := []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"/proc/vz": "", "/proc/cpuinfo": "flags\t: fpu hypervisor"}, "openvz"},
		{map[string]string{"/run/systemd/container": "lxc\n"}, "lxc"},
		{map[string]string{"/proc/1/environ": "PATH=/bin\x00container=podman\x00"}, "podman"},
		{map[string]string{"/.dockerenv": ""}, "docker"},
		{map[string]string{"/sys/class/dmi/id/sys_vendor": "QEMU\n", "/sys/class/dmi/id/product_name": "Standard PC (i440FX + PIIX, 1996)\n"}, "kvm"},
		{map[string]string{"/sys/class/dmi/id/sys_vendor": "Microsoft Corporation", "/sys/class/dmi/id/product_name": "Virtual Machine"}, "hyper-v"},
		{map[string]string{"/sys/class/dmi/id/sys_vendor": "Microsoft Corporation", "/sys/class/dmi/id/product_name": "Surface Laptop",
			"/proc/cpuinfo": "processor\t: 0\nflags\t\t: fpu vme sse2\n"}, "none"},
		{map[string]string{"/sys/class/dmi/id/sys_vendor": "Apple Inc.", "/sys/class/dmi/id/product_name": "Apple Virtualization Generic Platform"}, "apple"},
		{map[string]string{"/proc/cpuinfo": "flags\t\t: fpu hypervisor sse2\n"}, "vm"},
		{map[string]string{"/run/systemd/container": "<script>"}, ""},
		{map[string]string{}, ""},
	}
	for _, c := range cases {
		read := func(p string) string { return c.files[p] }
		exists := func(p string) bool { _, ok := c.files[p]; return ok }
		if got := detectVirt(read, exists); got != c.want {
			t.Errorf("%v: %q, want %q", c.files, got, c.want)
		}
	}
}
