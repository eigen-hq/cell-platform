#!/usr/bin/env bash
# Installs the cell base services into the cluster of the current kubeconfig.
# Idempotent: a second run changes nothing. Shared by the local cell
# (local-up.sh) and the VM installer (install.sh).
#
# Configuration (environment):
#   CELL_DOMAIN        apps live under <subdomain>.CELL_DOMAIN (required)
#   CELL_TLS           selfsigned (default) | acme
#   CELL_ACME_EMAIL    required for acme
#   CELL_ACME_SERVER   default: Let's Encrypt production
#   CELL_INGRESS_IP    the only address the ingress takes 80/443 on (E-53;
#                      default 127.0.0.1: the local cell, where k3d forwards)
#   CELL_STORAGE       local-path (default) | longhorn
#   CELL_JOIN_SERVER   address more machines join the cell on (optional; without
#                      it, the cell takes no more machines, as the local one)
#   OPERATOR_IMAGE     operator and API image, already present on the node (required)
#   BRIDGE_IMAGE       loopback bridge image, needed for *.localhost cells
#   Backup target (optional; the console sets one too), one of:
#   CELL_BACKUP_S3_ENDPOINT, CELL_BACKUP_S3_BUCKET, CELL_BACKUP_S3_ACCESS_KEY,
#   CELL_BACKUP_S3_SECRET_KEY (CELL_BACKUP_S3_REGION optional)
#                      an S3 bucket, over https; plain http only with
#                      CELL_BACKUP_S3_PRIVATE_NETWORK=true and an IP address
#                      of a private network as the endpoint's host (E-68)
#   CELL_BACKUP_PATH   a directory on the first machine (a disk, a mounted NAS)
#   CELL_BACKUP_RCLONE_CONFIG, CELL_BACKUP_RCLONE_ROOT
#                      any rclone remote (SFTP, WebDAV, SMB, ...): a file with
#                      a [target] section, and the path in it
#   CELL_BACKUP_PASSWORD  encrypts the backups (default: the stored one, else
#                      a new one); keep it outside the cell, a restore into a
#                      new cell needs it
#   CELL_BACKUP_PREFIX path in the target (default: CELL_DOMAIN)
#   CELL_TLS=names     certificates through the naming service (E-55), with
#   CELL_NAMES_URL, CELL_NAMES_LABEL, CELL_NAMES_TOKEN  from the cell's
#                      registration (install.sh does it, CELL_NAME)
#   CELL_DNS01_RESOLVERS  resolvers for the DNS-01 check (default 1.1.1.1:53,9.9.9.9:53)
#   CELL_NAMES_ZONE, CELL_NAMES_DNS_TOKEN
#                      run the naming service in this cell for a zone in the
#                      Hetzner Cloud DNS (only the cell that serves others)
#   CELL_LLM_API_KEY   the customer's Anthropic key for the model gateway
#                      (optional; an admin can set a provider in the console,
#                      and without one agent tasks wait). Set, it replaces the
#                      provider on every run; unset, the provider stays.
#   CELL_AGENT_MODEL   the Anthropic model (default: claude-opus-5-5)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=versions.env
source "$here/versions.env"

