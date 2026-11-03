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

# fetch URL FILE [progress]
# With "progress" and a terminal, shows the download's speed and time left. A
# connection that stops moving fails rather than waiting forever.
fetch() {
	show=""
	if [ "${3:-}" = "progress" ] && [ -t 2 ]; then
		show=yes
	fi
	if command -v curl >/dev/null 2>&1; then
		if [ -n "$show" ]; then
			curl -fSL --retry 3 --connect-timeout 30 --speed-limit 1024 --speed-time 120 -o "$2" "$1"
		else
			curl -fsSL --retry 3 --connect-timeout 30 --speed-limit 1024 --speed-time 120 -o "$2" "$1"
		fi
	elif command -v wget >/dev/null 2>&1; then
		if [ -n "$show" ]; then
			wget -T 120 -O "$2" "$1"
		else
			wget -q -T 120 -O "$2" "$1"
		fi
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

checksum_of() {
	awk -v name="$1" '$2 == name || $2 == "*" name {print $1; exit}' "$tmp_dir/checksums.txt"
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
# Ctrl-C ends the install here, rather than reading as a failed download.
trap 'exit 130' INT TERM

fetch "$BASE/checksums.txt" "$tmp_dir/checksums.txt" || fail "could not download $BASE/checksums.txt. Check your connection, and that a release exists."

# The compressed build is about a third of the size.
download="$asset.gz"
want="$(checksum_of "$download")"
if [ -z "$want" ] || ! command -v gzip >/dev/null 2>&1; then
	download="$asset"
	want="$(checksum_of "$download")"
fi
[ -n "$want" ] || fail "checksums.txt does not list $asset, so the download cannot be verified."

echo "Downloading $download ..."
fetch "$BASE/$download" "$tmp_dir/$download" progress || fail "could not download $BASE/$download. Check your connection, and that a release exists."

got="$(sha256_of "$tmp_dir/$download")"
[ "$want" = "$got" ] || fail "checksum mismatch for $download: the download is damaged or was tampered with. Nothing was installed."
echo "Checksum verified."

if [ "$download" != "$asset" ]; then
	gzip -dc "$tmp_dir/$download" >"$tmp_dir/$asset" || fail "could not unpack $download. Nothing was installed."
fi

mkdir -p "$BIN_DIR"
chmod 700 "$DATA_DIR" "$BIN_DIR" 2>/dev/null || true
chmod 755 "$tmp_dir/$asset"
# The move lands on the same disk, so a running copy is replaced in one step.
mv -f "$tmp_dir/$asset" "$BIN_DIR/.tpr-new"
mv -f "$BIN_DIR/.tpr-new" "$BIN_DIR/tpr"
# exec below replaces this shell, so the exit trap will not get to do this.
rm -rf "$tmp_dir"

tpr="$BIN_DIR/tpr"
if [ "$#" -eq 0 ]; then
	exec "$tpr" install
fi
if [ "$1" = "connect" ]; then
	# A machine that only uses a hub needs no service.
	"$tpr" install --no-service
fi
exec "$tpr" "$@"
