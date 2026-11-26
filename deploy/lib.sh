# Helpers for bootstrap.sh. Sourced, not executed. Tested by lib_test.sh.

# shellcheck source=ui.sh
source "$(dirname "${BASH_SOURCE[0]}")/ui.sh"

# render prints deploy/base/<file> with ${VAR} replaced from the environment;
# an unset variable is an error.
render() {
  perl -pe 's/\$\{([A-Z_]+)\}/defined $ENV{$1} ? $ENV{$1} : die "unset variable $1\n"/ge' "$BASE_DIR/$1"
}
apply() { render "$1" | kubectl apply -f - >/dev/null; }

# retry covers webhooks and CRDs that need a moment after install.
retry() {
  local n=1
  while [ "$n" -lt "${RETRY_ATTEMPTS:-60}" ]; do
    "$@" 2>/dev/null && return 0
    n=$((n + 1))
    sleep "${RETRY_DELAY:-5}"
  done
  "$@" && return 0 # the last attempt shows its errors
  local rc=$?
  # A failed run names this command, not retry's "$@".
  [ -s "$CELL_STEP_FILE.cmd" ] || printf '%s' "$*" >"$CELL_STEP_FILE.cmd"
  return "$rc"
}

# wait_ready <what> <namespace> <minutes> [deployment...] waits until the
# deployments (without names: all in the namespace) are available, and the
# step shows how many are.
wait_ready() {
  local what=$1 ns=$2 minutes=$3 end counts total ready
  shift 3
  end=$(($(date +%s) + minutes * 60))
  while :; do
    # Ready: the controller has seen the latest spec (as kubectl wait checks)
    # and enough replicas are available.
    counts=$(kubectl -n "$ns" get deploy "$@" --no-headers \
      -o custom-columns=W:.spec.replicas,A:.status.availableReplicas,G:.metadata.generation,O:.status.observedGeneration 2>/dev/null |
      awk '{n++} $4 != "<none>" && $4 >= $3 && ($1 == 0 || ($2 != "<none>" && $2 >= $1)) {r++} END {print n + 0, r + 0}') || counts="0 0"
    read -r total ready <<<"$counts"
    progress "$what: $ready of $total ready"
    if [ "$total" -gt 0 ] && [ "$ready" -eq "$total" ] && [ "$total" -ge "$#" ]; then return 0; fi
    [ "$(date +%s)" -lt "$end" ] || die "$what did not become ready within $minutes minutes ($ready of $total services): kubectl -n $ns get pods"
    sleep 3
  done
}

# acme_reason <host> prints why the certificate for <host> is not there
# yet, from its ACME challenge: "invalid: <reason>" once the CA refused it,
# "pending: <reason>" while a check fails. Nothing without a reason.
acme_reason() {
  kubectl get challenge -A --no-headers -o custom-columns=HOST:.spec.dnsName,STATE:.status.state,WHY:.status.reason |
    awk -v host="$1" '$1 == host && $2 != "valid" && $3 != "<none>" {state = $2; $1 = $2 = ""; sub(/^ +/, ""); print state ": " $0; exit}' |
    cut -c1-400
}

# forward_drops lists nftables chains that drop forwarded traffic, other
# than the tables iptables manages (k3s, Docker, ufw): the cell's ingress
# is forwarded, so these can keep the internet out.
forward_drops() {
  local rules
  # Only a hint: without nft, or if it fails (its error goes to the log), none.
  command -v nft >/dev/null || return 0
  rules=$(nft list ruleset) || return 0
  awk <<<"$rules" '
    /^table / {table = $2 " " $3}
    /^[ \t]*chain / {chain = $2; fwd = 0}
    /hook forward/ {fwd = 1}
    fwd && /(^|[ \t;])drop([ \t;]|$)/ && table !~ / (filter|nat|raw|mangle|security)$/ {print "nftables " table ", chain " chain; fwd = 0}'
}

# cert_failed <reason> stops with why the login page has no certificate and
# what the cell needs from the network.
cert_failed() {
  local drops here=""
  [ "$CELL_TLS" = acme ] || die "the certificate for auth.$CELL_DOMAIN failed: $1"
  drops=$(forward_drops) || drops=""
  [ -z "$drops" ] || here=$(printf ' These rules here drop\n  forwarded traffic, and the ingress of the cell is forwarded:\n%s' "$(sed 's/^/    /' <<<"$drops")")
  die "Let's Encrypt could not check auth.$CELL_DOMAIN:
    $1

  The cell needs these ports from the internet to $CELL_INGRESS_IP:
    tcp 80, 443   certificates, console, apps
    udp 3478      mesh
  Check the provider's firewall and this machine's own.$here

  Then run the installation again: it continues where it stopped."
}