: "${CELL_DOMAIN:?CELL_DOMAIN must be set}"
: "${OPERATOR_IMAGE:?OPERATOR_IMAGE must be set}"
export BRIDGE_IMAGE=${BRIDGE_IMAGE:-}
case $CELL_DOMAIN in *.localhost | localhost) [ -n "$BRIDGE_IMAGE" ] || die "BRIDGE_IMAGE must be set for *.localhost cells" ;; esac
export CELL_JOIN_SERVER=${CELL_JOIN_SERVER:-}
export CELL_DOMAIN AUTHENTIK_CHART_VERSION LONGHORN_VERSION OPERATOR_IMAGE RESTIC_VERSION RCLONE_VERSION AGENTGATEWAY_VERSION POSTGRES_VERSION AUTHENTIK_DIGEST
export CELL_TLS=${CELL_TLS:-selfsigned}
export CELL_ACME_EMAIL=${CELL_ACME_EMAIL:-}
export CELL_ACME_SERVER=${CELL_ACME_SERVER:-https://acme-v02.api.letsencrypt.org/directory}
export CELL_INGRESS_IP=${CELL_INGRESS_IP:-127.0.0.1}
CELL_STORAGE=${CELL_STORAGE:-local-path}

BASE_DIR="$here/base"
# shellcheck source=lib.sh
source "$here/lib.sh"
# A target named here is checked before anything changes; a stored one,
# also one from before the gateway, stays as it is (E-68).
[ -z "${CELL_BACKUP_S3_ENDPOINT:-}" ] || s3_env_ok

# Namespaces first, with a label owned by our own field manager. Without an
# owned field, the next server-side apply would rewrite their managed fields.
for ns in cert-manager cnpg-system cell-system; do
  printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  labels:\n    app.kubernetes.io/part-of: cell\n' "$ns"
  echo ---
done | kubectl apply --server-side --field-manager=cell-bootstrap -f - >/dev/null

step "Certificates and databases"
progress "downloading components"
cm=$(fetch "https://github.com/cert-manager/cert-manager/releases/download/$CERT_MANAGER_VERSION/cert-manager.yaml" "$CERT_MANAGER_SHA256" | pin_images strict)
if [ "$CELL_TLS" = names ]; then
  # DNS-01 checks ask public resolvers: the cluster DNS answers every cell
  # host with the ingress (below), also the challenge records.
  export CELL_DNS01_RESOLVERS=${CELL_DNS01_RESOLVERS:-1.1.1.1:53,9.9.9.9:53}
  cm=$(perl -pe 's{^(\s*)- --max-concurrent-challenges=60\n}{$1- --max-concurrent-challenges=60\n$1- --dns01-recursive-nameservers-only\n$1- --dns01-recursive-nameservers=$ENV{CELL_DNS01_RESOLVERS}\n}' <<<"$cm")
  grep -q -- '^ *- --dns01-recursive-nameservers-only$' <<<"$cm" || die "could not point cert-manager at public resolvers"
fi
kubectl apply --server-side -f - <<<"$cm" >/dev/null
fetch "https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v$CNPG_VERSION/cnpg-$CNPG_VERSION.yaml" "$CNPG_SHA256" |
  pin_images strict | kubectl apply --server-side -f - >/dev/null
wait_ready cert-manager cert-manager 5
wait_ready CloudNativePG cnpg-system 5
# The databases' backups (barman-cloud plugin).
progress "database backup plugin"
# The plugin's sidecar image sits base64-encoded in a Secret and stays tag-pinned (E-76).
barman=$(fetch "https://github.com/cloudnative-pg/plugin-barman-cloud/releases/download/v$BARMAN_PLUGIN_VERSION/manifest.yaml" "$BARMAN_PLUGIN_SHA256" | pin_images strict)
# Each attempt applies the whole manifest again.
apply_barman() { kubectl apply --server-side -f - <<<"$barman" >/dev/null; }
retry apply_barman
wait_ready CloudNativePG cnpg-system 5

case $CELL_STORAGE in longhorn) step "Storage (Longhorn $LONGHORN_VERSION)" ;; *) step "Storage ($CELL_STORAGE)" ;; esac
case $CELL_STORAGE in
  local-path) ;; # k3s default StorageClass, no snapshots
  longhorn)
    progress "snapshot controller"
    kubectl apply -k "https://github.com/kubernetes-csi/external-snapshotter/client/config/crd?ref=$EXTERNAL_SNAPSHOTTER_COMMIT" >/dev/null
    # The kustomization sets the controller's image tag itself (v8.6.0 for v8.7.0).
    kubectl kustomize "https://github.com/kubernetes-csi/external-snapshotter/deploy/kubernetes/snapshot-controller?ref=$EXTERNAL_SNAPSHOTTER_COMMIT" |
      pin_images strict | kubectl -n kube-system apply -f - >/dev/null
    apply storage-longhorn.yaml
    wait_ready Longhorn longhorn-system 20
    retry apply storage-longhorn-classes.yaml
    # Longhorn is the default StorageClass; local-path stays for k3s internals.
    kubectl annotate storageclass local-path storageclass.kubernetes.io/is-default-class=false --overwrite >/dev/null
    ;;
  *) die "unknown CELL_STORAGE=$CELL_STORAGE" ;;
