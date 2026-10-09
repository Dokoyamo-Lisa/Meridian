# Checking the subscription formats

What users import into their apps must be exactly what those apps read. Four tests check it, from
the strictest to the ones that rely on documentation:

| Test | What it checks | Needs |
| --- | --- | --- |
| `TestFormatsInRealClients` (`internal/panel/formats_test.go`) | Every protocol combination the panel accepts - on an IPv4 address, an IPv6 address and a domain - rendered for sing-box, mihomo (Clash Verge Rev, FlClash, Mihomo Party) and Xray, and checked by those programs themselves: `sing-box check` (the oldest sing-box the sing-box format supports and the newest), `mihomo -t`, and `xray run -test` for the server's inbound and for the outbound a proxy pass uses. Each address's whole subscription is checked too, with names apps must quote. | the client programs (below) |
| `TestShareLinksRoundTrip` (same file) | Every share link (v2rayN, Shadowrocket, Hiddify and the links on the users' page) is read back with the link reader that follows Xray's rules, and its address is checked the way strict readers (.NET, OkHttp) read it: IPv6 in brackets. | nothing |
| `TestGolden`, `TestAddressForms` (`internal/subgen/golden_test.go`) | Stash, Surge, Quantumult X, Loon and Shadowrocket have no parser one can run: their output for representative protocols is kept in `internal/subgen/testdata/golden` and was checked field by field against each app's documentation (sources in the test file). Each format writes IPv6 addresses the way it reads them. | nothing |
| `TestEveryAcceptedProtocolWorks` (`internal/panel/protocols_test.go`) | Every accepted combination renders a server configuration and works in at least one app. | nothing |

The last three run with `make test`. The first is skipped until the client programs are there.

## Run the real-client check

On Linux, or on a development Mac (amd64 or arm64), from the repository root:

```bash
bash test/formats/fetch-clients.sh
```

It downloads the pinned versions of sing-box (two of them), mihomo and Xray from their projects'
own GitHub releases into `~/.cache/meridian/formats` (`MERIDIAN_FORMATS_CACHE` puts them elsewhere),
checks each archive against its SHA-256 before unpacking it, and prints the programs' versions and
the variables to set. Expected output ends with:

```
sing-box version 1.14.3
Mihomo Meta v1.19.32 ...
Xray 26.3.27 (Xray, Penetrates Everything.) ...

Ready. Run the checks with:
...
```

A second run downloads nothing. Then:

```bash
eval "$(bash test/formats/fetch-clients.sh --env)"
MERIDIAN_NO_GEO_DOWNLOAD=1 go test -count=1 ./internal/panel -run 'TestFormatsInRealClients|TestShareLinksRoundTrip' -v
```

or simply `make formats`. It takes about ten seconds. Expected output:

```
--- PASS: TestFormatsInRealClients
    formats_test.go:...: checked map[mihomo:... sing-box:... xray outbound:... xray server:...]
--- PASS: TestShareLinksRoundTrip
    formats_test.go:...: ... links read back
```

### When it fails

- **`fetch-clients: ... has SHA-256 ..., expected ...`** - the download is not the release file the
  script pins. Nothing is unpacked. Try again later; if it persists, compare with the release page on
  GitHub before changing anything.
- **`fetch-clients: <system> is not supported`** - run it on Linux or a Mac, amd64 or arm64.
- **A combination fails** - the message names the address, the combination's number and settings,
  the client program, the last lines it printed and the configuration it was given. Fix the format
  in `internal/subgen` (or, when no app can use the combination, refuse it in `xraySettings.check` in
  `internal/panel/protocols.go` with a reason that says what to do). Never relax the test.

## Change a pinned version

1. Read the new release's file digests from GitHub, for example:

   ```bash
   gh api repos/SagerNet/sing-box/releases/tags/v1.14.3 \
     --jq '.assets[] | select(.name | test("-(darwin|linux)-(amd64|arm64)\\.tar\\.gz$")) | .name + " " + .digest'
   ```

   (`XTLS/Xray-core` and `MetaCubeX/mihomo` alike).
2. Put the version and the four SHA-256 values in `test/formats/fetch-clients.sh`.
3. Run the script and the checks again.

## Change a format without a parser

After a deliberate change to the Stash, Surge, Quantumult X, Loon or Shadowrocket output, check the
new lines against the app's documentation, then rewrite the golden files:

```bash
go test ./internal/subgen -run TestGolden -update
git diff internal/subgen/testdata/golden
```
