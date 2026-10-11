#!/bin/sh
# Build release archives + SHA256SUMS into ./dist (or $2). No network, no publish.
#   scripts/build_release.sh vX.Y.Z [outdir]
# Targets: darwin/linux/windows x amd64/arm64. Asset names match scripts/install.*:
#   bicz_<os>_<arch>.tar.gz (zip on windows), plus SHA256SUMS.
set -eu

version="${1:?usage: build_release.sh VERSION [outdir]}"
root="$(cd "$(dirname "$0")/.." && pwd)"
out="${2:-$root/dist}"
commit="$(git -C "$root" rev-parse --verify --quiet HEAD 2>/dev/null || true)"
date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

command -v zip >/dev/null 2>&1 || { echo "build_release: need zip" >&2; exit 1; }
rm -rf "$out"
mkdir -p "$out"
stage="$(mktemp -d "${TMPDIR:-/tmp}/bicz-release.XXXXXX")"
trap 'rm -rf "$stage"' EXIT

for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"
  arch="${target#*/}"
  exe=bicz
  [ "$os" = windows ] && exe=bicz.exe
  dir="$stage/$os-$arch"
  mkdir -p "$dir"
  (cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version -X main.commit=$commit -X main.date=$date" \
    -o "$dir/$exe" ./cmd/bicz)
  cp "$root/LICENSE" "$dir/LICENSE"
  if [ "$os" = windows ]; then
    (cd "$dir" && zip -q "$out/bicz_${os}_${arch}.zip" "$exe" LICENSE)
  else
    tar -czf "$out/bicz_${os}_${arch}.tar.gz" -C "$dir" "$exe" LICENSE
  fi
  echo "built bicz_${os}_${arch}"
done

# The installers ship as release assets so `releases/latest/download/install.sh` resolves.
cp "$root/scripts/install.sh" "$root/scripts/install.ps1" "$out/"

if [ -n "$commit" ] && git -C "$root" diff --quiet HEAD -- 2>/dev/null; then
  git -C "$root" archive --format=tar.gz --prefix="bicz-$version/" -o "$out/bicz_source.tar.gz" HEAD
fi

(
  cd "$out"
  if command -v sha256sum >/dev/null 2>&1; then sha256sum ./* ; else shasum -a 256 ./*; fi |
    sed 's#  \./#  #' | grep -v '  SHA256SUMS$' | sort -k2 >SHA256SUMS
)
echo "wrote $out:"
ls -l "$out"
