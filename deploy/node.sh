#!/usr/bin/env bash
# Prepares an Ubuntu LTS machine and starts k3s on it: as the cell's server
# (install.sh) or as one more machine of a cell (the join command from the
# console, which the API builds from this file, E-44). Run as root.
# Idempotent: a second run with the same settings changes nothing.
#
#   K3S_VERSION        k3s release (versions.env)
#   K3S_INSTALL_SHA256, K3S_SHA256_AMD64, K3S_SHA256_ARM64
#                      its pinned install script and binaries (versions.env)
#   CELL_STORAGE       longhorn (default) | local-path
#   CELL_NODE_IP       address of this machine in the network of the cell's
#                      machines, one of its own (default: the source address
#                      of the route to the server, on the server the default
#                      route). Machines talk only there (E-57).
#   CELL_INGRESS_IP    on the server: the cell's address, where it takes 80,
#                      443 and udp/3478 (default: CELL_NODE_IP, E-53)
#   CELL_JOIN_SERVER   the server's address; set, this machine joins as agent
#   CELL_JOIN_TOKEN    join token (k3s secure format), with CELL_JOIN_SERVER
set -euo pipefail
# The join script carries ui.sh in front of this file.
# shellcheck source=ui.sh
declare -F step >/dev/null || source "$(dirname "${BASH_SOURCE[0]}")/ui.sh"
[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"
: "${K3S_VERSION:?K3S_VERSION must be set}" "${K3S_INSTALL_SHA256:?K3S_INSTALL_SHA256 must be set}" \
  "${K3S_SHA256_AMD64:?K3S_SHA256_AMD64 must be set}" "${K3S_SHA256_ARM64:?K3S_SHA256_ARM64 must be set}"
CELL_STORAGE=${CELL_STORAGE:-longhorn}
role=server
if [ -n "${CELL_JOIN_SERVER:-}" ]; then
  : "${CELL_JOIN_TOKEN:?CELL_JOIN_TOKEN must be set to join}"
  role=agent
fi
CELL_NODE_IP=${CELL_NODE_IP:-$(ip -4 route get "${CELL_JOIN_SERVER:-1.1.1.1}" | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}')}
[ -n "$CELL_NODE_IP" ] || die "found no address of this machine: set CELL_NODE_IP=<address>"
CELL_INGRESS_IP=${CELL_INGRESS_IP:-$CELL_NODE_IP}
# node_iface prints the interface that holds an address (ip -o -4 addr on
# stdin). Flannel takes the default route's interface unless told, so
# WireGuard would leave through the public one (E-57).
node_iface() { awk -v ip="$1" '{split($4, a, "/"); if (a[1] == ip) {print $2; exit}}'; }
iface=$(ip -o -4 addr show | node_iface "$CELL_NODE_IP")
[ -n "$iface" ] || die "$CELL_NODE_IP is not an address of this machine (CELL_NODE_IP)"

# Joining: this script is all that runs, so it keeps its own log.
if [ "$role" = agent ] && [ -z "${CELL_UI_LOG:-}" ]; then
  ui '\n  %sJoining this machine to the cell%s %s%s%s\n\n' "$ui_bold" "$ui_off" "$ui_dim" "$K3S_VERSION" "$ui_off"
  ui_start /var/log/cell-install.log
  joined=1
fi

step "System packages"
export DEBIAN_FRONTEND=noninteractive
packages=(curl)
[ "$CELL_STORAGE" = longhorn ] && packages+=(open-iscsi nfs-common)
missing=()
for p in "${packages[@]}"; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
if [ "${#missing[@]}" -gt 0 ]; then
  progress "installing ${missing[*]}"
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
  busy=$(taken_ports "$CELL_INGRESS_IP") || die "could not check ports 80/443 and 3478 on $CELL_INGRESS_IP (ss failed)"
  [ -z "$busy" ] || die "ports 80/443 (tcp) or 3478 (udp) on $CELL_INGRESS_IP are taken by another program ($busy). Stop it, or give the cell an address of its own: CELL_INGRESS_IP=<address>"
fi

step "Kubernetes (k3s ${K3S_VERSION%%+*})"
# The machines talk on their node addresses only, in one network (E-57):
# pod traffic through WireGuard, images between machines through the embedded mirror, the
# platform's own under registry.cell.internal (E-42).
mkdir -p /etc/rancher/k3s
config=/etc/rancher/k3s/config.yaml
if [ "$role" = server ]; then
  # No ServiceLB: it takes 80 and 443 on every address of every machine;
  # the ingress takes them on CELL_INGRESS_IP alone (E-53).
  want=$(printf 'write-kubeconfig-mode: "600"\nnode-ip: %s\nflannel-iface: %s\nflannel-backend: wireguard-native\nembedded-registry: true\ndisable:\n  - servicelb\n' "$CELL_NODE_IP" "$iface")
else
  want=$(printf 'node-ip: %s\nflannel-iface: %s\n' "$CELL_NODE_IP" "$iface")
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
# install_k3s <role> <dir> puts the pinned k3s binary into dir and runs
# k3s' pinned install script, which then only sets up the service (E-65).
# Both are downloaded and checked first: a failed download leaves the k3s
# in dir as it was, so a rerun tries again. The script sees the new binary
# as unchanged, so it is told to restart.
install_k3s() {
  local file sum script
  case $(uname -m) in
    x86_64) file=k3s sum=$K3S_SHA256_AMD64 ;;
    aarch64) file=k3s-arm64 sum=$K3S_SHA256_ARM64 ;;
    *) die "k3s has no pinned build for $(uname -m)" ;;
  esac
  script=$(fetch "https://raw.githubusercontent.com/k3s-io/k3s/$K3S_VERSION/install.sh" "$K3S_INSTALL_SHA256") || return
  fetch "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION/+/%2B}/$file" "$sum" >"$2/k3s.new" || return
  chmod 755 "$2/k3s.new"
  mv -f "$2/k3s.new" "$2/k3s"
  INSTALL_K3S_BIN_DIR=$2 INSTALL_K3S_SKIP_DOWNLOAD=binary INSTALL_K3S_FORCE_RESTART=true sh -s - "$1" <<<"$script"
}
service=k3s
[ "$role" = agent ] && service=k3s-agent
if [ "$(k3s --version 2>/dev/null | awk 'NR==1{print $3}')" != "$K3S_VERSION" ] || ! systemctl is-enabled -q "$service" 2>/dev/null; then
  progress "downloading and starting k3s"
  [ "$role" = server ] || export K3S_URL="https://$CELL_JOIN_SERVER:6443" K3S_TOKEN="$CELL_JOIN_TOKEN"
  install_k3s "$role" /usr/local/bin
elif [ "$changed" = 1 ]; then
  systemctl restart "$service"
fi
if [ "${joined:-}" = 1 ]; then
  step_end
  ui '\n  %s%s✓ Joined%s The machine shows up in the console under Machines in a few minutes.\n\n' "$ui_bold" "$ui_ok" "$ui_off"
fi