rand() { openssl rand -hex 24; }

# questions prints which settings install.sh asks for in a terminal: the
# domain and the Let's Encrypt email of a cell with its own domain ($1 is
# the registration file of a cell with an address from the naming service).
questions() {
  if [ -n "${CELL_NAME:-}" ] || [ -f "$1" ]; then return 0; fi
  [ -n "${CELL_DOMAIN:-}" ] || echo domain
  [ -n "${CELL_ACME_EMAIL:-}" ] || [ "${CELL_TLS:-acme}" != acme ] || echo email
}

# show_once prints a password from a secret on the terminal (never into the
# log) as "label value", with a note below, once: the marker file stops
# later prints. Returns 2 if there is none, 1 if it could not be read;
# neither sets the marker.
# Usage: show_once <marker> <label> <namespace> <secret> <key> [note]
show_once() {
  local v
  [ ! -f "$1" ] || return 0
  v=$(secret_value "$3" "$4" "$5") || return 1
  [ -n "$v" ] || return 2
  ui '  %-11s %s%s%s\n' "$2" "$ui_bold" "$v" "$ui_off"
  [ -z "${6:-}" ] || ui '              %s%s%s\n' "$ui_dim" "$6" "$ui_off"
  touch "$1"
}

private_ip() { [[ $1 =~ ^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.) ]]; }

# private_addrs prints the private IPv4 addresses (ip -o -4 addr on stdin) of
# network cards: interfaces with a device under the sysfs net dir $1, not
# bridges or tunnels like docker0 or flannel.
private_addrs() {
  local dev cidr
  while read -r _ dev _ cidr _; do
    private_ip "${cidr%/*}" && [ -e "$1/$dev/device" ] && echo "${cidr%/*}"
  done
  return 0
}

# configured tells from the output of kubectl apply whether a resource that
# existed was changed (programs that read their configuration once need a
# restart then).
configured() { grep -qx "$2 configured" <<<"$1"; }

# secret_value prints one key of a secret. A missing secret prints nothing;
# any other error (API unreachable, forbidden) aborts.
secret_value() {
  local out
  if ! out=$(kubectl -n "$1" get secret "$2" -o "jsonpath={.data.${3//./\\.}}" 2>&1); then
    [[ $out == *NotFound* ]] && return 0
    die "read secret $1/$2: $out"
  fi
  printf '%s' "$out" | base64 -d
}

# stored_backup_target keeps the backup target of an earlier run when the
# environment names none: it sets stored to the stored rclone.conf, or, for
# a target from before the backup gateway (S3 only), exports its
# CELL_BACKUP_S3_* variables. A failed read aborts, so that a rerun never
# mistakes an unreachable API for a cell without a target.
stored_backup_target() {
  stored=""
  [ -z "${CELL_BACKUP_S3_ENDPOINT:-}${CELL_BACKUP_PATH:-}${CELL_BACKUP_RCLONE_CONFIG:-}" ] || return 0
  stored=$(secret_value cell-system backup-target rclone.conf) || return
  [ -z "$stored" ] || return 0
  local endpoint kv v
  endpoint=$(secret_value cell-system backup-target endpoint) || return
  [ -n "$endpoint" ] || return 0
  for kv in ENDPOINT:endpoint BUCKET:bucket REGION:region ACCESS_KEY:accessKey SECRET_KEY:secretKey PREFIX:prefix; do
    v=$(secret_value cell-system backup-target "${kv#*:}") || return
    export "CELL_BACKUP_S3_${kv%%:*}=$v"
  done
  CELL_BACKUP_PASSWORD=$(secret_value cell-system backup-target password) || return
  export CELL_BACKUP_PASSWORD
}