esac

kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: cell-config
  namespace: cell-system
data:
  domain: "$CELL_DOMAIN"
  tls: "$CELL_TLS"
  storage: "$CELL_STORAGE"
EOF

# The backup target: S3, a path on the first machine or any rclone remote,
# from the environment or, later, from the console (the operator writes it
# here). Only the backup gateway holds it (E-47); apps get keys of their
# own. Without a target the secret still holds the password and the gateway
# key, and the gateway runs: setting a target then changes nothing else.
step "Backups"
s3_vars=(CELL_BACKUP_S3_ENDPOINT CELL_BACKUP_S3_BUCKET CELL_BACKUP_S3_ACCESS_KEY CELL_BACKUP_S3_SECRET_KEY)
backup_password_kept
stored_backup_target
s3_set=0
for v in "${s3_vars[@]}"; do [ -n "${!v:-}" ] && s3_set=$((s3_set + 1)); done
prefix=${CELL_BACKUP_PREFIX:-${CELL_BACKUP_S3_PREFIX:-$CELL_DOMAIN}}
conf="" root="" where=""
export BACKUP_LOCAL_VOLUME="emptyDir: {}"
if [ -n "$stored" ]; then
  where="the stored target"
  conf=$stored
  root=$(secret_value cell-system backup-target root)
  prefix=$(secret_value cell-system backup-target prefix)
  CELL_BACKUP_PASSWORD=$(secret_value cell-system backup-target password)
  BACKUP_LOCAL_VOLUME=$(secret_value cell-system backup-target localVolume)
  BACKUP_LOCAL_VOLUME=${BACKUP_LOCAL_VOLUME:-"emptyDir: {}"}
elif [ "$s3_set" -eq "${#s3_vars[@]}" ]; then
  where="$CELL_BACKUP_S3_ENDPOINT/$CELL_BACKUP_S3_BUCKET"
  conf=$(printf '[target]\ntype = s3\nprovider = Other\nendpoint = %s\naccess_key_id = %s\nsecret_access_key = %s\nregion = %s\ndirectory_markers = true\n' \
    "$CELL_BACKUP_S3_ENDPOINT" "$CELL_BACKUP_S3_ACCESS_KEY" "$CELL_BACKUP_S3_SECRET_KEY" "${CELL_BACKUP_S3_REGION:-us-east-1}")
  root="$CELL_BACKUP_S3_BUCKET/$prefix"
elif [ "$s3_set" -ne 0 ]; then
  die "an S3 backup target needs all of: ${s3_vars[*]}"
elif [ -n "${CELL_BACKUP_PATH:-}" ]; then
  where="$CELL_BACKUP_PATH on the first machine"
  conf=$(printf '[target]\ntype = local\n')
  root="/backup/$prefix"
  BACKUP_LOCAL_VOLUME="hostPath: {path: \"$CELL_BACKUP_PATH\", type: DirectoryOrCreate}"
elif [ -n "${CELL_BACKUP_RCLONE_CONFIG:-}" ]; then
  where="rclone remote from $CELL_BACKUP_RCLONE_CONFIG"
  conf=$(cat "$CELL_BACKUP_RCLONE_CONFIG")
  grep -q '^\[target\]' <<<"$conf" || die "$CELL_BACKUP_RCLONE_CONFIG has no [target] section"
  root="${CELL_BACKUP_RCLONE_ROOT:+${CELL_BACKUP_RCLONE_ROOT%/}/}$prefix"
