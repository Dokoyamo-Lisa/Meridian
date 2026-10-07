# End-to-end tests in a local VM

A disposable Ubuntu VM ([Lima](https://lima-vm.io)) runs the agent and the cores; the panel runs on
your machine; a network namespace inside the VM runs real clients (sing-box) for every protocol.
Nothing here touches real servers.

## Once

```bash
limactl create --name meridian-test test/e2e/lima.yaml
limactl start meridian-test
```

## Each session

1. Build and start the panel (from the repository root):

   ```bash
   make agents
   go build -o /tmp/meridian-dev ./cmd/meridian
   MERIDIAN_NO_GEO_DOWNLOAD=1 MERIDIAN_ADMIN_PASSWORD='a-local-test-password' \
     /tmp/meridian-dev serve --listen 0.0.0.0:18080 --data /tmp/meridian-dev-data --agent-dir dist
   ```

   Sign in at http://127.0.0.1:18080. In **Settings › Panel** set the public URL to the address the
   VM reaches your machine at (for Lima's user networking that is `http://192.168.5.2:18080`).

2. **Servers › Add server** with all four protocols and the VM's address, then run the install
   command inside the VM:

   ```bash
   limactl shell meridian-test -- sudo bash -c 'cd /root && <paste the install command>'
   ```

3. Prepare the client side (after every VM boot) and generate client configs from a subscription:

   ```bash
   limactl copy -r test/e2e meridian-test:/tmp/e2e
   limactl shell meridian-test -- sudo bash /tmp/e2e/setup-client.sh
   limactl shell meridian-test -- sudo bash /tmp/e2e/gen-clients.sh 'http://192.168.5.2:18080/s/<token>'
   ```

## Tests

| Script (run in the VM as root) | Checks |
| --- | --- |
| `quick.sh LABEL` | one request through REALITY, Hysteria2, Shadowsocks and WireGuard: `204 204 204 204` |
| `client.sh /tmp/vmN.json PORT LABEL` | three requests plus a 5 MB download through one protocol, with timings |
| `pause-live.sh` | a slow Hysteria2 download; pause the subscription in the panel while it runs and watch it end |
| `matrix.py LINK` | every proxy in the sing-box, Clash and share-link formats through sing-box, mihomo and Xray: one `204` line per proxy and client (`setup-client.sh` installs the clients) |
| `country-live.sh CONFIG PORT [SECONDS]` | a client on a Chinese address (101.0.0.2) making a request every 2 s: block CN in Access and watch it stop, add an exception and watch it return |
| `foreign-xray.sh` | installs an Xray "from another panel" as `xray.service` with users carol and dave, and client configs with the original credentials - for testing import and take-over |
| `block-live.sh N` | one open session for protocol N (1-4) making a request every 2 s; block `10.99.0.2` in the panel: requests fail within seconds, and recover after unblocking |

Things worth checking after a change:

- **No restarts**: `systemctl show -p MainPID --value meridian-xray` must stay the same across user
  changes, protocol edits, panel restarts and agent upgrades.
- **Live pause/resume**: pause a subscription → `quick.sh` gives `000` everywhere within seconds;
  resume → `204` everywhere right away.
- **Accounting**: the subscription's usage in the panel grows by what the tests downloaded.
- **Firewall rules on other nft versions**: `MERIDIAN_NFT_DUMP=DIR go test ./internal/agent/nft -run
  TestDumpRulesets` writes the rulesets the agent produces; `nft -c -f DIR/full.nft` on a host checks
  them with that host's nft without applying anything (nft 1.0.6 on Debian 12 is the oldest checked).
- **Loopback guard**: as a normal user in the VM, `curl 127.0.0.1:50000` (the agent's Xray API port; 62789 on agents installed before 0.4.2) must be refused; as root
  it connects.
- **Upgrade**: build agents with a new version, **Upgrade agent** on the server page; traffic keeps
  flowing.

## Alpine Linux (OpenRC)

A second VM checks the agent on Alpine, where services are OpenRC's and nftables is optional:

```bash
limactl create --name meridian-alpine test/e2e/lima-alpine.yaml
limactl start meridian-alpine
limactl shell meridian-alpine -- sudo apk add curl python3      # the probe's needs, not the agent's
```

Add a server for it (Xray protocols, Hysteria2) and run the install command in the VM with `sh` -
it works with busybox's `wget` too. Then fetch sing-box's musl build (from its GitHub releases) to
`/tmp/sing-box` and run `test/e2e/alpine-probe.py LINK 'SERVER NAME'` there: every proxy of the
server must answer `204`. Check also:

- `rc-status` lists `meridian-agent`, `meridian-xray`, `meridian-hy2.N` and `meridian-realm.N`;
  `ps -o user,args` shows realm as `nobody`.
- Without nftables the server's page lists what does not apply, WireGuard and kernel forwards are
  refused, and new forwards use realm. `apk add nftables iproute2`: the page notices within seconds;
  then WireGuard and kernel forwards work (test a forward from a network namespace - a connection
  from the host itself never passes through PREROUTING).
- **Upgrade agent** from the panel: the agent exits and `supervise-daemon` starts the new one;
  Xray's and Hysteria2's PIDs stay the same.
- `meridian-agent uninstall` leaves no `meridian-*` service, file or nftables table behind.

**A server whose provider decides the ports** (NAT, LXC, Incus) can be simulated on the same VM: set
the server's ports from the provider to `40001-40010:10001-10010` (protocols then get 10001 and up),
make the VM forward the public numbers to them for its own connections, and probe - the probe
connects to the numbers in the links:

```bash
limactl shell meridian-alpine -- sudo sh -c '
nft add table ip natsim
nft add chain ip natsim out "{ type nat hook output priority -100; }"
for i in 1 2 3 4 5 6 7 8 9 10; do
  for p in tcp udp; do nft add rule ip natsim out ip daddr 127.0.0.1 $p dport $((40000+i)) redirect to :$((10000+i)); done
done'
limactl shell meridian-alpine -- sudo python3 /tmp/alpine-probe.py LINK 'SERVER NAME'   # every proxy: 204
limactl shell meridian-alpine -- sudo nft delete table ip natsim                        # afterwards
```

## Notes

- `setup-client.sh` creates the `client` namespace (10.99.0.2) with NAT through the VM, and
  downloads sing-box once.
- If your machine routes traffic through a proxy with fake-IP DNS, Hysteria2's outbound lookups can
  fail in odd ways; the VM's own resolver avoids that.
