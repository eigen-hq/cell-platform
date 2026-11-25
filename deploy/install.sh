#!/usr/bin/env bash
# Turns a fresh Ubuntu LTS VM into a cell. Run as root with a copy of deploy/:
#
#   CELL_DOMAIN=cell.example.com CELL_TLS=acme CELL_ACME_EMAIL=ops@example.com ./install.sh
#
# Defaults: CELL_STORAGE=longhorn, CELL_TLS=selfsigned. CELL_NODE_IP is the
# address other machines join on, in the network the cell's machines share
# (default: the source address of the default route); CELL_INGRESS_IP is the
# cell's address for browsers and devices (default: CELL_NODE_IP). On a
# server with a private network: CELL_NODE_IP=<private> CELL_INGRESS_IP=<public>
# (E-57). All other settings as in bootstrap.sh and node.sh. OPERATOR_IMAGE
# names the operator image; OPERATOR_IMAGE_TAR, if set, is an image archive
# (docker save) imported first. Idempotent: a second run changes nothing.
#
# The settings above (not the secrets) are kept in /etc/cell/install.env: a
# later run, as update.sh does, needs none of them again.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/versions.env"
source "$here/lib.sh"
[ "$(id -u)" -eq 0 ] || die "run as root"
settings=/etc/cell/install.env
saved=(CELL_DOMAIN CELL_TLS CELL_ACME_EMAIL CELL_ACME_SERVER CELL_STORAGE CELL_NODE_IP CELL_INGRESS_IP OPERATOR_IMAGE)
if [ -f "$settings" ]; then
  # Saved settings fill in what this run does not set.
  while IFS='=' read -r key value; do
    [ -n "${!key:-}" ] || export "$key=$value"
  done <"$settings"
fi
: "${CELL_DOMAIN:=}"
[ -n "$CELL_DOMAIN" ] || [ -n "${CELL_NAME:-}" ] || [ -f /etc/cell/names.env ] || die "set CELL_DOMAIN, or CELL_NAME for an address from the naming service"
export CELL_STORAGE=${CELL_STORAGE:-longhorn} CELL_TLS=${CELL_TLS:-selfsigned}
start=$(date +%s)

log "host packages"
export DEBIAN_FRONTEND=noninteractive
missing=()
for p in curl git openssl perl python3; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
if [ "${#missing[@]}" -gt 0 ]; then
  apt-get update -q
  apt-get install -yq "${missing[@]}"
fi

# The address other machines join on (E-44) and the cell's own (E-57).
export CELL_NODE_IP=${CELL_NODE_IP:-$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}')}
export CELL_INGRESS_IP=${CELL_INGRESS_IP:-$CELL_NODE_IP}
# An address from the naming service (E-55): registered once, kept in
# /etc/cell/names.env (root only); every run points it at this machine.
names=/etc/cell/names.env
if [ -z "$CELL_DOMAIN" ] && [ ! -f "$names" ]; then
  : "${CELL_NAMES_URL:?CELL_NAME needs CELL_NAMES_URL}" "${CELL_NAMES_KEY:?CELL_NAME needs CELL_NAMES_KEY (a registration key)}"
  [[ $CELL_NAME =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || die "CELL_NAME: lowercase letters, digits and inner dashes"
  log "address for $CELL_NAME from $CELL_NAMES_URL"
  reg=$(curl -fsS -H @<(printf 'Authorization: Bearer %s\n' "$CELL_NAMES_KEY") -H 'Content-Type: application/json' \
    -d "{\"name\":\"$CELL_NAME\",\"address\":\"$CELL_INGRESS_IP\"}" "$CELL_NAMES_URL/v0/cells") || die "the naming service refused the registration"
  mkdir -p /etc/cell
  # The answer becomes a file that later runs source as root: only checked
  # values, quoted.
  (umask 077 && exec python3 -c 'import json, re, shlex, sys
r = json.load(sys.stdin)
r["url"] = sys.argv[1]
want = {"url": r"https://[A-Za-z0-9.:/-]+", "label": r"[a-z0-9-]+", "domain": r"[a-z0-9.-]+", "token": r"[0-9a-f]{64}"}
for k, pattern in want.items():
    if not isinstance(r.get(k), str) or not re.fullmatch(pattern, r[k]):
        sys.exit("the naming service answered an invalid " + k)
    print("CELL_NAMES_" + k.upper() + "=" + shlex.quote(r[k]))' "$CELL_NAMES_URL" <<<"$reg" >"$names.new") ||
    die "could not keep the registration (see the line above)"
  mv "$names.new" "$names" || die "could not keep the registration in $names"
fi
if [ -f "$names" ]; then
  # shellcheck source=/dev/null
  source "$names"
  export CELL_NAMES_URL CELL_NAMES_LABEL CELL_NAMES_TOKEN CELL_DOMAIN=$CELL_NAMES_DOMAIN CELL_TLS=names
  curl -fsS -o /dev/null -X PUT -H @<(printf 'Authorization: Bearer %s\n' "$CELL_NAMES_TOKEN") -H 'Content-Type: application/json' \
    -d "{\"address\":\"$CELL_INGRESS_IP\"}" "$CELL_NAMES_URL/v0/cells/$CELL_NAMES_LABEL" || die "the naming service did not take this machine's address"
  log "cell address $CELL_DOMAIN -> $CELL_INGRESS_IP"
fi
K3S_VERSION=$K3S_VERSION "$here/node.sh"
mkdir -p /etc/cell
for key in "${saved[@]}"; do printf '%s=%s\n' "$key" "${!key:-}"; done >"$settings"
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
retry kubectl wait node --all --for=condition=Ready --timeout=10s >/dev/null

log "Flux CLI $FLUX_VERSION"
if [ "$(flux version --client 2>/dev/null | awk '{print $2}')" != "v$FLUX_VERSION" ]; then
  arch=$(dpkg --print-architecture)
  curl -fsSL "https://github.com/fluxcd/flux2/releases/download/v$FLUX_VERSION/flux_${FLUX_VERSION}_linux_$arch.tar.gz" |
    tar xz -C /usr/local/bin flux
fi

if [ -n "${OPERATOR_IMAGE_TAR:-}" ]; then
  log "operator image from $OPERATOR_IMAGE_TAR"
  # k3s imports archives from its images folder, also while it runs, and
  # shares them with the other machines of the cell (E-42); an image loaded
  # with ctr import alone is not shared.
  images=/var/lib/rancher/k3s/agent/images
  mkdir -p "$images"
  cmp -s "$OPERATOR_IMAGE_TAR" "$images/cell-operator.tar" || cp "$OPERATOR_IMAGE_TAR" "$images/cell-operator.tar"
  # containerd lists short names in full (cell-operator:x is
  # docker.io/library/cell-operator:x).
  full=$OPERATOR_IMAGE
  first=${full%%/*}
  if [ "$first" = "$full" ]; then full=docker.io/library/$full
  elif [[ $first != *[.:]* && $first != localhost ]]; then full=docker.io/$full; fi
  has_image() { k3s ctr -n k8s.io images ls -q | grep -qxF "$full"; }
  retry has_image
fi

# The ingress takes the cell's address, which node.sh checked.
CELL_JOIN_SERVER=$CELL_NODE_IP "$here/bootstrap.sh"
log "cell installed in $(($(date +%s) - start))s"
