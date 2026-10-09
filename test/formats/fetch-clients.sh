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
SINGBOX_NEW=1.14.3
MIHOMO=1.19.32
XRAY=26.3.27

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
	1.14.3/darwin-arm64) echo 0360ce634a04c26a6b4fd957517987bb4f6c8950f63435ce3c5af2fcae5860c5 ;;
	1.14.3/darwin-amd64) echo 6d002b74d03b478547349cc10af89e007868596e5aa931816d45da9f544715a9 ;;
	1.14.3/linux-amd64) echo e2bdf179c15a3652955dc44867e33ca0965c9c9b91b34267dcc5a5d639a5feee ;;
	1.14.3/linux-arm64) echo 29dae24d76bea3bc62d07dd3e60cfc12003de7debbd6b768df9be9c68360174e ;;
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
	darwin-arm64) echo 3312a6780652c622890fd4357c6a853bbf865464fd047ac7b7f52dab8de18652 ;;
	darwin-amd64) echo 18b382df77bded2ad0fb3db27db5636cb15b20729d5ba995a357eeb9b46bf507 ;;
	linux-amd64) echo ba3ce607747a07f948fc35780e108a4a7c7f552a38b9bd4d115f313ebcb89c20 ;;
	linux-arm64) echo 9dd862e28b46ff7d775f169cceebc28deccaa0a9e804237d421cd2571e0caba0 ;;
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
	darwin-arm64) echo 2e93a67e8aa1936ecefb307e120830fcbd4c643ab9b1c46a2d0838d5f8409eaf ;;
	darwin-amd64) echo f5b0471d3459eff1b82e48af0aeac186abcc3298210070afbbbd8437a4e8b203 ;;
	linux-amd64) echo 23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae ;;
	linux-arm64) echo 4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c ;;
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
