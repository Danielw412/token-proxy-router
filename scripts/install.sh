#!/bin/sh
# Token Proxy Router installer for Linux and macOS.
#
#   Hub:     curl -fsSL <release-url>/install.sh | sh
#   Client:  curl -fsSL <release-url>/install.sh | sh -s -- connect --hub <hub-url> --key <key>
#
# Downloads the release for this machine, verifies its SHA-256 against
# checksums.txt, and places it in the data folder's bin/.
#
# TPR_HOME changes the data folder. TPR_INSTALL_BASE changes the download address
# (for testing); checksums are still verified.
set -eu

REPO="Danielw412/token-proxy-router"
BASE="${TPR_INSTALL_BASE:-https://github.com/$REPO/releases/latest/download}"
DATA_DIR="${TPR_HOME:-$HOME/.token-proxy-router}"
BIN_DIR="$DATA_DIR/bin"

fail() {
	echo "install: $1" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required. Install it, then run this again."
}

need uname
need mktemp

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "this script is for Linux and macOS. On Windows use install.ps1." ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "no release is built for $(uname -m)." ;;
esac

asset="tpr-$os-$arch"

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "curl or wget is required."
	fi
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		fail "sha256sum or shasum is required to verify the download."
	fi
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

echo "Downloading $asset ..."
fetch "$BASE/$asset" "$tmp_dir/$asset" || fail "could not download $BASE/$asset. Check your connection, and that a release exists."
fetch "$BASE/checksums.txt" "$tmp_dir/checksums.txt" || fail "could not download checksums.txt."

want="$(awk -v name="$asset" '$2 == name || $2 == "*" name {print $1; exit}' "$tmp_dir/checksums.txt")"
[ -n "$want" ] || fail "checksums.txt does not list $asset, so the download cannot be verified."
got="$(sha256_of "$tmp_dir/$asset")"
[ "$want" = "$got" ] || fail "checksum mismatch for $asset: the download is damaged or was tampered with. Nothing was installed."
echo "Checksum verified."

mkdir -p "$BIN_DIR"
chmod 700 "$DATA_DIR" "$BIN_DIR" 2>/dev/null || true
chmod 755 "$tmp_dir/$asset"
# The move lands on the same disk, so a running copy is replaced in one step.
mv -f "$tmp_dir/$asset" "$BIN_DIR/.tpr-new"
mv -f "$BIN_DIR/.tpr-new" "$BIN_DIR/tpr"

tpr="$BIN_DIR/tpr"
if [ "$#" -eq 0 ]; then
	exec "$tpr" install
fi
if [ "$1" = "connect" ]; then
	# A machine that only uses a hub needs no service.
	"$tpr" install --no-service
fi
exec "$tpr" "$@"
