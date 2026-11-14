#!/bin/sh
# Install seavxrouter from GitHub Releases.
set -eu

REPO="seaavey/SeavxRouter"
BIN="seavxrouter"
VERSION="${SEAVX_VERSION:-latest}"
INSTALL_DIR="${SEAVX_INSTALL_DIR:-}"

usage() {
	cat <<'EOF'
Install seavxrouter from GitHub Releases.

Usage: install.sh [options]

Options:
  --version TAG   release tag to install (default: latest)
  --dir DIR       install directory (default: $PREFIX/bin on Termux,
                  ~/.local/bin elsewhere)
  -h, --help      show this help

Environment:
  SEAVX_VERSION       same as --version
  SEAVX_INSTALL_DIR   same as --dir

Examples:
  curl -fsSL https://raw.githubusercontent.com/seaavey/SeavxRouter/main/install.sh | sh
  ./install.sh --version v0.1.1 --dir ~/bin
EOF
}

fail() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	-h | --help)
		usage
		exit 0
		;;
	--version)
		[ $# -ge 2 ] || fail "--version needs a tag"
		VERSION="$2"
		shift 2
		;;
	--version=*)
		VERSION="${1#*=}"
		shift
		;;
	--dir)
		[ $# -ge 2 ] || fail "--dir needs a path"
		INSTALL_DIR="$2"
		shift 2
		;;
	--dir=*)
		INSTALL_DIR="${1#*=}"
		shift
		;;
	*)
		usage >&2
		fail "unknown option: $1"
		;;
	esac
done

case "$VERSION" in
latest | v*) ;;
*) VERSION="v$VERSION" ;;
esac

os=""
case "$(uname -s)" in
Linux*) os="linux" ;;
Darwin*) os="darwin" ;;
FreeBSD*) os="freebsd" ;;
MINGW* | MSYS* | CYGWIN*) os="windows" ;;
*) fail "unsupported operating system: $(uname -s)" ;;
esac

arch=""
case "$(uname -m)" in
x86_64 | amd64) arch="amd64" ;;
aarch64 | arm64) arch="arm64" ;;
armv7l | armv7* | armv8l) arch="armv7" ;;
i386 | i486 | i586 | i686) arch="386" ;;
riscv64) arch="riscv64" ;;
*) fail "unsupported architecture: $(uname -m)" ;;
esac

case "$os-$arch" in
linux-amd64 | linux-arm64 | linux-armv7 | linux-386 | linux-riscv64) ;;
darwin-amd64 | darwin-arm64) ;;
windows-amd64 | windows-arm64 | windows-386) ;;
freebsd-amd64 | freebsd-arm64) ;;
*) fail "no prebuilt binary for $os/$arch" ;;
esac

asset="$BIN-$os-$arch"
if [ "$os" = "windows" ]; then
	asset="$asset.exe"
fi

if [ "$VERSION" = "latest" ]; then
	base="https://github.com/$REPO/releases/latest/download"
else
	base="https://github.com/$REPO/releases/download/$VERSION"
fi

fetch() {
	url="$1"
	out="$2"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 15 -o "$out" "$url"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$out" "$url" || fail "download failed: $url"
	else
		fail "curl or wget is required"
	fi
}

if [ -z "$INSTALL_DIR" ]; then
	if [ -n "${PREFIX:-}" ] && [ -d "$PREFIX/bin" ] && [ -w "$PREFIX/bin" ]; then
		INSTALL_DIR="$PREFIX/bin"
	else
		INSTALL_DIR="$HOME/.local/bin"
	fi
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/seavxrouter.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

printf 'downloading %s (%s)\n' "$asset" "$VERSION"
fetch "$base/$asset" "$tmp/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt"

expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || fail "no checksum for $asset in checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
else
	fail "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $asset"

mkdir -p "$INSTALL_DIR"
install_path="$INSTALL_DIR/$BIN"
if [ "$os" = "windows" ]; then
	install_path="$install_path.exe"
fi
cp "$tmp/$asset" "$install_path"
chmod 0755 "$install_path"

printf 'installed %s\n' "$install_path"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*)
	printf '\nAdd %s to PATH:\n  export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR"
	;;
esac

printf '\nnext:\n  %s login antigravity\n  %s -listen 127.0.0.1:20128\n' "$BIN" "$BIN"
