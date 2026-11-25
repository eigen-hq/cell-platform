#!/usr/bin/env bash
# Turns a fresh Ubuntu LTS machine into a cell, or updates it. Customers run
# it through get.sh (one command, the latest release); from a clone of the
# repo, run it as root directly:
#
#   CELL_DOMAIN=cell.example.com CELL_ACME_EMAIL=ops@example.com deploy/install.sh
#
# In a terminal it asks for the domain and the email when they are not set.
# Defaults: CELL_STORAGE=longhorn; CELL_TLS=acme with CELL_ACME_EMAIL, else
# selfsigned. CELL_INGRESS_IP is the cell's address for browsers and devices
# (default: the source address of the default route); CELL_NODE_IP the
# address other machines join on, in the network the machines share
# (default: CELL_INGRESS_IP; with a private network, its address there,
# E-57). All other settings as in bootstrap.sh and node.sh.
#
# It builds the platform image here from the release (E-42, no registry).
# OPERATOR_IMAGE with OPERATOR_IMAGE_TAR (an image archive, docker save)
# uses a built one instead. Idempotent: a second run changes nothing.
#
# The settings above (not the secrets) are kept in /etc/cell/install.env: a
# later run, as an update, needs none of them again. Command output goes to
# /var/log/cell-install.log; the terminal shows the steps.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
source "$here/versions.env"
source "$here/lib.sh"
[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"
settings=/etc/cell/install.env
saved=(CELL_DOMAIN CELL_TLS CELL_ACME_EMAIL CELL_ACME_SERVER CELL_STORAGE CELL_NODE_IP CELL_INGRESS_IP)
if [ -f "$settings" ]; then
  # Saved settings fill in what this run does not set.
  while IFS='=' read -r key value; do
    [ -n "${!key:-}" ] || export "$key=$value"
  done <"$settings"
fi
# get.sh writes the release into VERSION; a clone names its commit.
version=$(cat "$repo/VERSION" 2>/dev/null || git -c safe.directory="$repo" -C "$repo" describe --tags --always --dirty 2>/dev/null || echo dev)
[ -f "$settings" ] && action=Updating || action=Installing
ui '\n  %s%s the cell%s %s%s%s\n\n' "$ui_bold" "$action" "$ui_off" "$ui_dim" "$version" "$ui_off"

: "${CELL_DOMAIN:=}"
domain_re='^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'
if [ -t 0 ]; then
  for q in $(questions /etc/cell/names.env); do
    case $q in
      domain)
        until [[ $CELL_DOMAIN =~ $domain_re ]]; do
          read -rp "  Domain of the cell, e.g. cell.example.com: " CELL_DOMAIN
          CELL_DOMAIN=$(printf '%s' "$CELL_DOMAIN" | tr '[:upper:]' '[:lower:]' | tr -d ' ')
        done
        ;;
      email)
        read -rp "  Email for Let's Encrypt (empty: self-signed certificates): " CELL_ACME_EMAIL
        until [ -z "$CELL_ACME_EMAIL" ] || [[ $CELL_ACME_EMAIL == ?*@?*.?* ]]; do
          read -rp "  That is not an email address. Email (or empty): " CELL_ACME_EMAIL
        done
        ;;
    esac
  done
  ui '\n'
fi
[ -n "$CELL_DOMAIN" ] || [ -n "${CELL_NAME:-}" ] || [ -f /etc/cell/names.env ] ||
  die "no domain: run in a terminal to be asked, or set CELL_DOMAIN=<domain>"
[ -z "$CELL_DOMAIN" ] || [[ $CELL_DOMAIN =~ $domain_re ]] || die "CELL_DOMAIN=$CELL_DOMAIN is not a domain (lowercase, e.g. cell.example.com)"
[ -n "${CELL_ACME_EMAIL:-}" ] && : "${CELL_TLS:=acme}"
export CELL_DOMAIN CELL_ACME_EMAIL CELL_STORAGE=${CELL_STORAGE:-longhorn} CELL_TLS=${CELL_TLS:-selfsigned}
start=$(date +%s)

# The cell's own address (E-53) and the one other machines join on (E-44, E-57).
export CELL_INGRESS_IP=${CELL_INGRESS_IP:-$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}')}
if [ -z "${CELL_NODE_IP:-}" ] && ! private_ip "$CELL_INGRESS_IP"; then
  # A public address and one private network next to it: the machines talk
  # in the private one.
  mapfile -t private < <(ip -o -4 addr show | private_addrs /sys/class/net)
  [ "${#private[@]}" -ne 1 ] || CELL_NODE_IP=${private[0]}
fi
export CELL_NODE_IP=${CELL_NODE_IP:-$CELL_INGRESS_IP}

row() { ui '  %s%-13s%s %s\n' "$ui_dim" "$1" "$ui_off" "$2"; }
[ -z "$CELL_DOMAIN" ] || row Domain "$CELL_DOMAIN"
row Address "$CELL_INGRESS_IP"
[ "$CELL_NODE_IP" = "$CELL_INGRESS_IP" ] || row Machines "$CELL_NODE_IP (private network)"
case $CELL_TLS in
  acme) row Certificates "Let's Encrypt ($CELL_ACME_EMAIL)" ;;
  selfsigned) row Certificates "self-signed" ;;
esac
row Log /var/log/cell-install.log
ui '\n'
ui_start /var/log/cell-install.log