fi
# A cell with a gateway keeps its target; one named again goes through
# the operator's checked switch (E-69), below.
keep_live_backup_target
if [ -n "$conf" ]; then note "target: $where"; else note "no target yet: set one in the console under Backups"; fi
changed=unchanged
if [ -z "$backup_kept" ]; then
  # Without one given, the stored password, else a new one (install.sh shows
  # it once). A new password for an existing repository would lock it.
  [ -n "${CELL_BACKUP_PASSWORD:-}" ] || CELL_BACKUP_PASSWORD=$(secret_value cell-system backup-target password)
  [ -n "$CELL_BACKUP_PASSWORD" ] || CELL_BACKUP_PASSWORD=$(rand)
  # The gateway key derives every app's key: made once, kept.
  key=$(secret_value cell-system backup-target gatewayKey)
  [ -n "$key" ] || key=$(rand)
  changed=$(backup_target_manifest "$conf" root="$root" gatewayKey="$key" password="$CELL_BACKUP_PASSWORD" \
    prefix="$prefix" localVolume="$BACKUP_LOCAL_VOLUME" | apply_secret)
fi
apply backup-gateway.yaml
case $changed in *unchanged*) ;; *) kubectl -n cell-system rollout restart deploy/backup-gateway >/dev/null ;; esac
kubectl -n cell-system rollout status deploy/backup-gateway --timeout=5m >/dev/null

if [ "$CELL_INGRESS_IP" != 127.0.0.1 ]; then
  step "Ingress"
  apply ingress-address.yaml
  ingress_on() { [ "$(kubectl -n kube-system get svc traefik -o jsonpath='{.spec.externalIPs[0]}')" = "$CELL_INGRESS_IP" ]; }
  retry ingress_on
fi

case $CELL_TLS in
  acme) step "Certificates from Let's Encrypt" ;;
  names) step "Certificates through the naming service" ;;
  *) step "Certificates (self-signed)" ;;
esac
case $CELL_TLS in
  selfsigned) retry apply issuer-selfsigned.yaml ;;
  acme)
    [ -n "$CELL_ACME_EMAIL" ] || die "CELL_TLS=acme needs CELL_ACME_EMAIL"
    retry apply issuer-acme.yaml
    ;;
  names)
    : "${CELL_NAMES_URL:?CELL_TLS=names needs CELL_NAMES_URL}" "${CELL_NAMES_LABEL:?CELL_TLS=names needs CELL_NAMES_LABEL}"
    : "${CELL_NAMES_TOKEN:?CELL_TLS=names needs CELL_NAMES_TOKEN}"
    export CELL_NAMES_URL CELL_NAMES_LABEL CELL_NAMES_TOKEN
    data_manifest cert-manager cell-names url="$CELL_NAMES_URL" label="$CELL_NAMES_LABEL" token="$CELL_NAMES_TOKEN" | apply_secret >/dev/null
    # ponytail: the operator adds app hosts to the same secret; a write of
    # it racing this one can drop an app's host until its next reconcile.
    accounts=$(names_accounts_manifest auth api console mcp mesh)
    apply_secret <<<"$accounts" >/dev/null
    retry apply issuer-names.yaml
    ;;
  *) die "unknown CELL_TLS=$CELL_TLS" ;;
esac

step "Identity (authentik $AUTHENTIK_CHART_VERSION)"
dbpw=$(secret_value cell-system identity-db-credentials password)
[ -n "$dbpw" ] || dbpw=$(rand)
ensure_secret cell-system identity-db-credentials kubernetes.io/basic-auth \
  username=authentik password="$dbpw"
