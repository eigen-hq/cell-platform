#!/usr/bin/env bash
# Tests for lib.sh with a fake kubectl. Run: deploy/lib_test.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Fake kubectl: behaviour comes from $FAKE (notfound | down | value). Calls go
# to $FAKE_LOG, env files passed via --from-env-file to $FAKE_ENV.
cat >"$tmp/kubectl" <<'EOF'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_LOG"
for a in "$@"; do
  case $a in --from-env-file=*) cat "${a#--from-env-file=}" >>"$FAKE_ENV" ;; esac
done
case "$FAKE:$*" in
  *--dry-run=client*) echo "kind: Secret" ;;
  *"create -f -"*) cat >/dev/null ;;
  notfound:*) echo 'Error from server (NotFound): secrets "x" not found' >&2; exit 1 ;;
  down:*) echo 'The connection to the server was refused' >&2; exit 1 ;;
  value:*get*) printf '%s' "$(printf 'secret-pw' | base64)" ;;
esac
EOF
chmod +x "$tmp/kubectl"
export PATH="$tmp:$PATH" FAKE_LOG="$tmp/log" FAKE_ENV="$tmp/env"
source "$here/lib.sh"

fails=0
check() { # description, expected, actual
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fails=$((fails + 1)); fi
}

export FAKE=value; check "secret_value decodes" "secret-pw" "$(secret_value ns s password)"
export FAKE=notfound; check "secret_value on missing secret prints nothing" "" "$(secret_value ns s password)"
export FAKE=down; check "secret_value aborts on API error" "1" "$( (secret_value ns s password >/dev/null 2>&1) && echo 0 || echo 1)"

: >"$FAKE_LOG"
check "ensure_secret aborts on API error" "1" "$( (ensure_secret ns s Opaque a=b >/dev/null 2>&1) && echo 0 || echo 1)"
check "ensure_secret does not create after API error" "0" "$(grep -c create "$FAKE_LOG" || true)"
: >"$FAKE_LOG"
: >"$FAKE_ENV"
export FAKE=notfound
ensure_secret ns s Opaque user=a password=hunter2
check "ensure_secret creates a missing secret" "1" "$(grep -c 'create -f -' "$FAKE_LOG")"
check "ensure_secret passes values via env file" "user=a password=hunter2" "$(paste -sd' ' "$FAKE_ENV")"
check "ensure_secret keeps values out of argv" "0" "$(grep -c hunter2 "$FAKE_LOG" || true)"

BASE_DIR=$tmp
printf 'host: ${CELL_DOMAIN}\n' >"$tmp/t.yaml"
export CELL_DOMAIN=example.org; check "render substitutes" "host: example.org" "$(render t.yaml)"
check "render fails on unset variable" "1" "$( (unset CELL_DOMAIN; render t.yaml >/dev/null 2>&1) && echo 0 || echo 1)"

export RETRY_ATTEMPTS=3 RETRY_DELAY=0
succeed_on() { echo x >>"$tmp/attempts"; [ "$(wc -l <"$tmp/attempts")" -ge "$1" ]; }
: >"$tmp/attempts"
check "retry succeeds on a middle attempt" "0" "$(retry succeed_on 2 && echo 0 || echo 1)"
: >"$tmp/attempts"
check "retry succeeds on the last attempt" "0" "$(retry succeed_on 3 2>/dev/null && echo 0 || echo 1)"
: >"$tmp/attempts"
check "retry fails when every attempt fails" "1" "$(retry succeed_on 4 2>/dev/null && echo 0 || echo 1)"
check "retry stops after the last attempt" "3" "$(wc -l <"$tmp/attempts" | tr -d ' ')"

# build_image with fake docker and k3d: $FAKE_DOCKER is ok | fail.
mkdir -p "$tmp/src/cmd" && echo 'package main' >"$tmp/src/cmd/main.go"
cat >"$tmp/docker" <<'EOF'
#!/usr/bin/env bash
echo "docker $*" >>"$FAKE_LOG"
case "$FAKE_DOCKER:$1" in
  *:image) exit 1 ;;                       # never built before
  fail:build) echo "build failed" >&2; exit 1 ;;
  ok:build) exit 0 ;;
  *:exec) exit 0 ;;                          # image not in the cell yet
esac
EOF
cat >"$tmp/k3d" <<'EOF'
#!/usr/bin/env bash
echo "k3d $*" >>"$FAKE_LOG"
[ "$FAKE_K3D" = ok ]
EOF
chmod +x "$tmp/docker" "$tmp/k3d"
: >"$FAKE_LOG"
out=$(FAKE_DOCKER=fail FAKE_K3D=ok build_image app op "$tmp/src" c cmd 2>/dev/null) && rc=0 || rc=$?
check "build_image fails when the build fails" "1:" "$rc:$out"
check "build_image does not import a failed build" "0" "$(grep -c '^k3d' "$FAKE_LOG" || true)"
out=$(FAKE_DOCKER=ok FAKE_K3D=fail build_image app op "$tmp/src" c cmd 2>/dev/null) && rc=0 || rc=$?
check "build_image fails when the import fails" "1:" "$rc:$out"
out=$(FAKE_DOCKER=ok FAKE_K3D=ok build_image app op "$tmp/src" c cmd) && rc=0 || rc=$?
check "build_image prints the hashed reference" "0:yes" "$rc:$([[ $out =~ ^app:[0-9a-f]{12}$ ]] && echo yes || echo "no ($out)")"
out=$(FAKE_DOCKER=ok FAKE_K3D=ok build_image app op "$tmp/src" c missing-dir 2>/dev/null) && rc=0 || rc=$?
check "build_image fails on missing sources" "1:" "$rc:$out"

[ "$fails" -eq 0 ]
