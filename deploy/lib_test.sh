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
source <(sed -n '/^node_iface()/p; /^ports_taken()/,/^}/p; /^taken_ports()/,/^}/p' "$here/node.sh")
listeners=$(printf 'LISTEN 0 4096 100.64.0.7:443 0.0.0.0:*\nLISTEN 0 4096 127.0.0.1:80 0.0.0.0:*\nLISTEN 0 4096 192.0.2.100:443 0.0.0.0:*\n')
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
addrs=$'2: eth0    inet 192.0.2.10/32 scope global dynamic eth0\n3: enp7s0    inet 10.0.0.2/32 brd 10.0.0.2 scope global dynamic enp7s0\n4: tailscale0    inet 100.64.0.1/32 scope global tailscale0'
check "flannel takes the private interface" enp7s0 "$(node_iface 10.0.0.2 <<<"$addrs")"
check "flannel takes the public one on a single address" eth0 "$(node_iface 192.0.2.10 <<<"$addrs")"
check "an address of another machine has no interface" "" "$(node_iface 10.0.0.20 <<<"$addrs")"
mkdir -p "$tmp/net/eth0/device" "$tmp/net/enp7s0/device" "$tmp/net/docker0"
addrs4=$'2: eth0    inet 192.0.2.10/32 scope global eth0\n3: enp7s0    inet 10.0.0.2/32 scope global enp7s0\n4: docker0    inet 172.17.0.1/16 scope global docker0\n5: tailscale0    inet 100.64.0.1/32 scope global tailscale0'
check "the private network card is found" 10.0.0.2 "$(private_addrs "$tmp/net" <<<"$addrs4")"
check "no private network, no address" "" "$(private_addrs "$tmp/net" <<<"${addrs4/10.0.0.2/203.0.113.5}")"
check "172.16/12 is private" 0 "$(private_ip 172.31.0.1; echo $?)"
check "172.32 is not private" 1 "$(private_ip 172.32.0.1; echo $?)"
check "a new cell is asked domain and email" "domain email" "$(unset CELL_DOMAIN CELL_ACME_EMAIL CELL_TLS CELL_NAME; questions "$tmp/none" | xargs)"
check "a given domain is still asked the email" email "$(CELL_DOMAIN=c.example.com; unset CELL_ACME_EMAIL CELL_TLS CELL_NAME; questions "$tmp/none" | xargs)"
check "acme without email is asked the email" email "$(CELL_DOMAIN=c.example.com CELL_TLS=acme; unset CELL_ACME_EMAIL CELL_NAME; questions "$tmp/none" | xargs)"
check "self-signed is asked nothing" "" "$(CELL_DOMAIN=c.example.com CELL_TLS=selfsigned; unset CELL_ACME_EMAIL CELL_NAME; questions "$tmp/none")"
check "a cell from the naming service is asked nothing" "" "$(touch "$tmp/names"; unset CELL_DOMAIN CELL_ACME_EMAIL CELL_TLS CELL_NAME; questions "$tmp/names")"
check "a password is shown once with its note" $'  Login       secret-pw\n              note\n|' "$(FAKE=value show_once "$tmp/shown" Login ns s k note; echo "|"; FAKE=value show_once "$tmp/shown" Login ns s k note)"
check "a missing password returns 2 and is not marked" "2 no" "$(FAKE=notfound show_once "$tmp/m2" L ns s k; echo "$? $([ -f "$tmp/m2" ] && echo yes || echo no)")"
check "an unreadable password fails and is not marked" "1 no" "$(FAKE=down show_once "$tmp/m1" L ns s k 2>/dev/null; echo "$? $([ -f "$tmp/m1" ] && echo yes || echo no)")"
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
check "backup_target_manifest encodes values" 'masterkey|hostPath: {path: "/b"}' "$(grep -E '^  (gatewayKey|localVolume):' <<<"$manifest" | awk '{gsub(/"/, "", $2); print $2}' | while read -r v; do base64 -d <<<"$v"; echo; done | paste -sd'|' -)"
check "die inside \$(...) still reports on stderr" "|error: gone" "$(: >"$CELL_STEP_FILE"; out=$( (die gone) 2>"$tmp/err"); printf '%s|' "$out"; grep -o 'error: gone' "$tmp/err")"
b64=$(backup_target_manifest $'[target]\ntype = s3' root= | awk '$1 == "rclone.conf:" {gsub(/"/, "", $2); print $2}')
check "backup_target_manifest writes rclone.conf as the operator does (no final line break)" "$(printf '[target]\ntype = s3' | base64)" "$(base64 -d <<<"$b64" | base64)"
check "no target writes an empty rclone.conf" '  rclone.conf: ""' "$(backup_target_manifest "" root= | grep rclone.conf)"
check "backup_target_manifest keeps values out of argv" "0" "$(grep -cE 'masterkey|hunter3|s3secret' "$FAKE_LOG" || true)"