ensure_secret cell-system identity Opaque \
  AUTHENTIK_SECRET_KEY="$(rand)$(rand)" \
  AUTHENTIK_POSTGRESQL__HOST=identity-db-rw \
  AUTHENTIK_POSTGRESQL__NAME=authentik \
  AUTHENTIK_POSTGRESQL__USER=authentik \
  AUTHENTIK_POSTGRESQL__PASSWORD="$dbpw" \
  AUTHENTIK_BOOTSTRAP_PASSWORD="$(rand)" \
  AUTHENTIK_BOOTSTRAP_TOKEN="$(rand)" \
  AUTHENTIK_BOOTSTRAP_EMAIL="admin@$CELL_DOMAIN" \
  AUTHENTIK_ERROR_REPORTING__ENABLED=false
# The platform's own token (E-77); created when missing, so a rerun moves an
# older cell off the bootstrap token.
ensure_secret cell-system identity-platform Opaque token="$(rand)"
retry apply identity.yaml
progress "starting its database"
retry kubectl -n cell-system wait cluster/identity-db --for=condition=Ready --timeout=10s >/dev/null
wait_ready authentik cell-system 10 authentik-server authentik-worker

step "Login page"
cacert=()
if [ "$CELL_TLS" = selfsigned ]; then
  ca=$(mktemp)
  trap 'rm -f "$ca"' EXIT
  ca_ready() { secret_value cert-manager cell-ca ca.crt >"$ca" && [ -s "$ca" ]; }
  retry ca_ready
  cacert=(--cacert "$ca")
fi
if [ "$CELL_TLS" = selfsigned ]; then
  progress "waiting for the login page"
else
  progress "certificate, then login page"
  # After a failed attempt cert-manager waits an hour before the next; a new
  # run (say after a firewall fix) tries at once. Ingresses recreate their
  # certificates; the foreground deletion also removes the old challenges.
  kubectl get certificate -A --no-headers \
    -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,OWNER:.metadata.ownerReferences[0].kind,FAILED:.status.lastFailureTime |
    awk '$3 == "Ingress" && $4 != "<none>" {print $1, $2}' |
    while read -r ns name; do kubectl -n "$ns" delete certificate "$name" --cascade=foreground --timeout=2m >/dev/null; done
fi
# The flow executor answers with the first login stage once the default flows exist.
login_ready() {
  curl -fsS -o /dev/null "${cacert[@]}" --resolve "auth.$CELL_DOMAIN:443:$CELL_INGRESS_IP" \
    "https://auth.$CELL_DOMAIN/api/v3/flows/executor/default-authentication-flow/"
}
for i in $(seq 72); do
  login_ready 2>/dev/null && break
  why=""
  # A failed query is tried again with the next round; its error is in the log.
  [ "$CELL_TLS" = selfsigned ] || why=$(acme_reason "auth.$CELL_DOMAIN") || why=""
  case $why in invalid:*) cert_failed "${why#invalid: }" ;; esac
  if [ "$i" -eq 72 ]; then
    [ -z "$why" ] || cert_failed "${why#*: }"
    login_ready || die "https://auth.$CELL_DOMAIN did not answer within 6 minutes: kubectl -n cell-system get pods,certificates"
  fi
  sleep 5
