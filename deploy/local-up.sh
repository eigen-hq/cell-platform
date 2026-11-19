#!/usr/bin/env bash
# Local cell: k3s in Docker via k3d, then the shared bootstrap.
# Ports 80/443 on this machine's loopback go to the cell ingress; apps are reachable as
# https://<subdomain>.cell.localhost (browsers and curl resolve *.localhost).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source "$here/versions.env"
: "${CELL_NAME:=cell}" "${KUBECONFIG:?}"
start=$(date +%s)

if ! k3d cluster get "$CELL_NAME" >/dev/null 2>&1; then
  k3d cluster create "$CELL_NAME" \
    --image "rancher/k3s:${K3S_VERSION/+/-}" \
    -p "127.0.0.1:80:80@loadbalancer" -p "127.0.0.1:443:443@loadbalancer" \
    --api-port 127.0.0.1:6550 \
    --kubeconfig-update-default=false --kubeconfig-switch-context=false \
    --wait
fi
mkdir -p "$(dirname "$KUBECONFIG")"
k3d kubeconfig get "$CELL_NAME" >"$KUBECONFIG"
chmod 600 "$KUBECONFIG"

# Images: tags are source hashes, so an operator change does not touch the
# bridge (and with it every app pod).
root=$(cd "$here/.." && pwd)
source "$here/lib.sh"
OPERATOR_IMAGE=$(build_image cell-operator operator "$root" "$CELL_NAME" api cmd/operator internal Dockerfile go.mod go.sum)
BRIDGE_IMAGE=$(build_image cell-bridge bridge "$root" "$CELL_NAME" cmd/bridge Dockerfile go.mod go.sum)
export OPERATOR_IMAGE BRIDGE_IMAGE

CELL_DOMAIN=${CELL_DOMAIN:-cell.localhost} "$here/bootstrap.sh"
echo "local cell up in $(($(date +%s) - start))s"
