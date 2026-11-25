#!/usr/bin/env bash
# Downloads the client cores whose parsers check Meridian's subscription formats: sing-box (the
# oldest version the sing-box format promises and the newest), mihomo and Xray. Pinned versions from
# the projects' own GitHub releases, each archive checked against its SHA-256 before it is unpacked.
#
#   bash test/formats/fetch-clients.sh          # downloads into ~/.cache/meridian/formats
#   eval "$(bash test/formats/fetch-clients.sh --env)"
#   MERIDIAN_NO_GEO_DOWNLOAD=1 go test ./internal/panel -run TestFormatsInRealClients -v
#
# Linux and macOS, amd64 and arm64. Set MERIDIAN_FORMATS_CACHE to keep the files elsewhere.
set -euo pipefail

SINGBOX_OLD=1.12.25
SINGBOX_NEW=1.14.4
MIHOMO=1.19.34
XRAY=26.11.9

cache="${MERIDIAN_FORMATS_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/meridian/formats}"
env_only=0
[ "${1:-}" = "--env" ] && env_only=1

os=$(uname -s)
arch=$(uname -m)
case "$os/$arch" in
Darwin/arm64) plat=darwin-arm64 ;;
Darwin/x86_64) plat=darwin-amd64 ;;
Linux/x86_64) plat=linux-amd64 ;;
Linux/aarch64 | Linux/arm64) plat=linux-arm64 ;;
*)
	echo "fetch-clients: $os/$arch is not supported - use Linux or macOS on amd64 or arm64" >&2
	exit 1
	;;
esac

# asset name and SHA-256 per platform, from each release's own page on GitHub
singbox_asset() { # version
	echo "sing-box-$1-$plat.tar.gz"
}
singbox_sum() { # version
	case "$1/$plat" in
	1.12.25/darwin-arm64) echo a4a06d507f3f4d951490168d1372fce4c02db7211e88af9da13f93ed98068d5e ;;
	1.12.25/darwin-amd64) echo fb9cb2a1d3160b9ef4a288818286ad27711907228232b8d84410cad7e42726d8 ;;
	1.12.25/linux-amd64) echo a1ec76e2b6b139eb747a1b1ebee7d14b8d4be5a833596cad8070a31ef960301f ;;
	1.12.25/linux-arm64) echo 719b76196c8b31efa636b2d8f669e314547e0da0a5ab38a75e1882d307bbd154 ;;
	1.14.4/darwin-arm64) echo 0d8c7f5ed5d587b0f1a5e9772a708ec3bfa4ac4c9329604b01d8d9c2ba406838 ;;
	1.14.4/darwin-amd64) echo eae86b9b033bba50b7c44ab719e4bfac0aca05efd28483390ed3aa510f32db25 ;;
	1.14.4/linux-amd64) echo 699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968 ;;
	1.14.4/linux-arm64) echo 436441f76ee2f7fe21b5b2d426d3cb261a480455a704865789ad533e4273ec26 ;;
	esac
}
mihomo_asset() {
	case "$plat" in
	darwin-amd64 | linux-amd64) echo "mihomo-$plat-compatible-v$MIHOMO.gz" ;; # runs on every amd64 CPU
	*) echo "mihomo-$plat-v$MIHOMO.gz" ;;
	esac
}
mihomo_sum() {
	case "$plat" in
	darwin-arm64) echo 1a7a20eca63fbbaec90137717465bfde488d0db2a3a79b8fadebf9b2545b6e4c ;;
	darwin-amd64) echo 703bceaa494732fa456f47e6ac2f06bf0bc5593d4c927d5f61e10f1a31039c37 ;;
	linux-amd64) echo 2763f5b414527f7baf6dc57e3f82f3ab561eedf34bfd7c2f2cde1f7e592efe06 ;;
	linux-arm64) echo daaf78a6240a282cdfd75bcbf98fe321f6bd52765cc29e79f344191198f0b50f ;;
	esac
}
xray_asset() {
	case "$plat" in
	darwin-arm64) echo Xray-macos-arm64-v8a.zip ;;
	darwin-amd64) echo Xray-macos-64.zip ;;
	linux-amd64) echo Xray-linux-64.zip ;;
	linux-arm64) echo Xray-linux-arm64-v8a.zip ;;
	esac
}
xray_sum() {
	case "$plat" in
	darwin-arm64) echo 233f0c66681cd0726f81a6b2b6ed6a5710869e312047b522e46ed53035bebd26 ;;
	darwin-amd64) echo 488963e676e06f5e48547f9dd38116aaca8de4234959b4272ae8f284e658dfd0 ;;
	linux-amd64) echo b651f7e1d9966ee2c1b1ee1bf8b52c72771824d3065dce96bbc8bcc19ed95f82 ;;
	linux-arm64) echo 64ed6858982a7f5eb85d88b811089063bd4f17561f4c8f44e0f1dc8e6f6430be ;;
	esac
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

