#!/usr/bin/env bash
# Cell check on real VMs, run locally with Lima (no cloud account needed):
#   - fresh Ubuntu 24.04 VM to reachable login page, timed
#   - second install run changes nothing
#   - a second VM joins through the platform API, with the command the
#     console shows (E-44); both machines are ready, linked by WireGuard
#   - the joined machine gets the platform image from the first one (no
#     registry), and its pods reach services on the first one
#   - Longhorn volumes with snapshots work on the joined machine
#   - memory and CPU use of the empty cell
# Usage: test/vm/run.sh [keep]   (keep: leave the VMs running afterwards)
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
source "$root/deploy/versions.env"
limactl=$root/bin/lima/bin/limactl
vm1=cell-vm
vm2=cell-vm2
domain=cell.test
log() { printf '\033[1m==> %s\033[0m\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
on() { local vm=$1; shift; "$limactl" shell --workdir / "$vm" "$@"; }
kc() { on "$vm1" sudo k3s kubectl "$@"; }
# The address of a VM on the network the VMs share (Lima user-v2).
addr() { on "$1" ip -4 -o addr show eth0 | awk '{split($4, a, "/"); print a[1]}'; }

[ -x "$limactl" ] || { echo "missing $limactl, run: make lima" >&2; exit 1; }
"$limactl" delete -f "$vm1" "$vm2" >/dev/null 2>&1 || true

# The VMs have no registry access to our image: ship it as an archive.
image=registry.cell.internal/cell-operator:vm-test
docker build -q -t "$image" "$root" >/dev/null
mkdir -p "$root/.cell"
docker save "$image" -o "$root/.cell/operator.tar"

log "two fresh VMs: Ubuntu 24.04"
start=$(date +%s)
vmstart() { "$limactl" start --name="$1" --tty=false --cpus="$2" --memory="$3" --disk=40 --mount-none --network=lima:user-v2 template:ubuntu-24.04 >/dev/null; }
vmstart "$vm1" 4 5 &
p1=$!
vmstart "$vm2" 2 3 &
p2=$!
wait "$p1"
wait "$p2"
booted=$(date +%s)
on "$vm1" cat /etc/os-release | grep PRETTY_NAME
ip1=$(addr "$vm1")
ip2=$(addr "$vm2")
echo "$vm1 $ip1, $vm2 $ip2"
"$limactl" copy -r "$root/deploy" "$vm1:/tmp/"
"$limactl" copy "$root/.cell/operator.tar" "$vm1:/tmp/operator.tar"
cell_install() { on "$vm1" sudo CELL_DOMAIN="$domain" OPERATOR_IMAGE="$image" OPERATOR_IMAGE_TAR=/tmp/operator.tar /tmp/deploy/install.sh; }

log "install on $vm1"
cell_install
done_at=$(date +%s)
echo "VM boot $((booted - start))s, install to login page $((done_at - booted))s"

log "second run must change nothing"
state() { kc get deploy,ds,helmcharts.helm.cattle.io,clusters.postgresql.cnpg.io,clusterissuers,certificates,crd -A \
  -o jsonpath='{range .items[*]}{.kind}/{.metadata.namespace}/{.metadata.name}={.metadata.generation}{"\n"}{end}' | sort; }
before=$(state)
cell_install >/dev/null
after=$(state)
if [ "$before" != "$after" ]; then
  diff <(echo "$before") <(echo "$after") || true
  fail "second run changed generations"
fi
echo "ok: unchanged"

log "admin token for the platform API"
# As a script on the VM: the API answers on the VM's ingress, and the
# admin's app password comes from the identity like in test/e2e.
api_session=$(cat <<'EOF'
set -euo pipefail
d=$1 ip=$2
kc() { k3s kubectl "$@"; }
kc -n cert-manager get secret cell-ca -o jsonpath='{.data.ca\.crt}' | base64 -d >/tmp/cell-ca.crt
bt=$(kc -n cell-system get secret identity -o jsonpath='{.data.AUTHENTIK_BOOTSTRAP_TOKEN}' | base64 -d)
get() { python3 -c "import json,sys; print(json.load(sys.stdin)$1)"; }
c() { curl -fsS --cacert /tmp/cell-ca.crt --resolve "auth.$d:443:$ip" --resolve "api.$d:443:$ip" "$@"; }
ak() { c -H "Authorization: Bearer $bt" -H 'Content-Type: application/json' "https://auth.$d/api/v3/$1" "${@:2}"; }
pk=$(ak 'core/users/?username=akadmin' | get '["results"][0]["pk"]')
ak core/tokens/vm-test/ -X DELETE >/dev/null 2>&1 || true
ak core/tokens/ -d "{\"identifier\":\"vm-test\",\"intent\":\"app_password\",\"user\":$pk,\"expiring\":true,\"expires\":\"$(date -u -d '+3 hours' +%FT%TZ)\"}" >/dev/null
pw=$(ak core/tokens/vm-test/view_key/ | get '["key"]')
c -d grant_type=client_credentials -d client_id=cell-api -d username=akadmin --data-urlencode "password=$pw" -d 'scope=openid profile' \
  "https://auth.$d/application/o/token/" | get '["access_token"]' >/tmp/api-token
EOF
)
on "$vm1" sudo bash -c "$api_session" _ "$domain" "$ip1"
api() { on "$vm1" sudo bash -c 'curl -fsS --cacert /tmp/cell-ca.crt --resolve "api.$1:443:$2" -H "Authorization: Bearer $(cat /tmp/api-token)" "${@:4}" "https://api.$1$3"' _ "$domain" "$ip1" "$@"; }

log "$vm2 joins through the API"
cmd=$(api /v0/nodes/joins -X POST | python3 -c 'import json,sys; print(json.load(sys.stdin)["command"])')
echo "$cmd"
[[ $cmd == "curl -fsSL https://api.$domain/v0/nodes/join.sh | sudo CELL_JOIN_KEY="*" bash" ]] || fail "unexpected join command"
# The test cell has no public DNS and a self-signed CA: the second VM gets
# both by hand, then runs the command as the console shows it.
on "$vm1" sudo cat /tmp/cell-ca.crt | on "$vm2" sudo tee /usr/local/share/ca-certificates/cell-ca.crt >/dev/null
on "$vm2" sudo update-ca-certificates >/dev/null
on "$vm2" sudo sh -c "echo '$ip1 api.$domain' >>/etc/hosts"
joined=$(date +%s)
on "$vm2" bash -c "$cmd"
check_nodes() { python3 -c '
import json, sys
n = json.load(sys.stdin)
assert len(n) == 2 and all(m["ready"] for m in n), n
assert [m["role"] for m in n] == ["server", "agent"], n
assert [m["address"] for m in n] == sys.argv[1:], n  # found by node.sh itself
print("ok: " + ", ".join("{} {} {}".format(m["name"], m["role"], m["address"]) for m in n))' "$ip1" "$ip2"; }
n=0
until nodes=$(api /v0/nodes) && echo "$nodes" | check_nodes 2>/dev/null; do
  n=$((n + 1)); [ "$n" -lt 60 ] || fail "machines: $nodes"; sleep 5
done
echo "$nodes" | check_nodes
echo "joined in $(($(date +%s) - joined))s"
node2=$(echo "$nodes" | python3 -c 'import json,sys; print(json.load(sys.stdin)[1]["name"])')

log "WireGuard between the machines"
for vm in "$vm1" "$vm2"; do
  on "$vm" ip link show flannel-wg >/dev/null || fail "$vm has no WireGuard link"
done
echo "ok: flannel-wg on both"

log "the cell keeps to its own address (E-53)"
for vm in "$vm1" "$vm2"; do
  ! on "$vm" curl -sk -o /dev/null --max-time 5 https://127.0.0.1/ || fail "$vm: the cell answers on 127.0.0.1:443"
done
echo "ok: 443 on other addresses stays free"

log "image and pod traffic on $node2"
kc apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: join-image, namespace: default}
spec:
  nodeName: $node2
  restartPolicy: Never
  containers: [{name: c, image: $image, imagePullPolicy: IfNotPresent, command: [/api]}]
---
apiVersion: v1
kind: Pod
metadata: {name: join-net, namespace: default}
spec:
  nodeName: $node2
  restartPolicy: Never
  containers: [{name: c, image: busybox:1.37, command: [wget, -qO-, http://cell-api.cell-system/healthz]}]
EOF
pulled() { [ -n "$(kc get pod join-image -o jsonpath='{.status.containerStatuses[0].imageID}')" ]; }
n=0
until pulled; do
  n=$((n + 1)); [ "$n" -lt 60 ] || fail "$node2 did not get $image"; sleep 5
done
echo "ok: $image on $node2 without a registry"
kc wait pod/join-net --for=jsonpath='{.status.phase}'=Succeeded --timeout=3m >/dev/null || { kc logs join-net || true; fail "no traffic from $node2 to the API"; }
api_node=$(kc -n cell-system get pod -l app.kubernetes.io/name=cell-api -o jsonpath='{.items[0].spec.nodeName}')
[ "$api_node" != "$node2" ] || fail "cell-api runs on $node2 itself, the check proves nothing"
echo "ok: pod on $node2 reached cell-api on $api_node: $(kc logs join-net)"
kc delete pod join-image join-net --wait >/dev/null

log "Longhorn volume and snapshot on $node2"
kc apply -f - <<EOF >/dev/null
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
  nodeName: $node2
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

[ "${1:-}" = keep ] || "$limactl" delete -f "$vm1" "$vm2" >/dev/null
