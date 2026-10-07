// Package wg runs kernel WireGuard interfaces for the agent. Peers are added and removed one at a
// time through netlink, so other devices stay connected. Per-device traffic comes from the
// kernel's peer counters; where devices go comes from conntrack, named by the DNS resolver the
// agent runs for them.
package wg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"meridian/internal/proto"
)

const Prefix = "uwg"

type Engine struct {
	Events func(kind, level, msg string)

	mu      sync.Mutex
	applied map[string]proto.WGInterface
	last    map[string][2]int64 // iface/pubkey -> rx, tx
	seen    map[string]int64    // iface/pubkey -> first time online this session
	dns     map[string]*resolver
	flows   *flowMonitor
}

func New() *Engine {
	return &Engine{applied: map[string]proto.WGInterface{}, last: map[string][2]int64{}, seen: map[string]int64{},
		dns: map[string]*resolver{}}
}

func ip(args ...string) (string, error) {
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ip %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// busyboxIP reports whether "ip" is busybox's (Alpine without iproute2), which has no "type wireguard".
func busyboxIP() bool {
	path, err := exec.LookPath("ip")
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	return err == nil && filepath.Base(real) == "busybox"
}

func linkExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

// Apply makes the interfaces match desired.
func (e *Engine) Apply(desired []proto.WGInterface) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	want := map[string]proto.WGInterface{}
	for _, w := range desired {
		want[w.Name] = w
	}
	var errs []string
	// remove interfaces we own that are no longer wanted
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if strings.HasPrefix(i.Name, Prefix) {
			if _, ok := want[i.Name]; !ok {
				if r := e.dns[i.Name]; r != nil {
					r.stop()
					delete(e.dns, i.Name)
				}
				if _, err := ip("link", "del", i.Name); err != nil {
					errs = append(errs, err.Error())
				}
				delete(e.applied, i.Name)
			}
		}
	}
	if len(desired) == 0 {
		if e.flows != nil {
			e.flows.stop()
			e.flows = nil
		}
		return joinErr(errs)
	}
	client, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("wireguard: %w", err)
	}
	defer client.Close()
	for _, w := range desired {
		if err := e.applyOne(client, w); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", w.Name, err))
			continue
		}
		e.applied[w.Name] = w
	}
	if e.flows == nil {
		if fm, err := newFlowMonitor(); err == nil {
			e.flows = fm
		} else if e.Events != nil {
			e.Events("monitor", "warn", "WireGuard destinations unavailable: "+err.Error())
		}
	}
	return joinErr(errs)
}

func (e *Engine) applyOne(client *wgctrl.Client, w proto.WGInterface) error {
	if !linkExists(w.Name) {
		if _, err := ip("link", "add", w.Name, "type", "wireguard"); err != nil {
			_ = exec.Command("modprobe", "wireguard").Run()
			if _, err := ip("link", "add", w.Name, "type", "wireguard"); err != nil {
				if busyboxIP() {
					return errors.New("WireGuard needs the iproute2 package on this server (apk add iproute2): busybox's ip cannot create WireGuard interfaces")
				}
				return err
			}
		}
	}
	if err := syncAddrs(w.Name, w.Address); err != nil {
		return err
	}
	mtu := w.MTU
	if mtu == 0 {
		mtu = 1420
	}
	if _, err := ip("link", "set", w.Name, "mtu", fmt.Sprint(mtu), "up"); err != nil {
		return err
	}

	priv, err := wgtypes.ParseKey(w.PrivateKey)
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	dev, err := client.Device(w.Name)
	if err != nil {
		return err
	}
	cfg := wgtypes.Config{}
	changed := false
	if dev.PrivateKey != priv {
		cfg.PrivateKey = &priv
		changed = true
	}
	if dev.ListenPort != w.ListenPort {
		port := w.ListenPort
		cfg.ListenPort = &port
		changed = true
	}
	have := map[wgtypes.Key]wgtypes.Peer{}
	for _, p := range dev.Peers {
		have[p.PublicKey] = p
	}
	wantKeys := map[wgtypes.Key]bool{}
	for _, p := range w.Peers {
		pub, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			continue
		}
		wantKeys[pub] = true
		var allowed []net.IPNet
		for _, a := range p.AllowedIPs {
			if _, n, err := net.ParseCIDR(a); err == nil {
				allowed = append(allowed, *n)
			}
		}
		var psk *wgtypes.Key
		if p.PresharedKey != "" {
			if k, err := wgtypes.ParseKey(p.PresharedKey); err == nil {
				psk = &k
			}
		}
		if cur, ok := have[pub]; ok && sameIPs(cur.AllowedIPs, allowed) && (psk == nil || cur.PresharedKey == *psk) {
			continue // unchanged: do not touch, the session keeps going
		}
		cfg.Peers = append(cfg.Peers, wgtypes.PeerConfig{PublicKey: pub, PresharedKey: psk, ReplaceAllowedIPs: true,
			AllowedIPs: allowed})
		changed = true
	}
	for k := range have {
		if !wantKeys[k] {
			cfg.Peers = append(cfg.Peers, wgtypes.PeerConfig{PublicKey: k, Remove: true})
			changed = true
		}
	}
	if changed {
		if err := client.ConfigureDevice(w.Name, cfg); err != nil {
			return err
		}
	}

	if w.DNS && len(w.Address) > 0 {
		gw := strings.Split(w.Address[0], "/")[0]
		r := e.dns[w.Name]
		if r == nil || r.addr != gw {
			if r != nil {
				r.stop()
			}
			nr, err := startResolver(gw)
			if err != nil {
				return fmt.Errorf("DNS on %s: %w", gw, err)
			}
			e.dns[w.Name] = nr
		}
	} else if r := e.dns[w.Name]; r != nil {
		r.stop()
		delete(e.dns, w.Name)
	}
	return nil
}

