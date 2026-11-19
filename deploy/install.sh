#!/usr/bin/env bash
# Turns a fresh Ubuntu LTS VM into a cell. Run as root with a copy of deploy/:
#
#   CELL_DOMAIN=cell.example.com CELL_TLS=acme CELL_ACME_EMAIL=ops@example.com ./install.sh
#
# Defaults: CELL_STORAGE=longhorn, CELL_TLS=selfsigned. All other settings as in
# bootstrap.sh. OPERATOR_IMAGE names the operator image; OPERATOR_IMAGE_TAR, if
# set, is an image archive (docker save) imported first. Idempotent: a second
# run changes nothing.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/versions.env"
source "$here/lib.sh"
[ "$(id -u)" -eq 0 ] || die "run as root"
: "${CELL_DOMAIN:?CELL_DOMAIN must be set}"
export CELL_STORAGE=${CELL_STORAGE:-longhorn}
start=$(date +%s)

log "host packages"
export DEBIAN_FRONTEND=noninteractive
packages=(curl git openssl perl)
[ "$CELL_STORAGE" = longhorn ] && packages+=(open-iscsi nfs-common)
missing=()
for p in "${packages[@]}"; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
if [ "${#missing[@]}" -gt 0 ]; then
  apt-get update -q
  apt-get install -yq "${missing[@]}"
fi

if [ "$CELL_STORAGE" = longhorn ]; then
  # Longhorn needs iSCSI; multipathd must not grab its block devices.
  systemctl enable --now iscsid >/dev/null 2>&1
  echo iscsi_tcp >/etc/modules-load.d/cell-longhorn.conf
  modprobe iscsi_tcp
  if systemctl is-active -q multipathd && ! grep -q 'cell: longhorn' /etc/multipath.conf 2>/dev/null; then
    printf '# cell: longhorn\nblacklist {\n  devnode "^sd[a-z0-9]+"\n}\n' >>/etc/multipath.conf
    systemctl restart multipathd
  fi
fi

log "k3s $K3S_VERSION"
if [ "$(k3s --version 2>/dev/null | awk 'NR==1{print $3}')" != "$K3S_VERSION" ]; then
  curl -sfL "https://raw.githubusercontent.com/k3s-io/k3s/$K3S_VERSION/install.sh" |
    INSTALL_K3S_VERSION="$K3S_VERSION" sh -s - server --write-kubeconfig-mode 600
fi
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
  k3s ctr -n k8s.io images import "$OPERATOR_IMAGE_TAR" >/dev/null
fi

CELL_INGRESS_IP=${CELL_INGRESS_IP:-127.0.0.1} "$here/bootstrap.sh"
log "cell installed in $(($(date +%s) - start))s"
