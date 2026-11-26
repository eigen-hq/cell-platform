# App manifest

Status: v1alpha1 (sharpened against the three reference apps). The manifest is the central abstraction of the platform: an app describes what it needs, and the platform provides it. The Go types are in `api/v1alpha1/app_types.go`, the CRD in `deploy/crd/`.

The model is the Cloudron manifest, where database, login and mail are requested as shared services of the platform.

## Basic idea

- The app brings only itself (Helm chart or container image).
- It requests everything shared: database, volumes, generated secrets, login, subdomain.
- The platform fills in the concrete values at install time (placeholders).
- The customer states only what only the customer can know, for example the desired subdomain.

A catalog manifest (`catalog/<app>/manifest.yaml`) is itself an `App`. Installing means: create this object, possibly with a changed subdomain.

## Example

Shortened from `catalog/forgejo/manifest.yaml`:

```yaml
apiVersion: cell.internal/v1alpha1
kind: App
metadata:
  name: forgejo                     # yields namespace app-forgejo
spec:
  title: Forgejo
  version: "15.0.11"                # available as {{ .app.version }}
  source:
    helm:                           # alternatively: image
      repo: oci://code.forgejo.org/forgejo-helm
      chart: forgejo
      version: "17.3.2"
      values:
        image:
          tag: "{{ .app.version }}"
        gitea:
          oauth:
            - name: authentik
              provider: openidConnect
              existingSecret: "{{ .auth.secretName }}"
              autoDiscoverUrl: "{{ .auth.discoveryUrl }}"
          config:
            database:
              HOST: "{{ .database.host }}:{{ .database.port }}"
  needs:
    database:
      engine: postgres
      size: 2Gi
  access:
    subdomain: git                  # yields git.<domain-of-the-cell>
    port: 3000
    service: "{{ .app.name }}-http" # service of the chart
    auth:
      mode: oidc
      redirectPaths: [/user/oauth2/authentik/callback]
      secretKeys: {clientId: key, clientSecret: secret}
    groups: [entwicklung]           # only these groups; empty: all users
  health:
    path: /api/healthz
    timeoutSeconds: 600
```

## Fields

| Field | Meaning |
|---|---|
| `version` | Version of the app. For images the default tag, for charts available through `{{ .app.version }}` in the values |
| `digests` | Map of version to the sha256 digest of the multi-arch image index. Only the entry of the current `version` applies: images get `@<digest>` appended, chart values use `{{ .app.digest }}` (empty without an entry). A version-only update therefore never keeps the digest of the old version; it runs by tag until `digests` has an entry for the new version (patch both together to stay pinned) |
| `source.helm` | `repo` (`https://…` or `oci://…`), `chart`, `version`, `values` with placeholders |
| `source.image` | `repository`, `tag` (default: `version`; the image gets `@<digest>` from `digests` for the current version), `command`, `args`, `env` (as in Kubernetes, including `valueFrom`), `files` |
| `source.image.files` | Files that go into the container read-only (`path`, `content`, `executable`). This is how app-specific steps run after installation, for example as a start hook of the image |
| `needs.database` | `engine: postgres`, `size`. One CloudNativePG database per app |
| `needs.volumes` | `name`, `size`, `mountPath` (image apps), at most 16. Charts create their volumes themselves. As with the database, the size cannot be changed yet, because a rollback cannot shrink a volume |
| `needs.secrets` | Names of generated secrets (key `password`), created once, kept across reinstalls |
| `needs.models` | `true`: the app reaches the cell's model gateway with a key of its own (E-81), never the provider's. Its tokens count for the account `app-<name>` and stop at the cell's limit per hour |
| `metadata.name` | Name of the app, at most 40 characters; names starting with `cell-` belong to the platform (its own clients in the identity service) |
| `access` | `subdomain` (not `auth`, `api` and `mcp`, which belong to the cell; only once per cell: a host that another route already serves stays with that route; otherwise the operator reserves the host atomically as ConfigMap `host-<subdomain>` in `cell-system` before it sets anything up; whoever reserves first keeps it until its routes have left it or the app is removed), `port`, `service` (default: app name), `auth`, `groups` |
| `access.auth` | `mode`, `redirectPaths` (OIDC callback paths), `secretKeys` (key names in the client secret, as the chart or app expects them) |
| `access.groups` | Groups of the identity service whose members may use the app; empty means all users of the cell. Needs a login (`oidc` or `proxy`); others are rejected at login |
| `health` | `path` (default `/`) and `timeoutSeconds` (default 300), see Status. Also the deadline within which an update must become healthy. An app that passes its own check and waits only for the login proxy gets 10 minutes more (E-75) |
| `mcp` | `path` of the MCP endpoint of the app (Streamable HTTP on `access.port`). Once the app has been healthy, the MCP gateway of the cell offers its tools, with the app name in front (`notes_memo_list_memos`), only to accounts that may use the app (`access.groups`). The status shows the internal address (`mcpEndpoint`) |
| `backup.schedule` | `none`, `hourly`, `daily` (default), `weekly`. Apps with a database or volumes are backed up, the first time right after installation. Retention is 30 days |

