# Helpers for bootstrap.sh. Sourced, not executed. Tested by lib_test.sh.

log() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

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
  hash=$(cd "$root" && find "$@" -type f ! -name '*_test.go' ! -path '*/testdata/*' | sort | xargs shasum | shasum | cut -c1-12) || return 1
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