# fetch url sum dir: downloads url once, checks its SHA-256 and leaves the archive in dir/archive
fetch() {
	local url=$1 sum=$2 dir=$3
	if [ -f "$dir/.verified" ] && [ "$(cat "$dir/.verified")" = "$sum" ]; then
		return 0
	fi
	rm -rf "$dir"
	mkdir -p "$dir"
	echo "fetch-clients: downloading $url" >&2
	curl -fsSL --proto '=https' --tlsv1.2 --retry 3 -o "$dir/archive" "$url"
	local got
	got=$(sha256 "$dir/archive")
	if [ "$got" != "$sum" ]; then
		rm -rf "$dir"
		echo "fetch-clients: $url has SHA-256 $got, expected $sum - not using it" >&2
		exit 1
	fi
}

done_marker() { # dir sum
	rm -f "$1/archive"
	echo "$2" >"$1/.verified"
}

singbox() { # version
	local v=$1 dir="$cache/sing-box-$1" sum
	sum=$(singbox_sum "$v")
	if [ ! -f "$dir/.verified" ] || [ "$(cat "$dir/.verified")" != "$sum" ]; then
		fetch "https://github.com/SagerNet/sing-box/releases/download/v$v/$(singbox_asset "$v")" "$sum" "$dir"
		tar -xzf "$dir/archive" -C "$dir" --strip-components 1
		done_marker "$dir" "$sum"
	fi
	echo "$dir/sing-box"
}

mihomo() {
	local dir="$cache/mihomo-$MIHOMO" sum
	sum=$(mihomo_sum)
	if [ ! -f "$dir/.verified" ] || [ "$(cat "$dir/.verified")" != "$sum" ]; then
		fetch "https://github.com/MetaCubeX/mihomo/releases/download/v$MIHOMO/$(mihomo_asset)" "$sum" "$dir"
		gunzip -c "$dir/archive" >"$dir/mihomo"
		chmod 0755 "$dir/mihomo"
		done_marker "$dir" "$sum"
	fi
	echo "$dir/mihomo"
}

xray() {
	local dir="$cache/xray-$XRAY" sum
	sum=$(xray_sum)
	if [ ! -f "$dir/.verified" ] || [ "$(cat "$dir/.verified")" != "$sum" ]; then
		fetch "https://github.com/XTLS/Xray-core/releases/download/v$XRAY/$(xray_asset)" "$sum" "$dir"
		unzip -q -o "$dir/archive" -d "$dir"
		chmod 0755 "$dir/xray"
		done_marker "$dir" "$sum"
	fi
	echo "$dir/xray"
}

mkdir -p "$cache"
sb_old=$(singbox "$SINGBOX_OLD")
sb_new=$(singbox "$SINGBOX_NEW")
mh=$(mihomo)
xr=$(xray)

if [ "$env_only" = 1 ]; then
	echo "export MERIDIAN_TEST_SINGBOX='$sb_old:$sb_new'"
	echo "export MERIDIAN_TEST_MIHOMO='$mh'"
	echo "export MERIDIAN_TEST_XRAY='$xr'"
	exit 0
fi
"$sb_new" version | head -n 1
"$mh" -v | head -n 1
"$xr" version | head -n 1
cat <<EOF

Ready. Run the checks with:

  export MERIDIAN_TEST_SINGBOX='$sb_old:$sb_new'
  export MERIDIAN_TEST_MIHOMO='$mh'
  export MERIDIAN_TEST_XRAY='$xr'
  MERIDIAN_NO_GEO_DOWNLOAD=1 go test ./internal/panel -run TestFormatsInRealClients -v
EOF