func sameIPs(a, b []net.IPNet) bool {
	if len(a) != len(b) {
		return false
	}
	s := func(l []net.IPNet) []string {
		out := make([]string, len(l))
		for i, n := range l {
			out[i] = n.String()
		}
		sort.Strings(out)
		return out
	}
	x, y := s(a), s(b)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// syncAddrs makes the interface carry exactly addrs.
func syncAddrs(name string, addrs []string) error {
	out, err := ip("-j", "addr", "show", "dev", name)
	if err != nil {
		return err
	}
	var info []struct {
		AddrInfo []struct {
			Local     string `json:"local"`
			PrefixLen int    `json:"prefixlen"`
			Scope     string `json:"scope"`
		} `json:"addr_info"`
	}
	_ = json.Unmarshal([]byte(out), &info)
	have := map[string]bool{}
	for _, i := range info {
		for _, a := range i.AddrInfo {
			if a.Scope == "link" {
				continue
			}
			have[fmt.Sprintf("%s/%d", a.Local, a.PrefixLen)] = true
		}
	}
	want := map[string]bool{}
	v6off := ipv6Off()
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a)
		if err != nil || (p.Addr().Is6() && v6off) { // a kernel without IPv6 refuses IPv6 addresses
			continue
		}
		want[p.String()] = true
		if !have[p.String()] {
			if _, err := ip("addr", "add", p.String(), "dev", name); err != nil && !strings.Contains(err.Error(), "File exists") {
				return err
			}
		}
	}
	for a := range have {
		if !want[a] {
			_, _ = ip("addr", "del", a, "dev", name)
		}
	}
	return nil
}

// Remove deletes every interface we own.
func (e *Engine) Remove() {
	_ = e.Apply(nil)
}

// ---------------------------------------------------------------- collection

type Collected struct {
	Traffic []proto.UserTraffic
	Online  []proto.OnlineUser
	IPs     []proto.IPSeen
	Dests   []proto.DestSeen
}

// Collect returns traffic and presence per device since the last call.
func (e *Engine) Collect(connLog, destLog bool) Collected {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out Collected
	if len(e.applied) == 0 {
		return out
	}
	client, err := wgctrl.New()
	if err != nil {
		return out
	}
	defer client.Close()
	now := time.Now()
	peerSub := map[netip.Addr]struct{ sub, node int64 }{}
	for name, w := range e.applied {
		dev, err := client.Device(name)
		if err != nil {
			continue
		}
		subOf := map[string]int64{}
		for _, p := range w.Peers {
			subOf[p.PublicKey] = p.SubID
			for _, a := range p.AllowedIPs {
				if pf, err := netip.ParsePrefix(a); err == nil {
					peerSub[pf.Addr()] = struct{ sub, node int64 }{p.SubID, w.NodeID}
				}
			}
		}
		for _, p := range dev.Peers {
			key := p.PublicKey.String()
			sub, ok := subOf[key]
			if !ok {
				continue
			}
			lk := name + "/" + key
			prev := e.last[lk]
			rx, tx := p.ReceiveBytes, p.TransmitBytes
			if rx < prev[0] || tx < prev[1] {
				prev = [2]int64{} // interface was recreated
			}
			up, down := rx-prev[0], tx-prev[1]
			e.last[lk] = [2]int64{rx, tx}
			if up > 0 || down > 0 {
				out.Traffic = append(out.Traffic, proto.UserTraffic{Sub: sub, Node: w.NodeID, Up: up, Down: down})
			}
			active := !p.LastHandshakeTime.IsZero() && now.Sub(p.LastHandshakeTime) < 3*time.Minute
			if active && p.Endpoint != nil {
				ipStr := p.Endpoint.IP.String()
				first, ok := e.seen[lk]
				if !ok {
					first = now.Unix()
					e.seen[lk] = first
				}
				out.Online = append(out.Online, proto.OnlineUser{Sub: sub, Node: w.NodeID,
					IPs: []proto.OnlineIP{{IP: ipStr, Since: first, Last: p.LastHandshakeTime.Unix()}}})
				if connLog {
					out.IPs = append(out.IPs, proto.IPSeen{Sub: sub, Node: w.NodeID, IP: ipStr, First: first,
						Last: now.Unix(), Conns: 0})
				}
			} else {
				delete(e.seen, lk)
			}
		}
	}
	if destLog && e.flows != nil {
		names := func(client netip.Addr, dst netip.Addr) string {
			for _, r := range e.dns {
				if n := r.nameFor(client, dst); n != "" {
					return n
				}
			}
			return ""
		}
		out.Dests = e.flows.collect(peerSub, names)
	}
	return out
}

func joinErr(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

var _ = context.Background

// ipv6Off says whether the kernel has IPv6 turned off (ipv6.disable=1, or disable_ipv6 for all).
func ipv6Off() bool {
	if _, err := os.Stat("/proc/net/if_inet6"); err != nil {
		return true
	}
	b, _ := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	return strings.TrimSpace(string(b)) == "1"
}
