package scan

import (
	"testing"
)

func TestXrayConfig(t *testing.T) {
	ins, err := parseXray("testdata/xray.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 5 { // the API port is left out
		t.Fatalf("got %d inbounds: %+v", len(ins), ins)
	}
	r := ins[0]
	if r.Protocol != "vless" || r.Security != "reality" || r.Target != "www.microsoft.com:443" || r.SNI != "www.microsoft.com" ||
		r.PrivateKey == "" || len(r.ShortIDs) != 2 || len(r.Users) != 2 || r.Users[1].Name != "bob" || r.Users[0].Flow != "xtls-rprx-vision" {
		t.Errorf("reality: %+v", r)
	}
	if v := ins[1]; v.Port != 8080 || v.Transport != "ws" || v.Path != "/vm" || v.Host != "cdn.example.com" || v.Users[0].ID == "" {
		t.Errorf("vmess: %+v", v)
	}
	if s := ins[2]; s.Method != "2022-blake3-aes-128-gcm" || s.ServerKey == "" || s.Users[0].Password == "" {
		t.Errorf("shadowsocks: %+v", s)
	}
	if s := ins[3]; s.Protocol != "socks" || !s.UDP || s.Users[0].Username != "erin" {
		t.Errorf("socks: %+v", s)
	}
	if k := ins[4]; k.Note == "" {
		t.Errorf("kcp should carry a note: %+v", k)
	}
}

func TestSingBoxConfig(t *testing.T) {
	ins, err := parseSingBox("testdata/singbox.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(ins) != 2 {
		t.Fatalf("got %d: %+v", len(ins), ins)
	}
	if tj := ins[0]; tj.Protocol != "trojan" || tj.Transport != "grpc" || tj.Service != "tun" || tj.Security != "tls" ||
		tj.SNI != "proxy.example.com" || tj.Users[0].Password != "frank-pass" {
		t.Errorf("trojan: %+v", tj)
	}
	if hy := ins[1]; hy.Protocol != "hysteria2" || !hy.ACME || hy.SNI != "hy.example.com" || hy.ObfsPassword != "obfs-pw" {
		t.Errorf("hysteria2: %+v", hy)
	}
}

func TestHysteriaConfig(t *testing.T) {
	ins, err := parseHysteria("testdata/hysteria.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h := ins[0]
	if h.Port != 8443 || h.ObfsPassword != "sal-pw" || len(h.Users) != 2 || h.Users[0].Password != "henry:hpass" {
		t.Errorf("hysteria: %+v", h)
	}
}

func TestCommandLines(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "-c", "/etc/xray/config.json"}, "/etc/xray/config.json"},
		{[]string{"run", "-config=/a.json"}, "/a.json"},
		{[]string{"run", "-confdir", "/etc/xray/conf.d"}, "/etc/xray/conf.d"},
		{[]string{"run", "--config", "/b.json"}, "/b.json"},
	} {
		if got := configArg("xray", c.args); got != c.want {
			t.Errorf("configArg(%v) = %q", c.args, got)
		}
	}
	if softwareOf("xray") != "xray" || softwareOf("sing-box") != "sing-box" || softwareOf("bash") != "" {
		t.Error("softwareOf")
	}
}

func TestSoftwareOf(t *testing.T) {
	for exe, want := range map[string]string{
		"xray": "xray", "xray-linux-amd64": "xray", "Xray": "xray", "xray-foreign": "xray", "v2ray": "v2ray",
		"sing-box": "sing-box", "sing-box-1.12.4": "sing-box", "hysteria": "hysteria", "hysteria-linux-arm64": "hysteria",
		"nginx": "", "bash": "", "sshd": "",
	} {
		if got := softwareOf(exe); got != want {
			t.Errorf("%s: got %q, want %q", exe, got, want)
		}
	}
}
