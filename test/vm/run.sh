#!/usr/bin/env bash
# M0 acceptance on a real VM, run locally with Lima (no cloud account needed):
#   - fresh Ubuntu LTS VM (A-12 size) to reachable login page, timed
#   - second install run changes nothing
#   - Longhorn volumes with snapshots work
#   - memory and CPU use of the empty cell
# Usage: test/vm/run.sh [keep]   (keep: leave the VM running afterwards)
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
source "$root/deploy/versions.env"
limactl=$root/bin/lima/bin/limactl
vm=cell-vm
domain=cell.test
log() { printf '\033[1m==> %s\033[0m\n' "$*"; }
vmsh() { "$limactl" shell --workdir / "$vm" "$@"; }
kc() { vmsh sudo k3s kubectl "$@"; }

[ -x "$limactl" ] || { echo "missing $limactl, run: make lima" >&2; exit 1; }
"$limactl" delete -f "$vm" >/dev/null 2>&1 || true

log "fresh VM: Ubuntu LTS, 4 vCPU, 8 GiB"
start=$(date +%s)
"$limactl" start --name="$vm" --tty=false --cpus=4 --memory=8 --disk=40 --mount-none template:ubuntu-lts >/dev/null
booted=$(date +%s)
vmsh cat /etc/os-release | grep PRETTY_NAME
"$limactl" copy -r "$root/deploy" "$vm:/tmp/"

log "install"
vmsh sudo CELL_DOMAIN="$domain" /tmp/deploy/install.sh
done_at=$(date +%s)
echo "VM boot $((booted - start))s, install to login page $((done_at - booted))s, total $((done_at - start))s"

log "second run must change nothing"
state() { kc get deploy,ds,helmcharts.helm.cattle.io,clusters.postgresql.cnpg.io,clusterissuers,certificates,crd -A \
  -o jsonpath='{range .items[*]}{.kind}/{.metadata.namespace}/{.metadata.name}={.metadata.generation}{"\n"}{end}' | sort; }
before=$(state)
vmsh sudo CELL_DOMAIN="$domain" /tmp/deploy/install.sh >/dev/null
after=$(state)
if [ "$before" != "$after" ]; then
  diff <(echo "$before") <(echo "$after") || true
  echo "FAIL: second run changed generations" >&2
  exit 1
fi
echo "ok: unchanged"

log "Longhorn volume and snapshot"
kc apply -f - <<'EOF' >/dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: snap-test, namespace: default}
spec:
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 1Gi}}
---
apiVersion: v1
kind: Pod
metadata: {name: snap-test, namespace: default}
spec:
  containers:
    - name: c
      image: busybox:1.37
      command: [sh, -c, "echo hello > /data/f && sleep 3600"]
      volumeMounts: [{name: d, mountPath: /data}]
  volumes: [{name: d, persistentVolumeClaim: {claimName: snap-test}}]
EOF
kc wait pod/snap-test --for=condition=Ready --timeout=5m >/dev/null
kc apply -f - <<'EOF' >/dev/null
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata: {name: snap-test, namespace: default}
spec: {source: {persistentVolumeClaimName: snap-test}}
EOF
kc wait volumesnapshot/snap-test --for=jsonpath='{.status.readyToUse}'=true --timeout=5m >/dev/null
echo "ok: snapshot ready"
kc delete pod/snap-test volumesnapshot/snap-test pvc/snap-test --wait >/dev/null

log "resources of the empty cell"
sleep 60 # let metrics-server collect
kc top node
kc top pods -A --sort-by=memory | head -25
vmsh free -m

[ "${1:-}" = keep ] || "$limactl" delete -f "$vm" >/dev/null