Not implemented yet and therefore not in the schema: backup hooks (O-10), `update` (O-09), `provisioning`, MCP server as a separate container (O-19), object storage, SMTP.

## Login modes

| `auth.mode` | When | What the platform does | Reference app |
|---|---|---|---|
| `oidc` | App supports OpenID Connect | Creates a provider and an application in authentik (implicit consent, so no click) and a secret `oidc` with client ID and secret | Forgejo (chart), Nextcloud (through `user_oidc`, set up by a start hook) |
| `proxy` | App has no login of its own | Forward-auth provider on the embedded outpost, Traefik middleware in front of the app; the app receives the user as `X-authentik-*` headers | Custom-app placeholder (`traefik/whoami`) |
| `none` | App is deliberately open or manages users itself | Nothing | - |

`ldap` is planned but will be implemented only when an app needs it.

## Placeholders

Strings in `source` and `access.service` are Go templates. A placeholder that does not exist for the app (for example `.database.host` without a database) is an error and appears in the status. Passwords are never available as placeholders, only secret names.

| Placeholder | Content |
|---|---|
| `.app.name`, `.app.version`, `.app.namespace` | Name, version, namespace `app-<name>` |
| `.cell.domain`, `.cell.caConfigMap` | Domain of the cell; ConfigMap with `ca.crt` (cell CA) and `ca-certificates.crt` (public roots plus cell CA) |
| `.access.host`, `.access.url` | Hostname and `https://` URL of the app |
| `.database.host`, `.port`, `.name`, `.user`, `.secretName` | Database; the secret has `username` and `password` |
| `.auth.issuer`, `.auth.discoveryUrl`, `.auth.clientId`, `.auth.secretName` | OIDC values; the secret has the keys from `secretKeys` |
| `.volumes.<name>.claim` | Name of the PersistentVolumeClaim |
| `.secrets.<name>` | Name of the generated secret |
| `.models.url`, `.models.openaiUrl`, `.models.model`, `.models.secretName` | Model gateway: the Anthropic Messages address, the OpenAI-compatible one (with `/v1`), the model name to ask for (`default`), the secret with the key in `apiKey`, sent as `Authorization: Bearer` |

## What the operator creates

In this order, each step with its own condition in the status:

1. Namespace `app-<name>` and ConfigMap `cell-ca`
2. generated secrets, database (`DatabaseReady`), waits until it is ready
3. Login client in authentik (`LoginReady`); rewritten on spec change, after an operator restart and every 10 minutes, so that drift heals
4. Workload (`WorkloadReady`): image apps as Deployment (strategy `Recreate`), Service, PVCs; Helm apps as Flux `OCIRepository`/`HelmRepository` and `HelmRelease`
5. Ingress with certificate from the ClusterIssuer `cell`; with `proxy` additionally middleware and outpost route
6. Health check directly against the service (`Ready`); with `proxy` then the check through the outpost, as a browser would come (`Ready=False` with reason `LoginProxyPending` while only this one fails)

