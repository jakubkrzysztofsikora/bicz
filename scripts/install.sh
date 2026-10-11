#!/bin/sh
# bicz installer (macOS / Linux). User-scope only: no sudo, no shell-rc edits.
#
# It downloads a release archive plus its SHA256SUMS file, verifies the archive
# checksum, and only then replaces $BICZ_INSTALL_DIR/bicz atomically. A
# checksum mismatch, a missing checksum line, or an unsafe target aborts with
# the existing install untouched.
#
# Where releases come from is never guessed. Set one of:
#   BICZ_REPO=owner/repo   GitHub repository that publishes the releases
#   BICZ_BASE_URL=URL      directory that directly contains the archive and
#                          SHA256SUMS (https, or http on loopback for tests)
#
# Other settings:
#   BICZ_VERSION=vX.Y.Z    release tag (default: the repository's latest release;
#                          ignored with BICZ_BASE_URL)
#   BICZ_INSTALL_DIR=DIR   default: $HOME/.local/bin
#   BICZ_OS / BICZ_ARCH    override platform detection (linux|darwin, amd64|arm64)
#
# The checksum protects against a corrupted or swapped archive relative to the
# SHA256SUMS served from the same origin. It is not a signature and does not
# authenticate the publisher.
set -eu

fail() {
  printf 'bicz-install: %s\n' "$*" >&2
  exit 1
}

if [ "$(id -u)" = "0" ]; then
  fail "refusing to run as root: bicz is a user-scope tool; run as your normal user"
fi

: "${HOME:?HOME is not set}"
INSTALL_DIR="${BICZ_INSTALL_DIR:-$HOME/.local/bin}"

# --- platform ----------------------------------------------------------------
os="${BICZ_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
case "$os" in
  linux | darwin) ;;
  *) fail "unsupported OS '$os' (this script handles linux and darwin; use install.ps1 on Windows)" ;;
esac
arch="${BICZ_ARCH:-$(uname -m)}"
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported architecture '$arch'" ;;
esac
asset="bicz_${os}_${arch}.tar.gz"

# --- source ------------------------------------------------------------------
if [ -n "${BICZ_BASE_URL:-}" ]; then
  base="${BICZ_BASE_URL%/}"
elif [ -n "${BICZ_REPO:-}" ]; then
  case "$BICZ_REPO" in
    */*/* | */ | /* | *[!A-Za-z0-9._/-]*) fail "BICZ_REPO must look like owner/repo" ;;
    */*) ;;
    *) fail "BICZ_REPO must look like owner/repo" ;;
  esac
  if [ -n "${BICZ_VERSION:-}" ]; then
    base="https://github.com/$BICZ_REPO/releases/download/$BICZ_VERSION"
  else
    base="https://github.com/$BICZ_REPO/releases/latest/download"
  fi
else
  fail "no release source: set BICZ_REPO=owner/repo (or BICZ_BASE_URL). The repository is not assumed."
fi
case "$base" in
  https://*) ;;
  http://127.0.0.1[:/]* | http://localhost[:/]* | http://\[::1\][:/]*) ;;
  *) fail "refusing non-https release source: $base" ;;
esac

# --- tools -------------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  download() { curl --fail --silent --show-error --location --proto '=https,http' --max-time 120 --output "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  download() { wget --quiet --timeout=120 --output-document="$2" "$1"; }
else
  fail "need curl or wget"
fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "need sha256sum or shasum"
fi
command -v tar >/dev/null 2>&1 || fail "need tar"

# --- target safety (before downloading anything) ------------------------------
target="$INSTALL_DIR/bicz"
if [ -L "$INSTALL_DIR" ]; then
  fail "install dir $INSTALL_DIR is a symlink; refusing"
fi
if [ -L "$target" ]; then
  fail "$target is a symlink; refusing to replace it"
fi
if [ -e "$target" ] && [ ! -f "$target" ]; then
  fail "$target exists and is not a regular file; refusing"
fi

# --- download + verify ---------------------------------------------------------
work="$(mktemp -d "${TMPDIR:-/tmp}/bicz-install.XXXXXX")"
trap 'rm -rf "$work"' EXIT INT TERM HUP

download "$base/SHA256SUMS" "$work/SHA256SUMS" || fail "could not download $base/SHA256SUMS"
download "$base/$asset" "$work/$asset" || fail "could not download $base/$asset"

want="$(awk -v name="$asset" '{ f = $2; sub(/^\*/, "", f); if (f == name) { print $1; exit } }' "$work/SHA256SUMS")"
[ -n "$want" ] || fail "SHA256SUMS has no entry for $asset; refusing to install"
got="$(sha256 "$work/$asset")"
if [ "$want" != "$got" ]; then
  fail "checksum mismatch for $asset: SHA256SUMS says $want, downloaded file is $got; nothing installed"
fi

# Validate the member list BEFORE extracting anything. The archive must hold
# exactly one regular file named bicz (plus an optional regular LICENSE), with no
# absolute paths, "..", nested directories, symlinks, or hard links. Only the
# validated bicz member is then extracted, by exact name. This is a guard for the
# common malicious-archive paths, not a containment boundary.
tar -tzf "$work/$asset" >"$work/names" 2>/dev/null || fail "could not list $asset"
tar -tvzf "$work/$asset" >"$work/types" 2>/dev/null || fail "could not list $asset"
[ "$(wc -l <"$work/names")" -eq "$(wc -l <"$work/types")" ] || fail "unexpected archive listing for $asset; refusing"
member=""
nbicz=0
nline=0
while IFS= read -r name; do
  nline=$((nline + 1))
  kind="$(sed -n "${nline}p" "$work/types" | cut -c1)"
  case "$name" in
    bicz | ./bicz)
      nbicz=$((nbicz + 1))
      member="$name"
      [ "$kind" = "-" ] || fail "archive member '$name' is not a regular file; refusing"
      ;;
    LICENSE | ./LICENSE)
      [ "$kind" = "-" ] || fail "archive member '$name' is not a regular file; refusing"
      ;;
    *) fail "unexpected archive member '$name'; refusing (expected only bicz and LICENSE)" ;;
  esac
done <"$work/names"
[ "$nbicz" -eq 1 ] || fail "archive must contain exactly one bicz binary; refusing"
mkdir "$work/x"
tar -xzf "$work/$asset" -C "$work/x" "$member" || fail "could not extract $asset"
[ -f "$work/x/bicz" ] && [ ! -L "$work/x/bicz" ] || fail "extracted bicz is not a regular file"
chmod 755 "$work/x/bicz"

# --- atomic install -------------------------------------------------------------
mkdir -p "$INSTALL_DIR"
[ -L "$INSTALL_DIR" ] && fail "install dir $INSTALL_DIR is a symlink; refusing"
if [ -f "$target" ]; then
  cp -p "$target" "$INSTALL_DIR/bicz.previous"
fi
staged="$INSTALL_DIR/.bicz.new.$$"
cp "$work/x/bicz" "$staged"
chmod 755 "$staged"
mv -f "$staged" "$target"

printf 'bicz-install: installed %s (sha256 %s)\n' "$target" "$got"
if [ -f "$INSTALL_DIR/bicz.previous" ]; then
  printf 'bicz-install: previous binary kept at %s (rollback: mv it back over bicz)\n' "$INSTALL_DIR/bicz.previous"
fi
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) printf 'bicz-install: %s is not on PATH; add it to your shell profile yourself (this script does not edit it)\n' "$INSTALL_DIR" ;;
esac
printf 'bicz-install: the downloaded binary was not executed by this script; run: %s version\n' "$target"
