package service

import (
	"strings"
	"testing"
)

var hy2 = Spec{
	Name:        "meridian-hy2@",
	Description: "Meridian Hysteria2 node %i",
	Exec:        "/var/lib/meridian-agent/cores/hysteria/2.13.0/hysteria",
	Args:        []string{"server", "-c", "/etc/meridian-agent/hy2/%i.yaml"},
	Env:         map[string]string{"HYSTERIA_LOG_LEVEL": "debug", "HYSTERIA_LOG_FORMAT": "json"},
	Log:         "/run/meridian-agent/hy2-%i.log",
	Caps:        []string{"CAP_NET_ADMIN", "CAP_NET_BIND_SERVICE", "CAP_NET_RAW"},
	NoFile:      1048576,
	Sandbox:     true,
	RestartSec:  2,
}

func TestSystemdUnit(t *testing.T) {
	u := systemdUnit(hy2)
	for _, want := range []string{
		"Description=Meridian Hysteria2 node %i\n",
		"Environment=HYSTERIA_LOG_FORMAT=json\nEnvironment=HYSTERIA_LOG_LEVEL=debug\n",
		"ExecStart=/var/lib/meridian-agent/cores/hysteria/2.13.0/hysteria server -c /etc/meridian-agent/hy2/%i.yaml\n",
		"Restart=always\nRestartSec=2\nLimitNOFILE=1048576\n",
		"StandardOutput=append:/run/meridian-agent/hy2-%i.log\n",
		"CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW\nAmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW\nNoNewPrivileges=true\n",
		"ProtectSystem=full\nProtectHome=true\nPrivateTmp=true\n",
		"[Install]\nWantedBy=multi-user.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("missing %q in\n%s", want, u)
		}
	}
	if systemdName("meridian-hy2@") != "meridian-hy2@.service" || systemdName("meridian-xray") != "meridian-xray.service" ||
		systemdName("x-ui.service") != "x-ui.service" {
		t.Error("systemd names")
	}
	realm := systemdUnit(Spec{Name: "meridian-realm@", Exec: "/r", Unprivileged: true, Caps: []string{"CAP_NET_BIND_SERVICE"}})
	if !strings.Contains(realm, "DynamicUser=yes\n") || !strings.Contains(realm, "NoNewPrivileges=true\n") || strings.Contains(realm, "ProtectSystem") {
		t.Errorf("realm unit:\n%s", realm)
	}
}

func TestOpenRCScript(t *testing.T) {
	s := openrcScript(hy2)
	for _, want := range []string{
		"#!/sbin/openrc-run\n",
		`instance="${RC_SVCNAME#*.}"` + "\n",
		`description="Meridian Hysteria2 node ${instance}"` + "\n",
		"supervisor=supervise-daemon\n",
		`command="/usr/bin/env"` + "\n",
		`command_args="HYSTERIA_LOG_FORMAT=json HYSTERIA_LOG_LEVEL=debug /var/lib/meridian-agent/cores/hysteria/2.13.0/hysteria server -c /etc/meridian-agent/hy2/${instance}.yaml"` + "\n",
		`no_new_privs="yes"` + "\n",
		`rc_ulimit="-n 1048576"` + "\n",
		"respawn_delay=2\nrespawn_max=0\n",
		`output_log="/run/meridian-agent/hy2-${instance}.log"` + "\n",
		"depend() {\n\tneed net\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	agent := openrcScript(Spec{Name: "meridian-agent", Description: "Meridian agent", Exec: "/usr/local/bin/meridian-agent", Args: []string{"run"}, RestartSec: 3})
	if strings.Contains(agent, "instance=") || !strings.Contains(agent, `output_logger="logger -t meridian-agent -p daemon.info"`) ||
		strings.Contains(agent, "capabilities") || strings.Contains(agent, "command_user") {
		t.Errorf("agent script:\n%s", agent)
	}
	if strings.Contains(s, "capabilities=") {
		t.Errorf("a root service got capabilities (they would restrict nothing):\n%s", s)
	}
	realm := openrcScript(Spec{Name: "meridian-realm@", Exec: "/r", Unprivileged: true, Caps: []string{"CAP_NET_BIND_SERVICE"}})
	if !strings.Contains(realm, `command_user="nobody:nobody"`) || !strings.Contains(realm, `no_new_privs="yes"`) ||
		!strings.Contains(realm, `capabilities="^cap_net_bind_service"`) {
		t.Errorf("realm script:\n%s", realm)
	}
	// nothing in a value can break out of its quotes
	evil := openrcScript(Spec{Name: "x", Description: "a\"; rm -rf / #`id` $HOME", Exec: "/x"})
	if !strings.Contains(evil, `description="a\"; rm -rf / #\`+"`"+`id\`+"`"+` \$HOME"`) {
		t.Errorf("quoting:\n%s", evil)
	}
}

func TestOpenRCPaths(t *testing.T) {
	if openrcPath("meridian-hy2@") != "/etc/init.d/meridian-hy2" || openrcPath("meridian-hy2.22") != "/etc/init.d/meridian-hy2.22" ||
		openrcPath("meridian-agent") != "/etc/init.d/meridian-agent" {
		t.Error("OpenRC paths")
	}
}
