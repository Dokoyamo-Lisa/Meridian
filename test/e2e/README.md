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
- **Loopback guard**: as a normal user in the VM, `curl 127.0.0.1:62789` must be refused; as root
  it connects.
- **Upgrade**: build agents with a new version, **Upgrade agent** on the server page; traffic keeps
  flowing.

## Notes

- `setup-client.sh` creates the `client` namespace (10.99.0.2) with NAT through the VM, and
  downloads sing-box once.
- If your machine routes traffic through a proxy with fake-IP DNS, Hysteria2's outbound lookups can
  fail in odd ways; the VM's own resolver avoids that.