Image apps trust the cell CA automatically (mounted bundle, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`). Helm apps mount `{{ .cell.caConfigMap }}` through their chart values. In local cells (`*.localhost`) image apps additionally get the loopback bridge (E-21).

## Status

- `phase`: `Installing` until the first successful health check, then `Ready`. If an app stays unhealthy for longer than `health.timeoutSeconds`, or its last change failed, it becomes `NeedsAttention`. `Updating` during a change, `RollingBack` during a rollback, `Restoring` during a restore, `Removing` during removal.
- `version`: version with which the last health check passed
- `lastBackup`: name and time of the last complete backup
- `url`: address of the app
- `conditions`: `DatabaseReady`, `LoginReady`, `WorkloadReady`, `Ready`, after a failed change `LastChangeSucceeded=False`, each with a readable reason

## Day-2 operation

Besides `App` there are three resources (`api/v1alpha1/operations_types.go`):

- **`Backup`**: database (base backup through barman-cloud) and volumes (restic) of an app in the bucket of the cell. Created on schedule, before every change or on request. Waits while a change or restore of the app is running, and the other way round. A running backup aborts on deletion: the database backup and the job are deleted, restic releases the lock on the repository, and the backup holds the app until no pod of the job is running.
- **`Restore`**: stops the app, restores volumes and database from a backup (name or `latest`) and starts it. Also works in a fresh cell that knows the backup only in the bucket. Starts only when the app is set up (namespace, volumes) and its backup credentials match the backup target of the cell, so that it does not hold up the installation. An app without data (no database, no volumes, no chart) cannot be restored. Before the start the operator configures the app for its current spec (for Helm apps: Flux has rolled out the release for it), so that no workloads run with a different configuration on the restored data. If Flux gives up in the process, the restore fails and the app stays stopped.
- **`ChangeRequest`**: originator, reason, risk level, action, approval. The sum of the `ChangeRequest`s is the change log. Every action waits for the approval, which an admin records through the platform API in a separate call (E-30), and backs up the app beforehand if it has data:
  - `update`: JSON Merge Patch on the app spec, health check within `health.timeoutSeconds`, on failure back to the previous spec and restore of the backup.
  - `install`: creates the app with the spec from `patch` and ends with its first health check. If it does not become healthy, the change fails and the app stays installed for diagnosis. If a `remove` of the same app is still running, the installation waits until it is done, instead of taking over its data.
  - `remove`: deletes the app as described in the section Removal; with `deleteData` afterwards also its namespace with volumes, database and secrets. Backups in the bucket stay.
  - `restore`: restores the backup `backup` (always by name) and waits for the health check. If the app does not become healthy within `health.timeoutSeconds`, the change fails and releases the app; the backup taken before the change holds the earlier state.

  The name is at most 244 characters long, because backup and rollback are named `<name>-pre`, `<name>-rollback` and `<name>-restore`.

## Tasks for agents

A **`Task`** (`kind: diagnose` or `package`) is work for the ops agent. The operator creates a diagnosis when an app becomes `NeedsAttention`, one per cause (health checks fail for longer than the manifest allows; a failed change; a failed restore; name `<app>-diagnose-<checksum of the cause>`), with the conditions of the app as `input`. An admin creates diagnoses or packagings through `POST /v0/tasks`; for `package`, `input` is the Compose file and `app` is the name of the new app. The agent takes the task, works only through the API (every call a step in `status.steps`) and ends it with its answer in `status.result`. If it loses a task (restart, lost API response), it ends the task as failed on its next poll; an admin can ask again. What it wants to change, it proposes as a `ChangeRequest` with `spec.task`; a packaged manifest as `install` with high risk, which the cluster checks before approval (E-35).

## Removal

Deleting an `App` removes workload, ingress and login client. The data stays as a last backup: volumes stay, the database is put to sleep (CloudNativePG hibernation), the namespace gets the label `cell.internal/retained=true`. PVCs from charts are protected from uninstall by a post-renderer with `helm.sh/resource-policy: keep`. If the app is created again, it takes over the data. If a change, restore or backup is running, removal waits until it has ended (a change aborts its running backup in the process and releases the app only when the job of that backup has stopped); a restore that has not replaced any data yet aborts, and new ones no longer start. Data is deleted for good only with an approved `remove` change with `deleteData` (through the API: `DELETE /v0/apps/{name}?deleteData=true`).

## Two tiers in the catalog

- **Integrated**: maintained manifest with login, tested updates and, where available, MCP. The curated default set.
- **Generic**: manifest derived from a Compose file (charts: O-15), generated by the ops agent and installed through `POST /v0/apps` with `manifest`. Runs with subdomain, backup and restart, but without a guarantee for login and updates.

## Answers to the open points of the first version

- **App-specific steps after installation**: as files in the manifest (`source.image.files`), mostly as a start hook of the image that runs idempotently on every start (Nextcloud: install `user_oidc` and create the provider). For charts, through their own mechanisms (Forgejo creates the OAuth source in the init container).
- **Placeholders or a translation layer**: placeholders are enough for all three apps. Two small additions were needed: `secretKeys` (charts expect their own key names) and placeholders in `access.service` (the service name depends on the release name).
- **Apps made of several containers without a chart**: still open; until such an app comes, the way is a Helm chart. Candidate for packaging by the agent.
