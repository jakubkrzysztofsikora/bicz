#!/bin/sh
# Fixture tests for scripts/install.sh against a LOCAL loopback HTTP server.
# Nothing here contacts a real release host, a real GitHub repo, or $HOME.
#
#   scripts/test_install.sh [path-to-built-bicz]
# Without an argument it builds ./cmd/bicz for the host platform into a temp dir.
set -eu

here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$here")"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/bicz-installtest.XXXXXX")"
server_pid=""
cleanup() {
  if [ -n "$server_pid" ]; then kill "$server_pid" 2>/dev/null || true; fi
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

pass=0
failn=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { failn=$((failn + 1)); printf 'FAIL %s\n' "$1"; }
expect_ok() { if "$@" >"$tmp/out" 2>&1; then return 0; else cat "$tmp/out"; return 1; fi; }

bin="${1:-}"
if [ -z "$bin" ]; then
  bin="$tmp/bicz-built"
  (cd "$root" && go build -trimpath -ldflags "-X main.version=v0.0.0-fixture" -o "$bin" ./cmd/bicz)
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in x86_64 | amd64) arch=amd64 ;; *) arch=arm64 ;; esac
asset="bicz_${os}_${arch}.tar.gz"

sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

# Fixture release directories served over http://127.0.0.1.
good="$tmp/srv/good"
mkdir -p "$good" "$tmp/stage"
cp "$bin" "$tmp/stage/bicz"
tar -czf "$good/$asset" -C "$tmp/stage" bicz
printf '%s  %s\n%s  %s\n' "$(sha "$good/$asset")" "$asset" "0000000000000000000000000000000000000000000000000000000000000000" "bicz_other_platform.tar.gz" >"$good/SHA256SUMS"

bad_sum="$tmp/srv/badsum"
mkdir -p "$bad_sum"
cp "$good/$asset" "$bad_sum/$asset"
printf '%s  %s\n' "1111111111111111111111111111111111111111111111111111111111111111" "$asset" >"$bad_sum/SHA256SUMS"

tampered="$tmp/srv/tampered"
mkdir -p "$tampered"
cp "$good/SHA256SUMS" "$tampered/SHA256SUMS"
printf 'not the archive that was checksummed' >"$tampered/$asset"

nosum="$tmp/srv/noentry"
mkdir -p "$nosum"
cp "$good/$asset" "$nosum/$asset"
printf '%s  %s\n' "$(sha "$good/$asset")" "bicz_some_other.tar.gz" >"$nosum/SHA256SUMS"

port_file="$tmp/port"
cat >"$tmp/server.py" <<'PY'
import http.server, socketserver, sys
class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *a): pass
with socketserver.TCPServer(("127.0.0.1", 0), Quiet) as s:
    open(sys.argv[1], "w").write(str(s.server_address[1]))
    s.serve_forever()
