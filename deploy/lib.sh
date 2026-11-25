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
  "$@" # last attempt shows its errors and decides the result
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
backup_target_manifest() {
  local conf=$1
  shift
  data_manifest cell-system backup-target "rclone.conf=${conf:+$conf$'\n'}" "$@"
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
