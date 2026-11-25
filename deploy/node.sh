#!/usr/bin/env bash
# Prepares an Ubuntu LTS machine and starts k3s on it: as the cell's server
# (install.sh) or as one more machine of a cell (the join command from the
# console, which the API builds from this file, E-44). Run as root.
# Idempotent: a second run with the same settings changes nothing.
#
#   K3S_VERSION        k3s release (versions.env)
#   CELL_STORAGE       longhorn (default) | local-path
#   CELL_NODE_IP       address of this machine the others reach it on, one of
#                      its own (default: the source address of the default route)
#   CELL_JOIN_SERVER   the server's address; set, this machine joins as agent
#   CELL_JOIN_TOKEN    join token (k3s secure format), with CELL_JOIN_SERVER
set -euo pipefail
say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
[ "$(id -u)" -eq 0 ] || { echo "error: run as root" >&2; exit 1; }
: "${K3S_VERSION:?K3S_VERSION must be set}"
CELL_STORAGE=${CELL_STORAGE:-longhorn}
CELL_NODE_IP=${CELL_NODE_IP:-$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}')}
[ -n "$CELL_NODE_IP" ] || { echo "error: no address found, set CELL_NODE_IP" >&2; exit 1; }
role=server
if [ -n "${CELL_JOIN_SERVER:-}" ]; then
  : "${CELL_JOIN_TOKEN:?CELL_JOIN_TOKEN must be set to join}"
  role=agent
fi

say "host packages"
export DEBIAN_FRONTEND=noninteractive
packages=(curl)
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

# The cell's ports must be free on its address: never take them over.
# ports_taken prints the listeners (ss -Hl output on stdin) that hold one of
# the ports (a regex, default 80|443) on the address, on all addresses, or
# as an IPv4-mapped IPv6 address.
ports_taken() {
  sed -E 's/\[::ffff:([0-9.]+)\]/\1/' | awk -v ip="$1" -v ports="${2:-80|443}" '$4 ~ "^(" ip "|0\\.0\\.0\\.0|\\*|\\[::\\]):(" ports ")$" {print $4}' |
    sort -u | awk '{printf "%s%s", sep, $0; sep = " "}'
}
# taken_ports prints what holds the cell's ports on an address, nothing if
# they are free: 80/443 for the ingress, udp/3478 for the mesh relay (E-56).
taken_ports() {
  local tcp udp
  tcp=$(ss -Hltn '( sport = :80 or sport = :443 )' | ports_taken "$1") || return
  udp=$(ss -Hlun '( sport = :3478 )' | ports_taken "$1" 3478) || return
  printf '%s' "$tcp${udp:+ udp $udp}"
}
if [ "$role" = server ]; then
  busy=$(taken_ports "$CELL_NODE_IP") || {
    echo "error: could not check ports 80/443 and 3478 on $CELL_NODE_IP (ss failed)." >&2
    exit 1
  }
  if [ -n "$busy" ]; then
    echo "error: ports 80/443 (tcp) or 3478 (udp) on $CELL_NODE_IP are taken by another program ($busy)." >&2
    echo "Give the cell an address of its own: CELL_NODE_IP=<address> (E-53)." >&2
    exit 1
  fi
fi

say "k3s $K3S_VERSION as $role on $CELL_NODE_IP"
# Pod traffic between machines runs through WireGuard to each machine's
# address; images move between machines through the embedded mirror, the
# platform's own under registry.cell.internal (E-42).
mkdir -p /etc/rancher/k3s
config=/etc/rancher/k3s/config.yaml
if [ "$role" = server ]; then
  # No ServiceLB: it takes 80 and 443 on every address of every machine;
  # the ingress takes them on CELL_NODE_IP alone (E-53).
  want=$(printf 'write-kubeconfig-mode: "600"\nnode-ip: %s\nnode-external-ip: %s\nflannel-backend: wireguard-native\nflannel-external-ip: true\nembedded-registry: true\ndisable:\n  - servicelb\n' "$CELL_NODE_IP" "$CELL_NODE_IP")
else
  want=$(printf 'node-ip: %s\nnode-external-ip: %s\n' "$CELL_NODE_IP" "$CELL_NODE_IP")
fi
changed=0
if [ "$(cat "$config" 2>/dev/null)" != "$want" ]; then
  printf '%s\n' "$want" >"$config"
  changed=1
fi
mirrors=$(printf 'mirrors:\n  registry.cell.internal:\n')
if [ "$(cat /etc/rancher/k3s/registries.yaml 2>/dev/null)" != "$mirrors" ]; then
  printf '%s\n' "$mirrors" >/etc/rancher/k3s/registries.yaml
  changed=1
fi
service=k3s
[ "$role" = agent ] && service=k3s-agent
if [ "$(k3s --version 2>/dev/null | awk 'NR==1{print $3}')" != "$K3S_VERSION" ] || ! systemctl is-enabled -q "$service" 2>/dev/null; then
  if [ "$role" = server ]; then
    curl -sfL "https://raw.githubusercontent.com/k3s-io/k3s/$K3S_VERSION/install.sh" |
      INSTALL_K3S_VERSION="$K3S_VERSION" sh -s - server
  else
    curl -sfL "https://raw.githubusercontent.com/k3s-io/k3s/$K3S_VERSION/install.sh" |
      INSTALL_K3S_VERSION="$K3S_VERSION" K3S_URL="https://$CELL_JOIN_SERVER:6443" K3S_TOKEN="$CELL_JOIN_TOKEN" sh -s - agent
  fi
elif [ "$changed" = 1 ]; then
  systemctl restart "$service"
fi
say "k3s running ($role)"
