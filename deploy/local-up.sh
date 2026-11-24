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
OPERATOR_IMAGE=$(build_image cell-operator operator "$root" "$CELL_NAME" api catalog cmd/operator cmd/api cmd/agent internal console Dockerfile go.mod go.sum)
BRIDGE_IMAGE=$(build_image cell-bridge bridge "$root" "$CELL_NAME" cmd/bridge Dockerfile go.mod go.sum)
export OPERATOR_IMAGE BRIDGE_IMAGE

# Off-cell backup target: an S3 server next to the cell (rclone serve s3).
# Its data and credentials live in .cell/ and survive cell-down, like a
# bucket at a provider would.
state="$root/.cell"
mkdir -p "$state/backup-s3/cell-backups"
if [ ! -f "$state/backup.env" ]; then
  (umask 077 && printf 'CELL_BACKUP_S3_ACCESS_KEY=%s\nCELL_BACKUP_S3_SECRET_KEY=%s\nCELL_BACKUP_PASSWORD=%s\n' \
    "$(rand)" "$(rand)" "$(rand)" >"$state/backup.env")
fi
set -a
source "$state/backup.env"
set +a
if ! docker inspect cell-backup-s3 >/dev/null 2>&1; then
  docker run -d --name cell-backup-s3 --restart unless-stopped -v "$state/backup-s3:/data" \
    -e RCLONE_AUTH_KEY="\"$CELL_BACKUP_S3_ACCESS_KEY,$CELL_BACKUP_S3_SECRET_KEY\"" \
    "rclone/rclone:$RCLONE_VERSION" serve s3 /data --addr :8080 >/dev/null
fi
docker start cell-backup-s3 >/dev/null
net="k3d-$CELL_NAME"
docker inspect -f '{{json .NetworkSettings.Networks}}' cell-backup-s3 | grep -q "\"$net\"" ||
  docker network connect "$net" cell-backup-s3
# Pods cannot resolve container names (k3d injects hosts only at creation),
# so the endpoint is the container's address on the cell network.
s3_ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}" cell-backup-s3)
[ -n "$s3_ip" ] || die "backup S3 has no address on $net"
export CELL_BACKUP_S3_ENDPOINT=http://$s3_ip:8080 CELL_BACKUP_S3_BUCKET=cell-backups CELL_BACKUP_S3_REGION=us-east-1

CELL_DOMAIN=${CELL_DOMAIN:-cell.localhost} "$here/bootstrap.sh"
echo "local cell up in $(($(date +%s) - start))s"