# The endpoint rule, from the cases the Go side tests too.
while read -r endpoint private want; do
  case $endpoint in '' | '#'*) continue ;; esac
  check "endpoint $endpoint (private network: $private)" "$want" "$(backup_endpoint_ok "$endpoint" "$private" && echo ok || echo refused)"
done <"$here/../internal/cell/testdata/endpoints.txt"
inject=$'https://s3.example.com\nendpoint = http://203.0.113.5'
check "an endpoint with a line break is refused" "refused" "$(backup_endpoint_ok "$inject" false && echo ok || echo refused)"
s3ok() { env CELL_BACKUP_S3_ACCESS_KEY=AK CELL_BACKUP_S3_SECRET_KEY=sk "$@" bash -c 'source "$1"; s3_env_ok' _ "$here/lib.sh" >/dev/null 2>&1 && echo ok || echo refused; }
check "an https target passes" "ok" "$(s3ok CELL_BACKUP_S3_ENDPOINT=https://s3.example.com)"
check "an injected endpoint is refused" "refused" "$(s3ok CELL_BACKUP_S3_ENDPOINT="$inject")"
check "a key with a line break is refused" "refused" "$(s3ok CELL_BACKUP_S3_ENDPOINT=https://s3.example.com CELL_BACKUP_S3_SECRET_KEY=$'sk\nendpoint = http://203.0.113.5')"
check "a key with a comment mark is refused" "refused" "$(s3ok CELL_BACKUP_S3_ENDPOINT=https://s3.example.com CELL_BACKUP_S3_ACCESS_KEY='a#b')"
check "a region with a space is refused" "refused" "$(s3ok CELL_BACKUP_S3_ENDPOINT=https://s3.example.com CELL_BACKUP_S3_REGION='a b')"
check "plain http needs the flag" "refused" "$(s3ok CELL_BACKUP_S3_ENDPOINT=http://10.0.0.5:9000)"
check "plain http to a private IP with the flag passes" "ok" "$(s3ok CELL_BACKUP_S3_ENDPOINT=http://10.0.0.5:9000 CELL_BACKUP_S3_PRIVATE_NETWORK=true)"
# The installer refuses such a target before it changes anything.
printf '#!/bin/sh\nexit 1\n' >"$tmp/curl" && chmod +x "$tmp/curl"
: >"$FAKE_LOG"
out=$(unset CELL_UI_LOG CELL_STEP_FILE CELL_STEP_DIR; CELL_DOMAIN=c.example.com OPERATOR_IMAGE=x CELL_BACKUP_S3_ENDPOINT=http://203.0.113.5 CELL_BACKUP_S3_PRIVATE_NETWORK=true "$here/bootstrap.sh" 2>&1) && rc=0 || rc=$?
check "bootstrap stops on a public http target" "1 https" "$rc $(grep -o https <<<"$out" | head -1)"
check "bootstrap stops before any kubectl call" "" "$(cat "$FAKE_LOG")"
rm "$tmp/curl"

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

# The live line never wraps: at most cols - 1 characters, also when narrow.
for cols in 20 45 70 80 200; do
  for label in "Identity" "Cell address a-rather-long-cell-name from https://names.example.org"; do
    line=$(ui_cl="" ui_dim="" ui_off="" ui_tick_line "$cols" x "$label" "12m 05s" "Longhorn: 5 of 8 ready, and more" 2>&1)
    check "live line fits $cols columns (${label:0:8})" "ok" "$([ "${#line}" -le $((cols - 1)) ] && echo ok || echo "${#line}: $line")"
  done
done
check "live line keeps the detail when it fits" "  x Identity                                 12m 05s  Longhorn: 5 of 8 ready" "$(ui_cl="" ui_dim="" ui_off="" ui_tick_line 120 x Identity "12m 05s" "Longhorn: 5 of 8 ready")"

# Certificate failures name the CA's reason and the machine's own drop rules.
kubectl() { printf '%b' "$FAKE_CHALLENGES"; }
check "acme_reason reports a refused challenge" "invalid: Timeout during connect (likely firewall problem)" \
  "$(FAKE_CHALLENGES='other.x invalid nope\nauth.x invalid Timeout during connect (likely firewall problem)\n' acme_reason auth.x)"
check "acme_reason reports a failing check" "pending: failed to perform self check: connection timed out" \
  "$(FAKE_CHALLENGES='auth.x pending failed to perform self check: connection timed out\n' acme_reason auth.x)"
check "acme_reason is quiet without a reason" "" "$(FAKE_CHALLENGES='auth.x pending <none>\nauth.x valid <none>\n' acme_reason auth.x)"
unset -f kubectl
nft() { cat <<'RULES'
table ip filter {
	chain FORWARD {
		type filter hook forward priority filter; policy drop;
	}
}
table inet workstation_guard {
	chain public_forward {
		type filter hook forward priority filter - 10; policy accept;
		iifname "eth0" ct state established,related return
		iifname "eth0" counter packets 42 bytes 2512 drop
	}
	chain public_input {
		type filter hook input priority filter; policy drop;
	}
}
RULES
}
check "forward_drops names custom forward drops only" "nftables inet workstation_guard, chain public_forward" "$(forward_drops)"
out=$(: >"$CELL_STEP_FILE"; CELL_TLS=acme CELL_DOMAIN=cell.x CELL_INGRESS_IP=192.0.2.1 cert_failed "Timeout during connect" 2>&1) && rc=0 || rc=$?
check "cert_failed names reason, ports and the drop rule" "1:3" "$rc:$(grep -cE 'Timeout during connect|tcp 80, 443|workstation_guard, chain public_forward' <<<"$out")"
nft() { echo "nft: broken" >&2; return 1; }
out=$(set -e; : >"$CELL_STEP_FILE"; CELL_TLS=acme CELL_DOMAIN=cell.x CELL_INGRESS_IP=192.0.2.1 cert_failed "Timeout during connect" 2>&1) && rc=0 || rc=$?
check "cert_failed keeps its message when nft fails" "1:2" "$rc:$(grep -cE 'Timeout during connect|tcp 80, 443' <<<"$out")"
unset -f nft
out=$(set -e; : >"$CELL_STEP_FILE"; PATH=/nonexistent CELL_TLS=acme CELL_DOMAIN=cell.x CELL_INGRESS_IP=192.0.2.1 cert_failed "Timeout during connect" 2>&1) && rc=0 || rc=$?
check "cert_failed keeps its message without nft" "1:1" "$rc:$(grep -c 'Timeout during connect' <<<"$out")"
out=$(: >"$CELL_STEP_FILE"; : >"$CELL_STEP_FILE.cmd"; RETRY_ATTEMPTS=1 retry false --flag 2>&1) && rc=0 || rc=$?
check "retry names the failed command" "1:false --flag" "$rc:$(cat "$CELL_STEP_FILE.cmd")"
: >"$CELL_STEP_FILE.cmd"

# A failed Secret apply keeps the Secret's values out of the log.
kubectl() { cat >/dev/null; printf 'Error from server (Invalid): error when applying patch:\n{"data":{"password":"aHVudGVyMg=="}}\naHVudGVyMg==\n' >&2; return 1; }
out=$(data_manifest ns s password=hunter2 | apply_secret 2>&1) && rc=0 || rc=$?
check "apply_secret withholds the Secret on failure" "1:0:1" "$rc:$(grep -c 'aHVudGVyMg' <<<"$out"):$(grep -c 'Error from server (Invalid)' <<<"$out")"
unset -f kubectl
# The install log is root's alone, also when an older one was readable.
printf old >"$tmp/install.log"; chmod 644 "$tmp/install.log"
( CELL_STEP_DIR=""; CELL_STEP_FILE=$tmp/ui-step; CELL_UI_LOG=""; ui_start "$tmp/install.log" ) 3>/dev/null
check "ui_start makes the log private" "600" "$(stat -c %a "$tmp/install.log" 2>/dev/null || stat -f %Lp "$tmp/install.log")"
ln -s "$tmp/elsewhere" "$tmp/link.log"
out=$( (CELL_STEP_DIR=""; CELL_STEP_FILE=$tmp/ui-step; CELL_UI_LOG=""; ui_start "$tmp/link.log") 2>&1) && rc=0 || rc=$?
check "ui_start refuses a linked log" "1:no" "$rc:$([ -e "$tmp/elsewhere" ] && echo yes || echo no)"

# wait_ready counts available deployments; kubectl prints
# "<replicas> <available> <generation> <observed generation>".
kubectl() { printf '%b' "$FAKE_DEPLOYS"; }
out=$(FAKE_DEPLOYS='1 1 3 3\n2 2 1 1\n0 <none> 1 1\n' wait_ready x ns 0 2>&1) && rc=0 || rc=$?
check "wait_ready returns when all are available" "0" "$rc"
out=$(: >"$CELL_STEP_FILE"; FAKE_DEPLOYS='1 1 1 1\n1 <none> 1 1\n' wait_ready Longhorn ns 0 2>&1) && rc=0 || rc=$?
check "wait_ready fails at the deadline with the count" "1:1 of 2" "$rc:$(grep -o '1 of 2' <<<"$out" | head -1)"
out=$(: >"$CELL_STEP_FILE"; FAKE_DEPLOYS='1 1 4 3\n' wait_ready x ns 0 2>&1) && rc=0 || rc=$?
check "wait_ready does not trust the status of an older spec" "1" "$rc"
out=$(: >"$CELL_STEP_FILE"; FAKE_DEPLOYS='1 1 1 1\n' wait_ready x ns 0 a b 2>&1) && rc=0 || rc=$?
check "wait_ready waits for named deployments not created yet" "1" "$rc"
unset -f kubectl

# A target named again on a live cell goes through the operator (E-69).
# The fake cell's backup-target: one file per key in $tmp/bt.
kubectl() {
  local a=$* k
  case $a in
    *"get secret backup-target -o jsonpath="*)
      k=${a##*\{.data.} && k=${k%\}} && k=${k//\\/}
      [ "$k" != "${FAKE_FAIL:-}" ] || { echo "Unable to connect to the server" >&2; return 1; }
      [ ! -f "$tmp/bt/$k" ] || base64 <"$tmp/bt/$k"
      ;;
  esac
}
live_cell() { # conf, root, prefix
  rm -rf "$tmp/bt" && mkdir "$tmp/bt"
  printf gk >"$tmp/bt/gatewayKey" && printf pw >"$tmp/bt/password"
  printf '%s' "$1" >"$tmp/bt/rclone.conf" && printf '%s' "$2" >"$tmp/bt/root" && printf '%s' "$3" >"$tmp/bt/prefix"
  printf 'emptyDir: {}' >"$tmp/bt/localVolume"
}
s3a=$'[target]\ntype = s3\nendpoint = https://a' s3b=$'[target]\ntype = s3\nendpoint = https://b'
again() { # conf, root, prefix as bootstrap chose them from the environment; prints request|kept|conf|root|prefix
  (stored="" where=t conf=$1 root=$2 prefix=$3 BACKUP_LOCAL_VOLUME=${4:-"emptyDir: {}"}; : >"$CELL_STEP_FILE"
    keep_live_backup_target && echo "$backup_request|$backup_kept|$conf|$root|$prefix") 2>/dev/null || echo refused
}
live_cell "$s3a" bk/c.x c.x
check "the same S3 target again changes nothing" "|1|$s3a|bk/c.x|c.x" "$(CELL_BACKUP_S3_ENDPOINT=https://a again "$s3a" bk/c.x c.x)"
check "another S3 target keeps the stored one and asks the operator" "1|1|$s3a|bk/c.x|c.x" "$(CELL_BACKUP_S3_ENDPOINT=https://b again "$s3b" bk2/c.x c.x)"
check "another bucket alone asks the operator" "1|1|$s3a|bk/c.x|c.x" "$(CELL_BACKUP_S3_ENDPOINT=https://a again "$s3a" bk2/c.x c.x)"
live_cell "$s3a" bk/from/console from/console
check "without a prefix named, the cell's prefix counts" "|1|$s3a|bk/from/console|from/console" "$(CELL_BACKUP_S3_ENDPOINT=https://a again "$s3a" bk/c.x c.x)"
check "a prefix named counts" "1|1|$s3a|bk/from/console|from/console" "$(CELL_BACKUP_S3_ENDPOINT=https://a CELL_BACKUP_PREFIX=c.x again "$s3a" bk/c.x c.x)"
check "a directory instead of a stored target is refused" "refused" "$(CELL_BACKUP_PATH=/b again $'[target]\ntype = local' /backup/c.x c.x 'hostPath: {path: "/b", type: DirectoryOrCreate}')"
live_cell "" "" c.x
check "a cell without a target asks the operator for its first S3 one" "1|1|||c.x" "$(CELL_BACKUP_S3_ENDPOINT=https://b again "$s3b" bk/c.x c.x)"
check "a cell with a gateway takes no directory, also without a target" "refused" "$(CELL_BACKUP_PATH=/b again $'[target]\ntype = local' /backup/c.x c.x 'hostPath: {path: "/b", type: DirectoryOrCreate}')"
rm -rf "$tmp/bt"
check "a fresh cell writes the target itself" "||$s3b|bk/c.x|c.x" "$(CELL_BACKUP_S3_ENDPOINT=https://b again "$s3b" bk/c.x c.x)"
live_cell "$s3a" bk/c.x c.x
check "an unreadable gateway key stops the run" "refused" "$(FAKE_FAIL=gatewayKey CELL_BACKUP_S3_ENDPOINT=https://b again "$s3b" bk/c.x c.x)"
check "an unreadable stored target stops the run" "refused" "$(FAKE_FAIL=root CELL_BACKUP_S3_ENDPOINT=https://b again "$s3b" bk2/c.x c.x)"
live_cell "$s3a" bk/c.x c.x
check "a target the environment does not name stays, unwritten" "|1|x|bk/c.x|c.x" "$( (stored=x conf=x root=bk/c.x prefix=c.x; keep_live_backup_target; echo "$backup_request|$backup_kept|$conf|$root|$prefix") )"
kept() { (: >"$CELL_STEP_FILE"; backup_password_kept) >/dev/null 2>&1 && echo kept || echo refused; }
check "the cell's backup key again is kept" "kept" "$(CELL_BACKUP_PASSWORD=pw kept)"
check "no backup key named is kept" "kept" "$(unset CELL_BACKUP_PASSWORD; kept)"
check "another backup key on a live cell is refused" "refused" "$(CELL_BACKUP_PASSWORD=other kept)"
rm -rf "$tmp/bt"
check "a fresh cell takes the backup key named" "kept" "$(CELL_BACKUP_PASSWORD=other kept)"

# The request: as the API writes it, through stdin; then the operator's result.
kubectl() {
  case "$*" in
    "create -f - -o jsonpath={.metadata.uid}")
      cat >"$tmp/request"
      [ -z "${FAKE_EXISTS:-}" ] || { echo 'Error from server (AlreadyExists): secrets "backup-target-request" already exists' >&2; return 1; }
      echo uid-1
      ;;
    *"get secret backup-target-request"*)
      polls=$(($(cat "$tmp/polls") + 1)) && echo "$polls" >"$tmp/polls"
      [ "$polls" -gt 2 ] || echo secret/backup-target-request
      ;;
    *"get configmap backup-target-status"*) printf '%s' "$FAKE_STATUS" ;;
    *) echo "unexpected: $*" >&2; return 1 ;;
  esac
}
sleep() { :; }
ask() { (: >"$CELL_STEP_FILE"; echo 0 >"$tmp/polls"; export CELL_BACKUP_S3_ENDPOINT=https://b CELL_BACKUP_S3_BUCKET=bk CELL_BACKUP_S3_ACCESS_KEY=AK CELL_BACKUP_S3_SECRET_KEY=plain-secret-key; [ $# -eq 0 ] || export "${@?}"; request_backup_target) 2>&1; }
b64() { printf '%s' "$1" | base64; }
status() { python3 -c 'import json, sys; print(json.dumps({"data": {"request": sys.argv[1], "target.json": sys.argv[2]}}))' "$@"; }
out=$(FAKE_STATUS=$(status uid-1 '{"configured":true,"check":{"state":"passed","by":"installer"}}') ask) && rc=0 || rc=$?
check "the operator's switch ends the request" "0:3" "$rc:$(cat "$tmp/polls")"
check "the request names the installer and the S3 target" "4" "$(grep -cE "requester: \"$(b64 installer)\"|privateNetwork: \"$(b64 false)\"|endpoint: \"$(b64 https://b)\"|requestedAt: \"" "$tmp/request")"
check "the request holds no key in plain text" "0" "$(grep -c plain-secret-key "$tmp/request" || true)"
out=$(FAKE_STATUS=$(status uid-1 '{"check":{"state":"passed","by":"installer"}}') ask CELL_BACKUP_S3_PRIVATE_NETWORK=true) && rc=0 || rc=$?
check "the request carries the private network asked for" "0:1" "$rc:$(grep -c "privateNetwork: \"$(b64 true)\"" "$tmp/request")"
out=$(FAKE_STATUS=$(status uid-1 '{"check":{"state":"failed","by":"installer","message":"403 Forbidden from the bucket"}}') ask) && rc=0 || rc=$?
check "a failed check stops the run with its reason" "1:1" "$rc:$(grep -c '403 Forbidden from the bucket' <<<"$out")"
out=$(FAKE_STATUS=$(status uid-1 '{"check":{"state":"failed","message":"passed"}}') ask) && rc=0 || rc=$?
check "a failed check is no success, whatever its message" "1" "$rc"
out=$(FAKE_STATUS=$(status uid-2 '{"check":{"state":"passed","by":"installer"}}') ask) && rc=0 || rc=$?
check "a later check's result is not taken for this one" "1:1" "$rc:$(grep -c 'another check answered last' <<<"$out")"
out=$(FAKE_EXISTS=1 ask) && rc=0 || rc=$?
check "a check running already stops the run" "1:1" "$rc:$(grep -c 'being checked already' <<<"$out")"
unset -f kubectl sleep

[ "$fails" -eq 0 ]