# backup_endpoint_ok tells whether $1 may be an S3 backup target's endpoint
# (E-68): https, or plain http only with $2 = true and an IP address of a
# private network as its host, never a host name. The API and the operator
# have the same rule (cell.CheckBackupEndpoint); both test
# internal/cell/testdata/endpoints.txt.
backup_endpoint_ok() {
  python3 - "$1" "$2" <<'PY'
import ipaddress, sys, urllib.parse
endpoint, private = sys.argv[1], sys.argv[2] == "true"
# urlsplit drops line breaks and tabs; the endpoint goes into rclone.conf.
if any(c <= " " or c >= "\x7f" for c in endpoint):
    sys.exit(1)
try:
    u = urllib.parse.urlsplit(endpoint)
    host = u.hostname or ""
except ValueError:
    sys.exit(1)
if u.scheme == "https":
    sys.exit(0)
if u.scheme != "http" or not private or "%" in host:
    sys.exit(1)
try:
    ip = ipaddress.ip_address(host)
except ValueError:
    sys.exit(1)
if ip.version == 6 and ip.ipv4_mapped:
    ip = ip.ipv4_mapped
nets = ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10", "::1/128", "fe80::/10", "fc00::/7"]
sys.exit(0 if any(ip in ipaddress.ip_network(n) for n in nets) else 1)
PY
}

# s3_env_ok stops the installer before it changes anything when the
# CELL_BACKUP_S3_* variables name an endpoint it refuses (E-68) or hold
# what rclone's config parser reads as more than the value, as
# cell.S3RcloneConf refuses it.
s3_env_ok() {
  local v bad="[\"'\`%\\#;]"
  for v in CELL_BACKUP_S3_ENDPOINT CELL_BACKUP_S3_ACCESS_KEY CELL_BACKUP_S3_SECRET_KEY CELL_BACKUP_S3_REGION; do
    [[ ${!v:-x} =~ ^[[:graph:]]+$ && ! ${!v:-x} =~ $bad ]] || die "$v holds spaces, control characters or any of $bad"
  done
  backup_endpoint_ok "$CELL_BACKUP_S3_ENDPOINT" "${CELL_BACKUP_S3_PRIVATE_NETWORK:-false}" ||
    die "CELL_BACKUP_S3_ENDPOINT must be https; plain http only with CELL_BACKUP_S3_PRIVATE_NETWORK=true and an IP address of a private network (10/8, 172.16/12, 192.168/16, loopback, link-local, 100.64/10, fc00::/7) as its host"
}

# backup_password_kept stops a run whose CELL_BACKUP_PASSWORD differs from
# the cell's backup key: the key encrypts every backup and never changes.
backup_password_kept() {
  local kept
  kept=$(secret_value cell-system backup-target password)
  [ -z "${CELL_BACKUP_PASSWORD:-}" ] || [ -z "$kept" ] || [ "$CELL_BACKUP_PASSWORD" = "$kept" ] ||
    die "CELL_BACKUP_PASSWORD differs from the cell's backup key; the key encrypts the backups and never changes"
}

# keep_live_backup_target runs after bootstrap chose conf, root, prefix and
# BACKUP_LOCAL_VOLUME from the environment. On a cell with a gateway only
# the operator writes the target (E-69): backup_kept=1 tells bootstrap to
# leave Secret backup-target as it is, so a switch meanwhile is never
# undone. A target named again goes through the operator's checked switch,
# as one set in the console: backup_request=1 asks request_backup_target to
# hand the S3 target over once the new operator runs. A directory or rclone
# remote is set only at the first installation. The same target again
# changes nothing. A failed read aborts, as in stored_backup_target.
keep_live_backup_target() {
  backup_request="" backup_kept=""
  local key was_conf was_root was_prefix was_volume
  key=$(secret_value cell-system backup-target gatewayKey) || return
  [ -n "$key" ] || return 0
  backup_kept=1
  [ -z "$stored" ] && [ -n "$conf" ] || return 0
  was_conf=$(secret_value cell-system backup-target rclone.conf) || return
  was_root=$(secret_value cell-system backup-target root) || return
  was_prefix=$(secret_value cell-system backup-target prefix) || return
  was_volume=$(secret_value cell-system backup-target localVolume) || return
  was_volume=${was_volume:-"emptyDir: {}"}
  # Without a prefix named, the cell's own (the console may have set one).
  if [ -z "${CELL_BACKUP_PREFIX:-}${CELL_BACKUP_S3_PREFIX:-}" ] && [ -n "$was_prefix" ]; then
    root=${root%"$prefix"}$was_prefix prefix=$was_prefix
  fi
  [ "$conf" != "$was_conf" ] || [ "$root" != "$was_root" ] || [ "$BACKUP_LOCAL_VOLUME" != "$was_volume" ] || return 0
  [ -n "${CELL_BACKUP_S3_ENDPOINT:-}" ] ||
    die "a directory or rclone remote is the backup target only at the first installation; another S3 bucket goes through the console (Backups) or CELL_BACKUP_S3_*"
  backup_request=1
  where="the stored target; the operator checks $where and switches to it after the Operator step"
  conf=$was_conf root=$was_root prefix=$was_prefix BACKUP_LOCAL_VOLUME=$was_volume
}

