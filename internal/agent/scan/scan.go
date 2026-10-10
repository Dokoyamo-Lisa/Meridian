// Package scan finds proxy software that already runs on a server - Xray, V2Ray (including the
// configs 3x-ui and x-ui generate), sing-box and Hysteria2 - and reads their inbounds, users
// included, so the panel can import them with the same credentials. It only reads: nothing is
// stopped or changed here.
package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Found is one piece of proxy software.
type Found struct {
	Software string    `json:"software"` // xray | v2ray | sing-box | hysteria
	Config   string    `json:"config"`   // file or directory it was read from
	Unit     string    `json:"unit,omitempty"`
	Running  bool      `json:"running"`
	Inbounds []Inbound `json:"inbounds"`
	Error    string    `json:"error,omitempty"`
}

// Inbound is one detected inbound, in the panel's terms.
type Inbound struct {
	Tag       string `json:"tag"`
	Protocol  string `json:"protocol"` // vless | vmess | trojan | shadowsocks | hysteria2 | socks | http | other
	Listen    string `json:"listen,omitempty"`
	Port      int    `json:"port"`
	Transport string `json:"transport,omitempty"` // raw | ws | grpc | httpupgrade | xhttp
	Path      string `json:"path,omitempty"`
	Host      string `json:"host,omitempty"`
	Service   string `json:"service_name,omitempty"`
	XHTTPMode string `json:"xhttp_mode,omitempty"`
	Security  string `json:"security,omitempty"` // none | tls | reality
	SNI       string `json:"sni,omitempty"`
	// REALITY
	Target     string   `json:"target,omitempty"`
	PrivateKey string   `json:"private_key,omitempty"`
	ShortIDs   []string `json:"short_ids,omitempty"`
	// TLS (and Hysteria2)
	CertPEM string `json:"cert_pem,omitempty"`
	KeyPEM  string `json:"key_pem,omitempty"`
	ACME    bool   `json:"acme,omitempty"`
	// Shadowsocks
	Method    string `json:"method,omitempty"`
	ServerKey string `json:"server_key,omitempty"`
	// VLESS Encryption: the inbound's "decryption" (with its private key), "" when none
	Decryption string `json:"decryption,omitempty"`
	// Hysteria2
	ObfsPassword string `json:"obfs_password,omitempty"`
	UDP          bool   `json:"udp,omitempty"`
	Users        []User `json:"users"`
	Note         string `json:"note,omitempty"` // why it cannot be imported as is
}

