#!/usr/bin/env bash
# Turns a fresh Ubuntu LTS VM into a cell. Run as root with a copy of deploy/:
#
#   CELL_DOMAIN=cell.example.com CELL_TLS=acme CELL_ACME_EMAIL=ops@example.com ./install.sh
#
# Defaults: CELL_STORAGE=longhorn, CELL_TLS=selfsigned. CELL_NODE_IP is the
# address other machines join on (default: the source address of the default
# route). All other settings as in bootstrap.sh and node.sh. OPERATOR_IMAGE
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
saved=(CELL_DOMAIN CELL_TLS CELL_ACME_EMAIL CELL_ACME_SERVER CELL_STORAGE CELL_NODE_IP OPERATOR_IMAGE)
if [ -f "$settings" ]; then
  # Saved settings fill in what this run does not set.
  while IFS='=' read -r key value; do
    [ -n "${!key:-}" ] || export "$key=$value"
  done <"$settings"
fi
: "${CELL_DOMAIN:?CELL_DOMAIN must be set}"
export CELL_STORAGE=${CELL_STORAGE:-longhorn} CELL_TLS=${CELL_TLS:-selfsigned}
start=$(date +%s)

log "host packages"
export DEBIAN_FRONTEND=noninteractive
missing=()
for p in curl git openssl perl; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
if [ "${#missing[@]}" -gt 0 ]; then
  apt-get update -q
  apt-get install -yq "${missing[@]}"
fi

# The address other machines join on (E-44).
export CELL_NODE_IP=${CELL_NODE_IP:-$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}')}
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

# The ingress takes the address the others join on, which node.sh checked.
CELL_JOIN_SERVER=$CELL_NODE_IP CELL_INGRESS_IP=$CELL_NODE_IP "$here/bootstrap.sh"
log "cell installed in $(($(date +%s) - start))s"