# request_backup_target asks the operator to check the CELL_BACKUP_S3_*
# target and switch to it under the gateway lock, the path the API takes
# (E-60, E-69), and waits for the result. Values go through stdin, never
# the command line.
request_backup_target() {
  local at out kind uid private=false deadline=$((SECONDS + 1200))
  [ "${CELL_BACKUP_S3_PRIVATE_NETWORK:-}" != true ] || private=true
  at=$(python3 -c 'import datetime; print(datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z"))')
  if ! out=$(data_manifest cell-system backup-target-request endpoint="$CELL_BACKUP_S3_ENDPOINT" region="${CELL_BACKUP_S3_REGION:-}" \
    bucket="$CELL_BACKUP_S3_BUCKET" prefix="${CELL_BACKUP_PREFIX:-${CELL_BACKUP_S3_PREFIX:-}}" \
    accessKey="$CELL_BACKUP_S3_ACCESS_KEY" secretKey="$CELL_BACKUP_S3_SECRET_KEY" privateNetwork="$private" \
    requester=installer requestedAt="$at" | kubectl create -f - -o 'jsonpath={.metadata.uid}' 2>&1); then
    kind=$(head -n 1 <<<"$out" | grep -oE '^(error|Error from server( \([A-Za-z]+\))?)' || echo "no error kind")
    [ "$kind" != "Error from server (AlreadyExists)" ] ||
      die "a backup target is being checked already; wait for its result in the console under Backups, then run again"
    die "asking the operator for the backup target failed ($kind); the rest of the message is withheld, it can hold the keys"
  fi
  uid=$out
  [ -n "$uid" ] || die "the backup target request has no UID"
  progress "the operator checks the target and switches to it"
  # ponytail: a fixed 20 min; the switch waits for running backups first.
  while :; do
    out=$(kubectl -n cell-system get secret backup-target-request --ignore-not-found -o name) ||
      die "could not read the backup target request"
    [ -n "$out" ] || break
    [ "$SECONDS" -lt "$deadline" ] || die "the backup target is still being checked after 20 min; the console shows the check under Backups"
    sleep 5
  done
  # The result of this request, told by its UID: a later one may have ended too.
  out=$(kubectl -n cell-system get configmap backup-target-status -o json | UID_ASKED=$uid python3 -c 'import json, os, sys
d = json.load(sys.stdin).get("data") or {}
c = json.loads(d.get("target.json") or "{}").get("check") or {}
print("another check answered last" if d.get("request") != os.environ["UID_ASKED"] else
      "passed" if c.get("state") == "passed" else "not passed: " + (c.get("message") or "no result"))') ||
    die "could not read the result of the backup target check"
  [ "$out" = passed ] || die "the operator did not switch to the backup target: $out"
  note "target: $CELL_BACKUP_S3_ENDPOINT/$CELL_BACKUP_S3_BUCKET, checked and switched to by the operator"
}

# apply_secret applies the Secret manifest on stdin and prints what kubectl
# says. A failed apply can quote the manifest, values and all: of its
# message only kubectl's kind of error reaches the log.
apply_secret() {
  local out rc=0
  out=$(kubectl apply -f - 2>&1) || rc=$?
  if [ "$rc" -eq 0 ]; then
    printf '%s\n' "$out"
    return 0
  fi
  printf 'applying a Secret failed (exit %s, %s); the rest of the message is withheld, it can hold the values\n' "$rc" \
    "$(head -n 1 <<<"$out" | grep -oE '^(error|Error from server( \([A-Za-z]+\))?)' || echo "no error kind")" >&2
  return "$rc"
}

# data_manifest prints a Secret in namespace $1 named $2 with KEY=VALUE
# pairs from the other arguments; values may span lines and keys may hold
# dots. Values travel through pipes, never through the argv of another
# process.
data_manifest() {
  local kv
  printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\ndata:\n' "$2" "$1"
  shift 2
  for kv in "$@"; do
    # Quoted: an empty value is "", not null.
    printf '  %s: "%s"\n' "${kv%%=*}" "$(printf '%s' "${kv#*=}" | base64 | tr -d '\n')"
  done
}

# backup_target_manifest prints the backup-target Secret: rclone.conf from
# the first argument (empty: no target yet), KEY=VALUE pairs from the others.
# Without a final line break, as the operator writes it (cell.S3RcloneConf):
# a target set in the console stays unchanged when the installer runs again.
backup_target_manifest() {
  local conf=$1
  shift
  data_manifest cell-system backup-target "rclone.conf=$conf" "$@"
}

# names_accounts prints the acme-dns accounts of a cell with an address from
# the naming service (E-55): the accounts in $1 (JSON, may be empty) plus one
# for each platform host named in the other arguments. cert-manager looks an
# account up by host; the operator adds the apps' hosts the same way.
# Credentials come from the environment, not from argv.
names_accounts() {
  EXISTING=$1 python3 - "${@:2}" <<'PY'
import json, os, sys
accounts = json.loads(os.environ.get("EXISTING") or "{}")
for sub in sys.argv[1:]:
    host = sub + "." + os.environ["CELL_DOMAIN"]
    accounts.setdefault(host, {"username": os.environ["CELL_NAMES_LABEL"], "password": os.environ["CELL_NAMES_TOKEN"],
                               "fulldomain": host, "subdomain": sub, "allowfrom": []})
print(json.dumps(accounts, sort_keys=True, separators=(",", ":")))
PY
}

# names_accounts_manifest prints the cell-acme-dns Secret with the stored
# accounts plus the platform hosts named in the arguments. A failed read
# aborts: writing without the stored accounts would drop the apps' hosts.
names_accounts_manifest() {
  local existing accounts
  existing=$(secret_value cert-manager cell-acme-dns accounts.json) || return
  accounts=$(names_accounts "$existing" "$@") || return
  data_manifest cert-manager cell-acme-dns accounts.json="$accounts"
}

# secret_manifest prints a Secret with KEY=VALUE pairs from the arguments.
# Values travel through a pipe, never through the argv of another process.
secret_manifest() {
  local ns=$1 name=$2 type=$3
  shift 3
  kubectl -n "$ns" create secret generic "$name" --type="$type" \
    --from-env-file=<(printf '%s\n' "$@") --dry-run=client -o yaml
}

# ensure_secret creates a secret once and never touches it again.
# Creating fails loudly if it appeared in the meantime.
ensure_secret() {
  local ns=$1 name=$2 out
  if out=$(kubectl -n "$ns" get secret "$name" -o name 2>&1); then
    return 0
  fi
  [[ $out == *NotFound* ]] || die "read secret $ns/$name: $out"
  secret_manifest "$@" | kubectl create -f - >/dev/null
}

# build_image builds an image unless it exists, imports it into the k3d cell
# unless it is there, and prints its reference. The tag is a hash of the
# sources, so unchanged code is a no-op. Every failure is returned: callers
# use $(...), where errexit does not apply.
# Usage: build_image <name> <docker target> <repo root> <cell> <sources...>
build_image() {
  local name=$1 target=$2 root=$3 cell=$4 hash ref
  shift 4
  hash=$(cd "$root" && find "$@" -type f ! -name '*_test.go' ! -path '*/testdata/*' \
    ! -path '*/node_modules/*' ! -path 'console/dist/*' ! -path 'console/test-results/*' ! -path 'console/playwright-report/*' \
    ! -path 'console/src/lib/api/schema.d.ts' ! -path 'console/src/lib/tokens.css' | sort | xargs shasum | shasum | cut -c1-12) || return 1
  [ -n "$hash" ] || return 1
  ref=$name:$hash
  if ! docker image inspect "$ref" >/dev/null 2>&1; then
    docker build -q --target "$target" -t "$ref" "$root" >/dev/null || return 1
  fi
  if ! docker exec "k3d-$cell-server-0" ctr -n k8s.io images ls -q | grep -q "$ref\$"; then
    k3d image import "$ref" -c "$cell" >/dev/null || return 1
  fi
  echo "$ref"
}