done
# The worker applies the platform's account from a blueprint in the
# background; operator and API wait for it, the installer checks it once.
# The header goes in on stdin, not the command line.
ptoken=$(secret_value cell-system identity-platform token)
[ -n "$ptoken" ] || die "secret cell-system/identity-platform has no token"
platform_ready() {
  local me
  me=$(printf 'header = "Authorization: Bearer %s"\n' "$ptoken" |
    curl -fsS -K - "${cacert[@]}" --resolve "auth.$CELL_DOMAIN:443:$CELL_INGRESS_IP" \
      "https://auth.$CELL_DOMAIN/api/v3/core/users/me/") || return
  [[ $me =~ \"username\":\ *\"cell-platform\" ]]
}
progress "the platform's account"
retry platform_ready || die "authentik does not accept the platform's token: kubectl -n cell-system logs deploy/authentik-worker"
step "App delivery (Flux $FLUX_VERSION)"
# Flux may write anywhere (cluster-admin), and namespace admins may write
# HelmReleases. A release that names no ServiceAccount therefore runs as its
# namespace's helm-deployer (only app namespaces have one, E-45), and
# releases use sources of their own namespace only.
flux=$(flux install --version="v$FLUX_VERSION" --components=source-controller,helm-controller --export |
  perl -0777 -pe 's{(- --enable-leader-election\n)((?:(?!\n---\n).)*?image: ghcr\.io/fluxcd/helm-controller:)}{$1        - --default-service-account=helm-deployer\n        - --no-cross-namespace-refs=true\n$2}s')
flux=$(pin_images strict <<<"$flux")
grep -q -- '^ *- --default-service-account=helm-deployer$' <<<"$flux" || die "could not lock helm-controller to the apps' deployers"
kubectl apply --server-side -f - <<<"$flux" >/dev/null
wait_ready Flux flux-system 5

step "Cluster DNS"
# Pods reach cell hosts (e.g. the OIDC issuer) through the ingress, like
# browsers do, without hairpin NAT or public DNS.
domain_re=$(printf '%s' "$CELL_DOMAIN" | sed 's/[.]/\\./g')
dns=$(kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: coredns-custom
  namespace: kube-system
data:
  cell.override: |
    rewrite name regex ^(.+\.)?$domain_re\.?$ traefik.kube-system.svc.cluster.local answer auto
EOF
)
case $dns in *unchanged*) ;; *) kubectl -n kube-system rollout restart deploy/coredns >/dev/null ;; esac
kubectl -n kube-system rollout status deploy/coredns --timeout=2m >/dev/null

step "Operator"
kubectl apply --server-side -f "$here/crd/" >/dev/null
apply operator.yaml
kubectl -n cell-system rollout status deploy/cell-operator --timeout=5m >/dev/null
[ -z "$backup_request" ] || request_backup_target

step "Platform API"
apply change-approval.yaml
# The mesh (E-56) only on a cell with an address of its own: its devices
# need the cell's relay. The API makes its login client at start.
mesh_new=0
if [ "$CELL_INGRESS_IP" != 127.0.0.1 ] && [ -z "$(secret_value cell-system mesh-oidc client_secret)" ]; then
  ensure_secret cell-system mesh-oidc Opaque client_secret="$(rand)"
  mesh_new=1
fi
apply api.yaml
[ "$mesh_new" = 0 ] || kubectl -n cell-system rollout restart deploy/cell-api >/dev/null
kubectl -n cell-system rollout status deploy/cell-api --timeout=5m >/dev/null

# The naming service, in the cell that runs it for others (E-55). A rerun
# without the settings keeps the stored ones; keys and secret stay.
names_zone=${CELL_NAMES_ZONE:-$(secret_value cell-system names NAMES_ZONE)}
if [ -n "$names_zone" ]; then
  step "Naming service"
  names_token=${CELL_NAMES_DNS_TOKEN:-$(secret_value cell-system names NAMES_DNS_TOKEN)}
  [ -n "$names_token" ] || die "the naming service needs CELL_NAMES_DNS_TOKEN (Hetzner Cloud API token for the zone)"
  names_keys=$(secret_value cell-system names NAMES_KEYS)
  [ -n "$names_keys" ] || names_keys=$(rand)
  names_secret=$(secret_value cell-system names NAMES_SECRET)
  [ -n "$names_secret" ] || names_secret=$(rand)$(rand)
  changed=$(data_manifest cell-system names NAMES_ZONE="$names_zone" NAMES_DNS_TOKEN="$names_token" \
    NAMES_KEYS="$names_keys" NAMES_SECRET="$names_secret" | kubectl apply -f -)
  apply names.yaml
  case $changed in *unchanged*) ;; *) kubectl -n cell-system rollout restart deploy/cell-names >/dev/null ;; esac
  kubectl -n cell-system rollout status deploy/cell-names --timeout=5m >/dev/null