// User is a detected account.
type User struct {
	Name     string `json:"name"`
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	Username string `json:"username,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Method   string `json:"method,omitempty"`
}

// Result is a whole scan.
type Result struct {
	Found []Found `json:"found"`
}

const maxFile = 8 << 20

// knownConfigs are where these programs keep their configuration by default.
var knownConfigs = map[string][]string{
	"xray":     {"/usr/local/etc/xray/config.json", "/etc/xray/config.json", "/usr/local/x-ui/bin/config.json"},
	"v2ray":    {"/usr/local/etc/v2ray/config.json", "/etc/v2ray/config.json"},
	"sing-box": {"/etc/sing-box/config.json", "/usr/local/etc/sing-box/config.json"},
	"hysteria": {"/etc/hysteria/config.yaml", "/etc/hysteria/config.yml", "/etc/hysteria/config.json"},
}

// Run scans the host. own lists the units this agent runs, which are skipped.
func Run(own func(unit string) bool) Result {
	var res Result
	seen := map[string]bool{}
	add := func(f Found) {
		if seen[f.Config] {
			return
		}
		seen[f.Config] = true
		res.Found = append(res.Found, f)
	}
	// running processes first: they say exactly which config is in use
	for _, p := range processes() {
		if own(p.unit) || strings.HasPrefix(p.config, "/etc/meridian-agent") {
			continue
		}
		f := parse(p.software, p.config)
		f.Unit, f.Running = p.unit, true
		add(f)
	}
	// then the usual places, for software that is installed but stopped
	names := make([]string, 0, len(knownConfigs))
	for n := range knownConfigs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, sw := range names {
		for _, path := range knownConfigs[sw] {
			if _, err := os.Stat(path); err != nil || seen[path] {
				continue
			}
			add(parse(sw, path))
		}
	}
	return res
}

type proc struct {
	software, config, unit string
}

// unitRE finds the service a process runs in from its cgroup: a systemd unit ("…/x-ui.service") or an
// OpenRC service ("/openrc.x-ui")
var unitRE = regexp.MustCompile(`([A-Za-z0-9@_.:-]+\.service)|/openrc\.([A-Za-z0-9@_.:-]+)`)

// processes lists running proxy programs with their config and systemd unit.
func processes() []proc {
	var out []proc
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	for _, d := range dirs {
		raw, err := os.ReadFile(filepath.Join(d, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		sw := softwareOf(filepath.Base(args[0]))
		if sw == "" {
			continue
		}
		cfg := configArg(sw, args[1:])
		if cfg == "" {
			continue
		}
		if !filepath.IsAbs(cfg) {
			if cwd, err := os.Readlink(filepath.Join(d, "cwd")); err == nil {
				cfg = filepath.Join(cwd, cfg)
			}
		}
		unit := ""
		if cg, err := os.ReadFile(filepath.Join(d, "cgroup")); err == nil {
			if m := unitRE.FindStringSubmatch(string(cg)); m != nil {
				unit = m[1] + m[2]
			}
		}
		out = append(out, proc{software: sw, config: filepath.Clean(cfg), unit: unit})
	}
	return out
}

// softwareOf names the proxy program an executable is. Panels and scripts often rename the
// binaries (xray-linux-amd64, Xray, xray_core, sing-box-1.12), so the name only has to start right.
func softwareOf(exe string) string {
	e := strings.ToLower(exe)
	switch {
	case strings.HasPrefix(e, "xray"):
		return "xray"
	case strings.HasPrefix(e, "v2ray"):
		return "v2ray"
	case strings.HasPrefix(e, "sing-box"), strings.HasPrefix(e, "singbox"):
		return "sing-box"
	case strings.HasPrefix(e, "hysteria"):
		return "hysteria"
	}
	return ""
}

// configArg finds the config file or directory on a command line.
func configArg(sw string, args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, flag := range []string{"-c", "--config", "-config", "-confdir", "--confdir", "-C"} {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
			if strings.HasPrefix(a, flag+"=") {
				return strings.TrimPrefix(a, flag+"=")
			}
		}
	}
	if sw == "hysteria" {
		for _, p := range knownConfigs["hysteria"] {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func readSmall(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxFile {
		return nil, fmt.Errorf("%s is too large", path)
	}
	return os.ReadFile(path)
}

// parse reads one config file (or a directory of Xray JSON files).
func parse(sw, path string) Found {
	f := Found{Software: sw, Config: path, Inbounds: []Inbound{}}
	var err error
	switch sw {
	case "xray", "v2ray":
		f.Inbounds, err = parseXray(path)
	case "sing-box":
		f.Inbounds, err = parseSingBox(path)
	case "hysteria":
		f.Inbounds, err = parseHysteria(path)
	}
	if err != nil {
		f.Error = err.Error()
	}
	return f
}

// ---------------------------------------------------------------- Xray / V2Ray

type xrayFile struct {
	Inbounds []json.RawMessage `json:"inbounds"`
}

func parseXray(path string) ([]Inbound, error) {
	var files []string
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		m, _ := filepath.Glob(filepath.Join(path, "*.json"))
		sort.Strings(m)
		files = m
	} else {
		files = []string{path}
	}
	var out []Inbound
	for _, file := range files {
		raw, err := readSmall(file)
		if err != nil {
			return out, err
		}
		var cfg xrayFile
		if err := json.Unmarshal(stripComments(raw), &cfg); err != nil {
			return out, fmt.Errorf("%s: %v", file, err)
		}
		for _, r := range cfg.Inbounds {
			if in, ok := xrayInbound(r); ok {
				out = append(out, in)
			}
		}
	}
	return out, nil
}

type xrayIn struct {
	Tag      string          `json:"tag"`
	Protocol string          `json:"protocol"`
	Listen   string          `json:"listen"`
	Port     json.RawMessage `json:"port"`
	Settings struct {
		Clients  []map[string]any `json:"clients"`
		Accounts []map[string]any `json:"accounts"`
		Method   string           `json:"method"`
		Password string           `json:"password"`
		UDP      bool             `json:"udp"`
		Network  string           `json:"network"`
		// VLESS
		Decryption string `json:"decryption"`
	} `json:"settings"`
	Stream struct {
		Network  string `json:"network"`
		Security string `json:"security"`
		WS       struct {
			Path    string            `json:"path"`
			Host    string            `json:"host"`
			Headers map[string]string `json:"headers"`
		} `json:"wsSettings"`
		GRPC struct {
			ServiceName string `json:"serviceName"`
		} `json:"grpcSettings"`
		HU struct {
			Path string `json:"path"`
			Host string `json:"host"`
		} `json:"httpupgradeSettings"`
		XHTTP struct {
			Path string `json:"path"`
			Host string `json:"host"`
			Mode string `json:"mode"`
		} `json:"xhttpSettings"`
		Split struct {
			Path string `json:"path"`
			Host string `json:"host"`
			Mode string `json:"mode"`
		} `json:"splithttpSettings"`
		Reality struct {
			Dest        json.RawMessage `json:"dest"`
			Target      json.RawMessage `json:"target"`
			ServerNames []string        `json:"serverNames"`
			PrivateKey  string          `json:"privateKey"`
			ShortIDs    []string        `json:"shortIds"`
		} `json:"realitySettings"`
		TLS struct {
			ServerName   string `json:"serverName"`
			Certificates []struct {
				CertificateFile string   `json:"certificateFile"`
				KeyFile         string   `json:"keyFile"`
				Certificate     []string `json:"certificate"`
				Key             []string `json:"key"`
			} `json:"certificates"`
		} `json:"tlsSettings"`
	} `json:"streamSettings"`
}

func xrayInbound(raw json.RawMessage) (Inbound, bool) {
	var x xrayIn
	if json.Unmarshal(raw, &x) != nil {
		return Inbound{}, false
	}
	if x.Protocol == "dokodemo-door" || x.Protocol == "tunnel" || strings.HasPrefix(x.Tag, "api") {
		return Inbound{}, false // the API port and plain port forwards are not proxies to import
	}
	in := Inbound{Tag: x.Tag, Listen: x.Listen, Port: portOf(x.Port), Users: []User{}}
	switch x.Protocol {
	case "vless", "vmess", "trojan", "shadowsocks", "socks", "http":
		in.Protocol = x.Protocol
	case "hysteria":
		in.Protocol = "hysteria2"
	default:
		in.Protocol = "other"
		in.Note = "Rosélune does not run " + x.Protocol
	}
	switch n := strings.ToLower(x.Stream.Network); n {
	case "", "tcp", "raw":
		in.Transport = "raw"
	case "ws", "websocket":
		in.Transport, in.Path, in.Host = "ws", x.Stream.WS.Path, x.Stream.WS.Host
		if in.Host == "" {
			in.Host = x.Stream.WS.Headers["Host"]
		}
	case "grpc", "gun":
		in.Transport, in.Service = "grpc", x.Stream.GRPC.ServiceName
	case "httpupgrade":
		in.Transport, in.Path, in.Host = "httpupgrade", x.Stream.HU.Path, x.Stream.HU.Host
	case "xhttp":
		in.Transport, in.Path, in.Host, in.XHTTPMode = "xhttp", x.Stream.XHTTP.Path, x.Stream.XHTTP.Host, x.Stream.XHTTP.Mode
	case "splithttp":
		in.Transport, in.Path, in.Host, in.XHTTPMode = "xhttp", x.Stream.Split.Path, x.Stream.Split.Host, x.Stream.Split.Mode
	case "hysteria":
		in.Transport = ""
	default:
		in.Transport = n
		if in.Note == "" {
			in.Note = "Rosélune does not support the " + n + " transport"
		}
	}
	switch strings.ToLower(x.Stream.Security) {
	case "", "none":
		in.Security = "none"
	case "reality":
		in.Security = "reality"
		r := x.Stream.Reality
		in.Target = targetOf(r.Target)
		if in.Target == "" {
			in.Target = targetOf(r.Dest)
		}
		if len(r.ServerNames) > 0 {
			in.SNI = r.ServerNames[0]
		}
		in.PrivateKey, in.ShortIDs = r.PrivateKey, r.ShortIDs
	case "tls":
		in.Security = "tls"
		in.SNI = x.Stream.TLS.ServerName
		if len(x.Stream.TLS.Certificates) > 0 {
			c := x.Stream.TLS.Certificates[0]
			if len(c.Certificate) > 0 {
				in.CertPEM, in.KeyPEM = strings.Join(c.Certificate, "\n"), strings.Join(c.Key, "\n")
			} else {
				in.CertPEM, in.KeyPEM = fileText(c.CertificateFile), fileText(c.KeyFile)
			}
		}
	default:
		in.Security = x.Stream.Security
		if in.Note == "" {
			in.Note = "Rosélune does not support " + x.Stream.Security + " security"
		}
	}
	if in.Protocol == "shadowsocks" {
		in.Method, in.ServerKey = x.Settings.Method, x.Settings.Password
	}
	if in.Protocol == "socks" {
		in.UDP = x.Settings.UDP
	}
	if in.Protocol == "vless" && x.Settings.Decryption != "none" {
		in.Decryption = x.Settings.Decryption
	}
	for i, c := range x.Settings.Clients {
		u := User{Name: str(c["email"]), ID: str(c["id"]), Password: str(c["password"]), Flow: str(c["flow"]),
			Method: str(c["method"])}
		if in.Protocol == "hysteria2" {
			u.Password = str(c["auth"])
		}
		if u.Name == "" {
			u.Name = fmt.Sprintf("%s-%d", nz(in.Tag, in.Protocol), i+1)
		}
		in.Users = append(in.Users, u)
	}
	for i, a := range x.Settings.Accounts {
		in.Users = append(in.Users, User{Name: nz(str(a["user"]), fmt.Sprintf("user-%d", i+1)), Username: str(a["user"]),
			Password: str(a["pass"])})
	}
	if in.Protocol == "shadowsocks" && len(in.Users) == 0 && in.ServerKey != "" {
		// single-user Shadowsocks: the one password is the user's
		in.Users = append(in.Users, User{Name: nz(in.Tag, "shadowsocks"), Password: in.ServerKey, Method: in.Method})
	}
	return in, true
}

func targetOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n int
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return "127.0.0.1:" + strconv.Itoa(n)
	}
	return ""
}

func portOf(raw json.RawMessage) int {
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v
		}
	}
	return 0
}

// ---------------------------------------------------------------- sing-box

type sbIn struct {
	Type       string           `json:"type"`
	Tag        string           `json:"tag"`
	Listen     string           `json:"listen"`
	ListenPort int              `json:"listen_port"`
	Users      []map[string]any `json:"users"`
	Method     string           `json:"method"`
	Password   string           `json:"password"`
	TLS        struct {
		Enabled         bool   `json:"enabled"`
		ServerName      string `json:"server_name"`
		CertificatePath string `json:"certificate_path"`
		KeyPath         string `json:"key_path"`
		ACME            *struct {
			Domain []string `json:"domain"`
		} `json:"acme"`
		Reality struct {
			Enabled    bool     `json:"enabled"`
			PrivateKey string   `json:"private_key"`
			ShortID    []string `json:"short_id"`
			Handshake  struct {
				Server     string `json:"server"`
				ServerPort int    `json:"server_port"`
			} `json:"handshake"`
		} `json:"reality"`
	} `json:"tls"`
	Transport struct {
		Type        string            `json:"type"`
		Path        string            `json:"path"`
		Host        any               `json:"host"`
		Headers     map[string]string `json:"headers"`
		ServiceName string            `json:"service_name"`
	} `json:"transport"`
	Obfs struct {
		Type     string `json:"type"`
		Password string `json:"password"`
	} `json:"obfs"`
}

func parseSingBox(path string) ([]Inbound, error) {
	raw, err := readSmall(path)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Inbounds []sbIn `json:"inbounds"`
	}
	if err := json.Unmarshal(stripComments(raw), &cfg); err != nil {
		return nil, err
	}
	var out []Inbound
	for _, x := range cfg.Inbounds {
		in := Inbound{Tag: x.Tag, Listen: x.Listen, Port: x.ListenPort, Users: []User{}, Transport: "raw", Security: "none"}
		switch x.Type {
		case "vless", "vmess", "trojan", "shadowsocks", "socks", "http", "hysteria2":
			in.Protocol = x.Type
		case "mixed":
			in.Protocol = "socks"
		case "tun", "tproxy", "redirect", "direct":
			continue
		default:
			in.Protocol, in.Note = "other", "Rosélune does not run "+x.Type
		}
		switch x.Transport.Type {
		case "":
		case "ws":
			in.Transport, in.Path = "ws", x.Transport.Path
			in.Host = x.Transport.Headers["Host"]
		case "httpupgrade":
			in.Transport, in.Path = "httpupgrade", x.Transport.Path
			if h, ok := x.Transport.Host.(string); ok {
				in.Host = h
			}
		case "grpc":
			in.Transport, in.Service = "grpc", x.Transport.ServiceName
		default:
			in.Transport, in.Note = x.Transport.Type, "Rosélune does not support the "+x.Transport.Type+" transport"
		}
		if x.Type == "hysteria2" {
			in.Transport = ""
			in.ObfsPassword = x.Obfs.Password
		}
		switch {
		case x.TLS.Reality.Enabled:
			in.Security, in.SNI = "reality", x.TLS.ServerName
			in.PrivateKey, in.ShortIDs = x.TLS.Reality.PrivateKey, x.TLS.Reality.ShortID
			if h := x.TLS.Reality.Handshake; h.Server != "" {
				in.Target = h.Server + ":" + strconv.Itoa(max(h.ServerPort, 443))
			}
		case x.TLS.Enabled:
			in.Security, in.SNI = "tls", x.TLS.ServerName
			in.CertPEM, in.KeyPEM = fileText(x.TLS.CertificatePath), fileText(x.TLS.KeyPath)
			if x.TLS.ACME != nil {
				in.ACME = true
				if in.SNI == "" && len(x.TLS.ACME.Domain) > 0 {
					in.SNI = x.TLS.ACME.Domain[0]
				}
			}
		}
		if x.Type == "shadowsocks" {
			in.Method, in.ServerKey = x.Method, x.Password
		}
		for i, u := range x.Users {
			user := User{Name: nz(str(u["name"]), fmt.Sprintf("%s-%d", nz(x.Tag, x.Type), i+1)), ID: str(u["uuid"]),
				Password: str(u["password"]), Flow: str(u["flow"]), Username: str(u["username"])}
			in.Users = append(in.Users, user)
		}
		if x.Type == "shadowsocks" && len(in.Users) == 0 && x.Password != "" {
			in.Users = append(in.Users, User{Name: nz(x.Tag, "shadowsocks"), Password: x.Password, Method: x.Method})
		}
		out = append(out, in)
	}
	return out, nil
}

// ---------------------------------------------------------------- Hysteria2

func parseHysteria(path string) ([]Inbound, error) {
	raw, err := readSmall(path)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Listen string `yaml:"listen" json:"listen"`
		TLS    struct {
			Cert string `yaml:"cert" json:"cert"`
			Key  string `yaml:"key" json:"key"`
		} `yaml:"tls" json:"tls"`
		ACME struct {
			Domains []string `yaml:"domains" json:"domains"`
		} `yaml:"acme" json:"acme"`
		Auth struct {
			Type     string            `yaml:"type" json:"type"`
			Password string            `yaml:"password" json:"password"`
			UserPass map[string]string `yaml:"userpass" json:"userpass"`
		} `yaml:"auth" json:"auth"`
		Obfs struct {
			Type       string `yaml:"type" json:"type"`
			Salamander struct {
				Password string `yaml:"password" json:"password"`
			} `yaml:"salamander" json:"salamander"`
		} `yaml:"obfs" json:"obfs"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	in := Inbound{Tag: "hysteria", Protocol: "hysteria2", Users: []User{}, Security: "tls"}
	l := cfg.Listen
	if l == "" {
		l = ":443"
	}
	if i := strings.LastIndex(l, ":"); i >= 0 {
		in.Listen = strings.Trim(l[:i], "[]")
		in.Port, _ = strconv.Atoi(l[i+1:])
	}
	if len(cfg.ACME.Domains) > 0 {
		in.ACME, in.SNI = true, cfg.ACME.Domains[0]
	} else {
		in.CertPEM, in.KeyPEM = fileText(cfg.TLS.Cert), fileText(cfg.TLS.Key)
	}
	if cfg.Obfs.Type == "salamander" {
		in.ObfsPassword = cfg.Obfs.Salamander.Password
	}
	switch cfg.Auth.Type {
	case "password":
		in.Users = append(in.Users, User{Name: "hysteria", Password: cfg.Auth.Password})
	case "userpass":
		names := make([]string, 0, len(cfg.Auth.UserPass))
		for n := range cfg.Auth.UserPass {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			// Hysteria's userpass sends "user:password" as the auth string
			in.Users = append(in.Users, User{Name: n, Password: n + ":" + cfg.Auth.UserPass[n]})
		}
	default:
		in.Note = "this Hysteria2 server authenticates through " + nz(cfg.Auth.Type, "an unknown method") +
			" - its users cannot be imported"
	}
	return []Inbound{in}, nil
}

// ---------------------------------------------------------------- helpers

func fileText(path string) string {
	if path == "" {
		return ""
	}
	b, err := readSmall(path)
	if err != nil || len(b) > 64<<10 {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// stripComments removes // and /* */ comments, which Xray and sing-box accept in JSON.
func stripComments(b []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '*' {
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return out
}