PY
cd "$tmp/srv"
python3 "$tmp/server.py" "$port_file" &
server_pid=$!
cd "$root"
i=0
while [ ! -s "$port_file" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
[ -s "$port_file" ] || { echo "fixture server did not start"; exit 1; }
base="http://127.0.0.1:$(cat "$port_file")"

run() { # run <dir> <srv> [extra env...] -> runs the installer with isolated HOME
  dest="$1"; srv="$2"; shift 2
  env -i PATH="$PATH" HOME="$tmp/home" TMPDIR="$tmp" BICZ_INSTALL_DIR="$dest" BICZ_BASE_URL="$base/$srv" "$@" sh "$here/install.sh"
}
mkdir -p "$tmp/home"

# 1. success: verified install, runnable binary
d="$tmp/t1/bin"
if expect_ok run "$d" good && [ -x "$d/bicz" ] && "$d/bicz" version | grep -q '^bicz '; then ok "verified install produces a runnable binary"; else bad "verified install"; fi

# 2. idempotent re-run keeps a rollback copy, no stray staging files
if expect_ok run "$d" good && [ -f "$d/bicz.previous" ] && ! ls "$d"/.bicz.new.* >/dev/null 2>&1; then ok "re-run is clean and keeps bicz.previous"; else bad "idempotent re-run"; fi

# 3. checksum mismatch: rejected, existing binary byte-identical
d3="$tmp/t3/bin"; mkdir -p "$d3"; printf 'OLD-BINARY' >"$d3/bicz"; chmod 755 "$d3/bicz"
if run "$d3" badsum >"$tmp/out" 2>&1; then bad "checksum mismatch must fail"; else
  if grep -q 'checksum mismatch' "$tmp/out" && [ "$(cat "$d3/bicz")" = "OLD-BINARY" ] && [ ! -e "$d3/bicz.previous" ]; then ok "checksum mismatch rejected, old binary untouched"; else cat "$tmp/out"; bad "mismatch side effects"; fi
fi

# 4. tampered archive vs honest SHA256SUMS: rejected on a fresh dir, nothing created
d4="$tmp/t4/bin"
if run "$d4" tampered >"$tmp/out" 2>&1; then bad "tampered archive must fail"; else
  if grep -q 'checksum mismatch' "$tmp/out" && [ ! -e "$d4/bicz" ]; then ok "tampered archive rejected, nothing installed"; else cat "$tmp/out"; bad "tampered side effects"; fi
fi

# 5. no checksum entry for the asset
d5="$tmp/t5/bin"
if run "$d5" noentry >"$tmp/out" 2>&1; then bad "missing checksum entry must fail"; else
  if grep -q 'no entry for' "$tmp/out" && [ ! -e "$d5/bicz" ]; then ok "missing SHA256SUMS entry rejected"; else cat "$tmp/out"; bad "noentry side effects"; fi
fi

# 7. plain http to a non-loopback host is refused before any request
if env -i PATH="$PATH" HOME="$tmp/home" BICZ_INSTALL_DIR="$tmp/t7" BICZ_BASE_URL="http://example.invalid/rel" sh "$here/install.sh" >"$tmp/out" 2>&1; then bad "non-https must fail"; else
  if grep -q 'non-https' "$tmp/out"; then ok "non-https non-loopback source refused"; else cat "$tmp/out"; bad "non-https message"; fi
fi

# 8. malformed BICZ_REPO
if env -i PATH="$PATH" HOME="$tmp/home" BICZ_INSTALL_DIR="$tmp/t8" BICZ_REPO='a/b/c' sh "$here/install.sh" >"$tmp/out" 2>&1; then bad "bad repo must fail"; else ok "malformed BICZ_REPO refused"; fi

# 9. symlinked target is refused, link target untouched
d9="$tmp/t9/bin"; mkdir -p "$d9"; printf 'VICTIM' >"$tmp/victim"; ln -s "$tmp/victim" "$d9/bicz"
if run "$d9" good >"$tmp/out" 2>&1; then bad "symlink target must fail"; else
  if [ "$(cat "$tmp/victim")" = "VICTIM" ] && grep -q symlink "$tmp/out"; then ok "symlinked target refused, victim untouched"; else cat "$tmp/out"; bad "symlink side effects"; fi
fi

# 10. symlinked install dir is refused
mkdir -p "$tmp/real"; ln -s "$tmp/real" "$tmp/linkdir"
if run "$tmp/linkdir" good >"$tmp/out" 2>&1; then bad "symlink dir must fail"; else
  if [ ! -e "$tmp/real/bicz" ]; then ok "symlinked install dir refused"; else bad "symlink dir wrote through"; fi
fi

# 11. missing asset on the server (404)
if run "$tmp/t11/bin" does-not-exist >"$tmp/out" 2>&1; then bad "404 must fail"; else ok "unreachable release path fails closed"; fi

# --- hostile archives: honest SHA256SUMS, malicious members -------------------------------
cat >"$tmp/mktar.py" <<'PY'
import hashlib, io, os, sys, tarfile
out, asset, case, victim = sys.argv[1:5]
os.makedirs(out, exist_ok=True)
def reg(tf, name, data=b"BINARY"):
    info = tarfile.TarInfo(name); info.size = len(data); info.mode = 0o755
    tf.addfile(info, io.BytesIO(data))
def link(tf, name, target, hard=False):
    info = tarfile.TarInfo(name); info.type = tarfile.LNKTYPE if hard else tarfile.SYMTYPE; info.linkname = target
    tf.addfile(info)
path = os.path.join(out, asset)
with tarfile.open(path, "w:gz") as tf:
    if case == "traversal": reg(tf, "bicz"); reg(tf, "../escape")
    elif case == "absolute": reg(tf, "bicz"); reg(tf, os.path.join(os.path.dirname(out), "abs-escape"))
    elif case == "nested": reg(tf, "x/bicz")
    elif case == "symlink": link(tf, "bicz", victim)
    elif case == "hardlink": reg(tf, "LICENSE"); link(tf, "bicz", "LICENSE", hard=True)
    elif case == "extra": reg(tf, "bicz"); reg(tf, "evil")
    elif case == "dup": reg(tf, "bicz"); reg(tf, "bicz")
    elif case == "dotslash": reg(tf, "./bicz"); reg(tf, "./LICENSE")
    elif case == "marker": reg(tf, "bicz", b"#!/bin/sh\ntouch " + victim.encode() + b"\n")
    else: raise SystemExit("unknown case " + case)
sha = hashlib.sha256(open(path, "rb").read()).hexdigest()
open(os.path.join(out, "SHA256SUMS"), "w").write(sha + "  " + asset + "\n")
PY
printf 'VICTIM' >"$tmp/victim-target"
for case in traversal absolute nested symlink hardlink extra dup; do
  python3 "$tmp/mktar.py" "$tmp/srv/h-$case" "$asset" "$case" "$tmp/victim-target"
  dh="$tmp/h-$case/dest/bin"
  if run "$dh" "h-$case" >"$tmp/out" 2>&1; then bad "hostile archive '$case' must be rejected"; else
    if [ ! -e "$dh/bicz" ] && [ ! -e "$tmp/h-$case/escape" ] && [ ! -e "$tmp/h-$case/dest/escape" ] && [ ! -e "$tmp/srv/escape" ] \
      && [ ! -e "$tmp/srv/abs-escape" ] && [ ! -e "$tmp/evil" ] && [ "$(cat "$tmp/victim-target")" = "VICTIM" ]; then
      ok "hostile archive '$case' rejected before extraction, nothing written"
    else cat "$tmp/out"; bad "hostile '$case' side effects"; fi
  fi
done
if find "$tmp" \( -name escape -o -name abs-escape -o -name evil \) -not -path "$tmp/srv/*" | grep -q .; then bad "traversal members were written somewhere under the temp tree"; else ok "no traversal member was written anywhere under the temp tree"; fi

python3 "$tmp/mktar.py" "$tmp/srv/h-dotslash" "$asset" dotslash "$tmp/victim-target"
if expect_ok run "$tmp/h-dotslash/bin" h-dotslash && [ -f "$tmp/h-dotslash/bin/bicz" ]; then ok "./bicz + ./LICENSE layout is accepted"; else bad "dotslash layout"; fi

python3 "$tmp/mktar.py" "$tmp/srv/h-marker" "$asset" marker "$tmp/exec-marker"
if expect_ok run "$tmp/h-marker/bin" h-marker && [ ! -e "$tmp/exec-marker" ]; then ok "installer never executes the downloaded binary"; else bad "installer executed the downloaded binary or failed"; fi

printf '\n%d passed, %d failed\n' "$pass" "$failn"
[ "$failn" -eq 0 ]
