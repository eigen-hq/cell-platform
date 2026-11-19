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
#   CELL_INGRESS_IP    address the ingress answers on (default 127.0.0.1)
#   CELL_STORAGE       local-path (default) | longhorn
#   CELL_BACKUP_S3_ENDPOINT, CELL_BACKUP_S3_BUCKET, CELL_BACKUP_S3_REGION,
#   CELL_BACKUP_S3_ACCESS_KEY, CELL_BACKUP_S3_SECRET_KEY
#                      off-cell backup target (optional, all or none)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=versions.env
source "$here/versions.env"

: "${CELL_DOMAIN:?CELL_DOMAIN must be set}"
export CELL_DOMAIN AUTHENTIK_CHART_VERSION LONGHORN_VERSION
export CELL_TLS=${CELL_TLS:-selfsigned}
export CELL_ACME_EMAIL=${CELL_ACME_EMAIL:-}
export CELL_ACME_SERVER=${CELL_ACME_SERVER:-https://acme-v02.api.letsencrypt.org/directory}
CELL_INGRESS_IP=${CELL_INGRESS_IP:-127.0.0.1}
CELL_STORAGE=${CELL_STORAGE:-local-path}

BASE_DIR="$here/base"
# shellcheck source=lib.sh
source "$here/lib.sh"

# Namespaces first, with a label owned by our own field manager. Without an
# owned field, the next server-side apply would rewrite their managed fields.
for ns in cert-manager cnpg-system cell-system; do
  printf 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  labels:\n    app.kubernetes.io/part-of: cell\n' "$ns"
  echo ---
done | kubectl apply --server-side --field-manager=cell-bootstrap -f - >/dev/null

log "cert-manager $CERT_MANAGER_VERSION, CloudNativePG $CNPG_VERSION"
kubectl apply --server-side -f "https://github.com/cert-manager/cert-manager/releases/download/$CERT_MANAGER_VERSION/cert-manager.yaml" >/dev/null
kubectl apply --server-side -f "https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v$CNPG_VERSION/cnpg-$CNPG_VERSION.yaml" >/dev/null
kubectl -n cert-manager wait deploy --all --for=condition=Available --timeout=5m >/dev/null
kubectl -n cnpg-system wait deploy --all --for=condition=Available --timeout=5m >/dev/null

log "storage: $CELL_STORAGE"
case $CELL_STORAGE in
  local-path) ;; # k3s default StorageClass, no snapshots
  longhorn)
    kubectl apply -k "https://github.com/kubernetes-csi/external-snapshotter/client/config/crd?ref=$EXTERNAL_SNAPSHOTTER_VERSION" >/dev/null
    kubectl -n kube-system apply -k "https://github.com/kubernetes-csi/external-snapshotter/deploy/kubernetes/snapshot-controller?ref=$EXTERNAL_SNAPSHOTTER_VERSION" >/dev/null
    apply storage-longhorn.yaml
    kubectl -n longhorn-system wait deploy --all --for=condition=Available --timeout=10m >/dev/null 2>&1 \
      || retry kubectl -n longhorn-system wait deploy --all --for=condition=Available --timeout=10m >/dev/null
    retry apply storage-longhorn-classes.yaml
    # Longhorn is the default StorageClass; local-path stays for k3s internals.
    kubectl annotate storageclass local-path storageclass.kubernetes.io/is-default-class=false --overwrite >/dev/null
    ;;
  *) die "unknown CELL_STORAGE=$CELL_STORAGE" ;;
esac

log "cell config"
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

s3_vars=(CELL_BACKUP_S3_ENDPOINT CELL_BACKUP_S3_BUCKET CELL_BACKUP_S3_REGION CELL_BACKUP_S3_ACCESS_KEY CELL_BACKUP_S3_SECRET_KEY)
s3_set=0
for v in "${s3_vars[@]}"; do [ -n "${!v:-}" ] && s3_set=$((s3_set + 1)); done
if [ "$s3_set" -eq "${#s3_vars[@]}" ]; then
  log "backup target: $CELL_BACKUP_S3_ENDPOINT/$CELL_BACKUP_S3_BUCKET"
  secret_manifest cell-system backup-target Opaque \
    endpoint="$CELL_BACKUP_S3_ENDPOINT" bucket="$CELL_BACKUP_S3_BUCKET" \
    region="$CELL_BACKUP_S3_REGION" accessKey="$CELL_BACKUP_S3_ACCESS_KEY" \
    secretKey="$CELL_BACKUP_S3_SECRET_KEY" | kubectl apply -f - >/dev/null
elif [ "$s3_set" -ne 0 ]; then
  die "backup target needs all of: ${s3_vars[*]}"
fi

log "TLS issuer: $CELL_TLS"
case $CELL_TLS in
  selfsigned) retry apply issuer-selfsigned.yaml ;;
  acme)
    [ -n "$CELL_ACME_EMAIL" ] || die "CELL_TLS=acme needs CELL_ACME_EMAIL"
    retry apply issuer-acme.yaml
    ;;
  *) die "unknown CELL_TLS=$CELL_TLS" ;;
esac

log "identity (authentik $AUTHENTIK_CHART_VERSION)"
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
retry apply identity.yaml
retry kubectl -n cell-system wait cluster/identity-db --for=condition=Ready --timeout=10s >/dev/null
retry kubectl -n cell-system get deploy authentik-server authentik-worker >/dev/null
kubectl -n cell-system wait deploy/authentik-server deploy/authentik-worker --for=condition=Available --timeout=10m >/dev/null

log "login page https://auth.$CELL_DOMAIN"
cacert=()
if [ "$CELL_TLS" = selfsigned ]; then
  ca=$(mktemp)
  trap 'rm -f "$ca"' EXIT
  ca_ready() { secret_value cert-manager cell-ca ca.crt >"$ca" && [ -s "$ca" ]; }
  retry ca_ready
  cacert=(--cacert "$ca")
fi
# The flow executor answers with the first login stage once the default flows exist.
retry curl -fsS -o /dev/null "${cacert[@]}" --resolve "auth.$CELL_DOMAIN:443:$CELL_INGRESS_IP" \
  "https://auth.$CELL_DOMAIN/api/v3/flows/executor/default-authentication-flow/"
log "cell ready"