if [ "$CELL_TLS" = acme ] && [ -n "$CELL_DOMAIN" ]; then
  step "DNS record"
  # Let's Encrypt asks for each host of the cell: without the wildcard the
  # install would wait on certificates that never come. Another address is
  # fine behind a router that forwards the ports.
  got=$(getent ahostsv4 "dns-check.$CELL_DOMAIN" | awk 'NR == 1 {print $1}') || true
  [ -n "$got" ] || die "*.$CELL_DOMAIN has no DNS record. Add the record *.$CELL_DOMAIN A $CELL_INGRESS_IP at your DNS provider, wait a few minutes and run again."
  [ "$got" = "$CELL_INGRESS_IP" ] ||
    note "points to $got, not $CELL_INGRESS_IP: fine only if $got forwards ports 80 and 443 here"
fi

build=1
[ -z "${OPERATOR_IMAGE_TAR:-}" ] || build=0
step "Installer tools"
export DEBIAN_FRONTEND=noninteractive
missing=()
for p in curl git openssl perl python3; do dpkg -s "$p" >/dev/null 2>&1 || missing+=("$p"); done
# Docker only builds the platform image; the cell runs on k3s.
[ "$build" = 0 ] || command -v docker >/dev/null || missing+=(docker.io)
if [ "${#missing[@]}" -gt 0 ]; then
  apt-get update -q
  apt-get install -yq "${missing[@]}"
fi
if [ "$(flux version --client 2>/dev/null | awk '{print $2}')" != "v$FLUX_VERSION" ]; then
  arch=$(dpkg --print-architecture)
  curl -fsSL "https://github.com/fluxcd/flux2/releases/download/v$FLUX_VERSION/flux_${FLUX_VERSION}_linux_$arch.tar.gz" |
    tar xz -C /usr/local/bin flux
fi

if [ "$build" = 1 ]; then
  export OPERATOR_IMAGE=registry.cell.internal/cell-operator:$version
  step "Platform image $version"
  OPERATOR_IMAGE_TAR=/var/lib/cell/cell-operator.tar
  mkdir -p /var/lib/cell
  docker build -q -t "$OPERATOR_IMAGE" "$repo"
  docker save "$OPERATOR_IMAGE" -o "$OPERATOR_IMAGE_TAR"
fi
: "${OPERATOR_IMAGE:?OPERATOR_IMAGE must be set with OPERATOR_IMAGE_TAR}"

# An address from the naming service (E-55): registered once, kept in
# /etc/cell/names.env (root only); every run points it at this machine.
names=/etc/cell/names.env
if [ -z "$CELL_DOMAIN" ] && [ ! -f "$names" ]; then
  : "${CELL_NAMES_URL:?CELL_NAME needs CELL_NAMES_URL}" "${CELL_NAMES_KEY:?CELL_NAME needs CELL_NAMES_KEY (a registration key)}"
  [[ $CELL_NAME =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || die "CELL_NAME: lowercase letters, digits and inner dashes"
  step "Cell address $CELL_NAME from $CELL_NAMES_URL"
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
  step "Cell address $CELL_DOMAIN"
  curl -fsS -o /dev/null -X PUT -H @<(printf 'Authorization: Bearer %s\n' "$CELL_NAMES_TOKEN") -H 'Content-Type: application/json' \
    -d "{\"address\":\"$CELL_INGRESS_IP\"}" "$CELL_NAMES_URL/v0/cells/$CELL_NAMES_LABEL" || die "the naming service did not take this machine's address"
fi
K3S_VERSION=$K3S_VERSION "$here/node.sh"
mkdir -p /etc/cell
for key in "${saved[@]}"; do printf '%s=%s\n' "$key" "${!key:-}"; done >"$settings"
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
retry kubectl wait node --all --for=condition=Ready --timeout=10s >/dev/null

step "Platform image in the cluster"
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

# The ingress takes the cell's address, which node.sh checked.
CELL_JOIN_SERVER=$CELL_NODE_IP "$here/bootstrap.sh"
step_end

ui '\n  %s%s✓ Cell ready%s %sin %s%s\n\n' "$ui_bold" "$ui_ok" "$ui_off" "$ui_dim" "$(duration $(($(date +%s) - start)))" "$ui_off"
ui '  %-11s %s\n' Console "https://console.$CELL_DOMAIN"
# Each password is shown once, after the first run that completes with it.
[ -f /etc/cell/login-shown ] || ui '  %-11s %s\n' User akadmin
show_once /etc/cell/login-shown Password cell-system identity AUTHENTIK_BOOTSTRAP_PASSWORD \
  "Shown only now. Change it after the first login." ||
  die "could not read the first login password (Secret cell-system/identity)"
target=$(secret_value cell-system backup-target rclone.conf) || die "could not read the backup target (Secret cell-system/backup-target)"
if [ -n "$target" ]; then
  ui '  %-11s %s\n' Backups "on, to $(secret_value cell-system backup-target root)"
else
  ui '  %-11s %s\n' Backups "${ui_err}not set up yet${ui_off}: add a target in the console under Backups"
fi
show_once /etc/cell/backup-shown "Backup key" cell-system backup-target password \
  "Shown only now. Keep it outside the cell: a restore into a new cell needs it." ||
  die "could not read the backup key (Secret cell-system/backup-target)"
ui '\n'