fi

if [ "$CELL_INGRESS_IP" != 127.0.0.1 ]; then
  step "Mesh (Headscale $HEADSCALE_VERSION)"
  if [ "$CELL_TLS" = selfsigned ]; then
    kubectl -n cell-system create configmap cell-ca --from-file=ca.crt="$ca" --dry-run=client -o yaml |
      kubectl apply -f - >/dev/null
  fi
  export HEADSCALE_VERSION TAILSCALE_VERSION
  out=$(render mesh.yaml | kubectl apply -f -)
  # Headscale reads its configuration (relay address, policy) only at start.
  if configured "$out" configmap/cell-mesh; then kubectl -n cell-system rollout restart deploy/cell-mesh >/dev/null; fi
  kubectl -n cell-system rollout status deploy/cell-mesh --timeout=5m >/dev/null
  # The router's first login: a one-time key, made once. Its state then
  # lives in mesh-router-state.
  # ponytail: a lost state needs the mesh-router secret deleted and a rerun.
  if [ -z "$(secret_value cell-system mesh-router authkey)" ]; then
    key=$(kubectl -n cell-system exec deploy/cell-mesh -- headscale preauthkeys create --tags tag:cell --expiration 1h) ||
      die "could not make the mesh router's key"
    [ -n "$key" ] || die "the mesh made an empty key for its router"
    data_manifest cell-system mesh-router authkey="$key" | kubectl apply -f - >/dev/null
  fi
  kubectl -n cell-system rollout status deploy/cell-mesh-router --timeout=5m >/dev/null
  # Running is not joined: the cell is in the mesh once its route is served.
  mesh_joined() {
    kubectl -n cell-system exec deploy/cell-mesh -c headscale -- headscale nodes list-routes -o json |
      python3 -c 'import json, sys; sys.exit(not any(sys.argv[1] in (n.get("subnet_routes") or []) for n in json.load(sys.stdin)))' "$CELL_INGRESS_IP/32"
  }
  RETRY_ATTEMPTS=24 retry mesh_joined || die "the cell's router did not join the mesh: kubectl -n cell-system logs deploy/cell-mesh-router"
fi

step "Model gateway and ops agent"
if [ -n "${CELL_LLM_API_KEY:-}" ]; then
  # Replaced as a whole: an OpenAI-compatible provider an admin set in the
  # console must not keep its address for the Anthropic key.
  llm_args=(provider=anthropic apiKey="$CELL_LLM_API_KEY")
  [ -n "${CELL_AGENT_MODEL:-}" ] && llm_args+=(model="$CELL_AGENT_MODEL")
  llm=$(secret_manifest cell-system cell-agent-llm Opaque "${llm_args[@]}")
  if kubectl -n cell-system get secret cell-agent-llm -o name >/dev/null 2>&1; then
    kubectl replace -f - <<<"$llm" >/dev/null
  else
    kubectl create -f - <<<"$llm" >/dev/null
  fi
fi
apply models.yaml
apply agent.yaml
# Both are up only once the operator created the agent's account and the
# gateway's configuration (Secrets cell-agent, cell-models). The gateway
# reloads its configuration and the agent asks for the provider: a new key
# needs no restart.
kubectl -n cell-system rollout status deploy/cell-models --timeout=5m >/dev/null
kubectl -n cell-system rollout status deploy/cell-agent --timeout=5m >/dev/null

step "MCP gateway"
apply mcp.yaml
# Up once the operator wrote its configuration (ConfigMap cell-mcp).
kubectl -n cell-system rollout status deploy/cell-mcp --timeout=5m >/dev/null
step_end
