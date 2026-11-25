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
  oldtarget:*rclone*) ;;
  oldtarget:*get*) printf '%s' "$(printf 'v' | base64)" ;;
esac
EOF
chmod +x "$tmp/kubectl"
export PATH="$tmp:$PATH" FAKE_LOG="$tmp/log" FAKE_ENV="$tmp/env"
source "$here/lib.sh"

fails=0
check() { # description, expected, actual
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want '$2', got '$3'"; fails=$((fails + 1)); fi
}

applied=$'configmap/cell-mesh configured\ndeployment.apps/cell-mesh unchanged\nsecret/mesh-router-state created'
check "configured sees a changed resource" 0 "$(configured "$applied" configmap/cell-mesh && echo 0 || echo 1)"
check "configured ignores an unchanged one" 1 "$(configured "$applied" deployment.apps/cell-mesh && echo 0 || echo 1)"
check "configured ignores a new one" 1 "$(configured "$applied" secret/mesh-router-state && echo 0 || echo 1)"
check "configured matches the whole name" 1 "$(configured "$applied" configmap/cell && echo 0 || echo 1)"
export FAKE=value; check "secret_value decodes" "secret-pw" "$(secret_value ns s password)"
export FAKE=notfound; check "secret_value on missing secret prints nothing" "" "$(secret_value ns s password)"
export FAKE=down; check "secret_value aborts on API error" "1" "$( (secret_value ns s password >/dev/null 2>&1) && echo 0 || echo 1)"

# node.sh's port check (node.sh runs on its own, so the function comes from there)
source <(sed -n '/^ports_taken()/,/^}/p; /^taken_ports()/,/^}/p' "$here/node.sh")
listeners=$(printf 'LISTEN 0 4096 100.93.118.80:443 0.0.0.0:*\nLISTEN 0 4096 127.0.0.1:80 0.0.0.0:*\nLISTEN 0 4096 192.0.2.100:443 0.0.0.0:*\n')
check "port check leaves other addresses alone" "" "$(ports_taken 192.0.2.10 <<<"$listeners")"
check "port check finds the cell address" "192.0.2.10:443" "$(printf '%s\nLISTEN 0 1 192.0.2.10:443 0.0.0.0:*\n' "$listeners" | ports_taken 192.0.2.10)"
check "port check finds a wildcard" "*:80" "$(printf 'LISTEN 0 1 *:80 *:*\n' | ports_taken 192.0.2.10)"
check "port check finds an IPv4-mapped address" "192.0.2.10:443" "$(printf 'LISTEN 0 1 [::ffff:192.0.2.10]:443 [::]:*\n' | ports_taken 192.0.2.10)"
check "port check finds the relay port" "0.0.0.0:3478" "$(printf 'UNCONN 0 0 0.0.0.0:3478 0.0.0.0:*\n' | ports_taken 192.0.2.10 3478)"
check "port check ignores other udp ports" "" "$(printf 'UNCONN 0 0 0.0.0.0:34780 0.0.0.0:*\n' | ports_taken 192.0.2.10 3478)"
# ss fake: TCP_LISTENERS for -Hltn, UDP_LISTENERS for -Hlun.
ss() { case $1 in -Hltn) printf '%s' "${TCP_LISTENERS:-}" ;; -Hlun) printf '%s' "${UDP_LISTENERS:-}" ;; esac; }
check "free ports print nothing" "" "$(TCP_LISTENERS="" UDP_LISTENERS="" taken_ports 192.0.2.10)"
check "free ports print nothing with other listeners" "" "$(TCP_LISTENERS="$listeners" UDP_LISTENERS=$'UNCONN 0 0 127.0.0.1:3478 0.0.0.0:*\n' taken_ports 192.0.2.10)"
check "a taken relay port is named" " udp 0.0.0.0:3478" "$(UDP_LISTENERS=$'UNCONN 0 0 0.0.0.0:3478 0.0.0.0:*\n' taken_ports 192.0.2.10)"
check "taken web and relay ports are named" "*:80 udp 0.0.0.0:3478" "$(TCP_LISTENERS=$'LISTEN 0 1 *:80 *:*\n' UDP_LISTENERS=$'UNCONN 0 0 0.0.0.0:3478 0.0.0.0:*\n' taken_ports 192.0.2.10)"
ss() { return 42; }
check "a failing ss fails the check" 1 "$( (set -o pipefail; taken_ports 192.0.2.10 >/dev/null) && echo 0 || echo 1)"
unset -f ss

accounts=$(CELL_DOMAIN=acme-x.cells.test CELL_NAMES_LABEL=acme-x CELL_NAMES_TOKEN=tok names_accounts '{"forgejo.acme-x.cells.test":{"subdomain":"forgejo"}}' auth api)
check "names_accounts keeps the apps and adds platform hosts" 'api.acme-x.cells.test auth.acme-x.cells.test forgejo.acme-x.cells.test|api acme-x tok' \
  "$(python3 -c 'import json,sys; a=json.load(sys.stdin); print(" ".join(sorted(a)) + "|" + " ".join(a["api.acme-x.cells.test"][k] for k in ("subdomain","username","password")))' <<<"$accounts")"

export FAKE=down
check "a failed read of the stored accounts aborts and prints nothing" "1:" \
  "$(out=$(CELL_DOMAIN=d CELL_NAMES_LABEL=l CELL_NAMES_TOKEN=t names_accounts_manifest auth 2>/dev/null) && echo "0:$out" || echo "1:$out")"

export FAKE=oldtarget
check "a target from before the gateway is kept" "v v v" "$(stored_backup_target; echo "$CELL_BACKUP_S3_ENDPOINT $CELL_BACKUP_S3_SECRET_KEY $CELL_BACKUP_PASSWORD")"
export FAKE=value
check "a stored gateway target is kept" "secret-pw" "$(stored_backup_target; echo "$stored")"
check "a target in the environment wins" "" "$(CELL_BACKUP_PATH=/b; stored_backup_target; echo "$stored")"
export FAKE=notfound
check "no stored target" "" "$(stored_backup_target; echo "$stored${CELL_BACKUP_S3_ENDPOINT:-}")"
export FAKE=down
check "a failed read of the stored target aborts" "1" "$( (stored_backup_target >/dev/null 2>&1) && echo 0 || echo 1)"

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
: >"$FAKE_LOG"
: >"$FAKE_ENV"
manifest=$(backup_target_manifest "[target] secret_access_key = s3secret" gatewayKey=masterkey "localVolume=hostPath: {path: \"/b\"}")
check "backup_target_manifest encodes values" 'masterkey|hostPath: {path: "/b"}' "$(grep -E '^  (gatewayKey|localVolume):' <<<"$manifest" | awk '{print $2}' | while read -r v; do base64 -d <<<"$v"; echo; done | paste -sd'|' -)"
check "backup_target_manifest keeps values out of argv" "0" "$(grep -cE 'masterkey|hunter3|s3secret' "$FAKE_LOG" || true)"

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
