#!/usr/bin/env bash
# Fetch the prebuilt liblbug for one or more target platforms and place
# it where cgo_shared.go expects it. The native libs are NOT committed
# (see .gitignore); this script is the single source of truth and is run
# by `make build`/`make test`, by CI, and by the release pipeline.
#
# Link model (see internal/thirdparty/go-ladybug/cgo_shared.go):
#   - linux / darwin : STATIC  -> lib/static/<os>-<arch>/liblbug.a
#   - windows        : DYNAMIC -> lib/dynamic/windows/{lbug_shared.dll,
#                                  liblbug_shared.dll.a}  (mingw import lib
#                                  generated from the MSVC-built DLL; the
#                                  DLL ships next to gortex.exe at runtime)
#
# Usage:
#   scripts/fetch-lbug.sh                # host os/arch
#   scripts/fetch-lbug.sh all            # every release target
#   scripts/fetch-lbug.sh linux arm64    # one explicit target
#
# Env:
#   LBUG_VERSION   liblbug release tag without the leading v (default below)
#   LBUG_VARIANT   linux static flavour: compat (default) | perf
set -euo pipefail

LBUG_VERSION="${LBUG_VERSION:-0.17.0}"
LBUG_VARIANT="${LBUG_VARIANT:-compat}"
REPO="LadybugDB/ladybug"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_LBUG_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/internal/thirdparty/go-ladybug"
LIB_STATIC="$GO_LBUG_DIR/lib/static"
LIB_DYNAMIC="$GO_LBUG_DIR/lib/dynamic"

log() { printf '\033[36m[fetch-lbug]\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31m[fetch-lbug] %s\033[0m\n' "$*" >&2; exit 1; }

download() {
	local url="$1" out="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$out" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$out" "$url"
	else
		die "need curl or wget"
	fi
}

extract() {
	local file="$1" dir="$2"
	mkdir -p "$dir"
	case "$file" in
		*.tar.gz|*.tgz) tar -xzf "$file" -C "$dir" ;;
		*.zip)          unzip -oq "$file" -d "$dir" ;;
		*) die "unknown archive: $file" ;;
	esac
}

# place_header copies lbug.h next to the cgo binding if it isn't already
# there (it is committed, so this only helps a stripped checkout).
place_header() {
	local src_root="$1"
	if [ ! -f "$GO_LBUG_DIR/lbug.h" ]; then
		local h; h="$(find "$src_root" -name lbug.h | head -1 || true)"
		if [ -n "$h" ]; then cp "$h" "$GO_LBUG_DIR/lbug.h"; log "placed lbug.h"; fi
	fi
}

fetch_static() {
	local os="$1" arch="$2" asset libarch destdir
	case "$os-$arch" in
		linux-amd64)  libarch=x86_64;  asset="liblbug-static-linux-x86_64-${LBUG_VARIANT}.tar.gz" ;;
		linux-arm64)  libarch=aarch64; asset="liblbug-static-linux-aarch64-${LBUG_VARIANT}.tar.gz" ;;
		darwin-amd64) asset="liblbug-static-osx-x86_64.tar.gz" ;;
		darwin-arm64) asset="liblbug-static-osx-arm64.tar.gz" ;;
		*) die "no static asset for $os/$arch" ;;
	esac
	destdir="$LIB_STATIC/$os-$arch"
	if [ -f "$destdir/liblbug.a" ] && [ -z "${LBUG_FORCE:-}" ]; then
		log "$os/$arch already present (LBUG_FORCE=1 to refetch)"; return 0
	fi
	local tmp; tmp="$(mktemp -d)"
	log "$os/$arch (static): $asset @ v$LBUG_VERSION"
	download "https://github.com/$REPO/releases/download/v$LBUG_VERSION/$asset" "$tmp/$asset"
	extract "$tmp/$asset" "$tmp/x"
	local a; a="$(find "$tmp/x" -name 'liblbug.a' | head -1 || true)"
	[ -n "$a" ] || die "liblbug.a not found in $asset"
	mkdir -p "$destdir"
	# Only liblbug.a goes in the static dir so `-llbug` resolves to the
	# archive (no .so/.dylib for the linker to prefer).
	cp "$a" "$destdir/liblbug.a"
	place_header "$tmp/x"
	rm -rf "$tmp"
	log "  -> $destdir/liblbug.a"
}

fetch_windows() {
	local asset="liblbug-windows-x86_64.zip" destdir="$LIB_DYNAMIC/windows"
	if [ -f "$destdir/lbug_shared.dll" ] && [ -z "${LBUG_FORCE:-}" ]; then
		log "windows/amd64 already present (LBUG_FORCE=1 to refetch)"; return 0
	fi
	local tmp; tmp="$(mktemp -d)"
	log "windows/amd64 (dynamic): $asset @ v$LBUG_VERSION"
	download "https://github.com/$REPO/releases/download/v$LBUG_VERSION/$asset" "$tmp/$asset"
	extract "$tmp/$asset" "$tmp/x"
	mkdir -p "$destdir"
	local dll; dll="$(find "$tmp/x" -name 'lbug_shared.dll' | head -1 || true)"
	[ -n "$dll" ] || die "lbug_shared.dll not found in $asset"
	# The .exe links directly against the DLL (cgo: -l:lbug_shared.dll),
	# so no import lib is needed. The DLL itself must ship next to the
	# .exe at runtime (the release windows job bundles it + the VC++
	# runtime).
	cp "$dll" "$destdir/lbug_shared.dll"
	place_header "$tmp/x"
	rm -rf "$tmp"
	log "  -> $destdir/lbug_shared.dll"
}

fetch_one() {
	local os="$1" arch="$2"
	case "$os" in
		windows) fetch_windows ;;
		linux|darwin) fetch_static "$os" "$arch" ;;
		*) die "unsupported os $os" ;;
	esac
}

# ---- target selection -----------------------------------------------------
declare -a targets=()
case "${1:-}" in
	all)
		targets=("linux amd64" "linux arm64" "darwin amd64" "darwin arm64" "windows amd64")
		;;
	""|host)
		os="$(uname -s)"; arch="$(uname -m)"
		case "$os" in
			Linux) os=linux ;; Darwin) os=darwin ;;
			MINGW*|MSYS*|CYGWIN*) os=windows ;;
			*) die "unknown host os $os" ;;
		esac
		case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; esac
		targets=("$os $arch")
		;;
	*)
		targets=("$1 ${2:-amd64}")
		;;
esac

for t in "${targets[@]}"; do
	# shellcheck disable=SC2086
	fetch_one $t
done
log "liblbug v$LBUG_VERSION ready"
