# Status

What the platform can do today and how it is verified. Every capability has an automatic evidence check.

## Overview

- Last updated: 2026-11-24
- `make demo` runs the target demo against the local cell (steps 2 to 6, each an end-to-end test with a log); step 1 on a fresh VM is `make vm-test`
- Open questions are in `decisions.md`

## Demo

This walkthrough runs without manual work:

1. Fresh VM, one command, and a few minutes later the cell is up.
2. Three apps are installed and are usable at once with single sign-on.
3. One app is broken on purpose. The Ops agent finds the cause, proposes the repair and, after approval, restores the app.
4. An update fails and is rolled back automatically, without data loss.
5. A previously unknown app is packaged and installed by the agent.
6. An MCP client sees the tools of the installed apps through a single endpoint.

## What runs

### Cell by command

A fresh VM becomes a working cell.

Status 2026-11-19:

| Check | How | Result |
|---|---|---|
| Fresh VM to login page | `make vm-test`: Ubuntu 26.04.1 LTS in Lima, 4 vCPU, 8 GiB | 522 s total (149 s VM start incl. image download, 373 s installation) |
| Idempotent | second run of `install.sh` or `make cell-up`; generations and resource versions unchanged | passed (`make vm-test`, `make e2e`) |
| Snapshots | PVC on Longhorn, `VolumeSnapshot` becomes `readyToUse` | passed (`make vm-test`) |
| Local cell | `make cell-up` from nothing, images cached | 234 s |
| Resources, empty cell (VM) | `free -m`, `kubectl top` after 60 s idle | 3.4 GB used, 4.5 GB available, 0.36 CPU idle; authentik alone 965 MiB, Longhorn about 450 MiB |
| Resources, empty cell (local) | `kubectl top` | pods 1.05 GiB, node 2.6 GiB (without Longhorn) |
| Automatic address (E-55) | unit tests `internal/names`: registering only with a key, name and address checked, wildcard set, move and proofs only with the own token, subdomain checked strictly, the newest proof replaces the previous one, Hetzner client against a simulated API; `TestACMEAccounts` (account per app host), `deploy/lib_test.sh` (platform accounts) | passed (2026-11-25); real certificate via Hetzner DNS and Let's Encrypt not yet shown, needs a zone and a token |
| Mesh (E-56) | local cell, mesh set up by hand with the address of the node: Headscale starts only once the login at authentik is reachable; the router registers with the one-time key (tag `tag:cell`), its route is approved automatically; a test device joins, finds its own relay (`Nearest DERP: Cell`, UDP yes) and reaches Traefik over the route (80 and 443), 6443 and 10250 of the cell stay blocked over the mesh (directly reachable); the mesh section of `bootstrap.sh` ran against the local cell; `make vm-test`: setup including mesh on a fresh Ubuntu VM passed (777 s), second run unchanged (the later Longhorn step ran into a timeout, the server VM with 5 GiB was overloaded); `deploy/lib_test.sh` (port check, also with GNU tools on Ubuntu 24.04) | passed (2026-11-25); device login through the browser and clients outside the cell not yet shown |
| Machine network separate from the address of the cell (E-57) | `bash -n`, `deploy/lib_test.sh`; setup with `CELL_NODE_IP` (private) and `CELL_INGRESS_IP` (public) | open: to be shown on the two VPS with a private network |
| Install with little manual work (E-58) | `bash -n`, `shellcheck`, `deploy/lib_test.sh` (private address of a network card detected, bridges and tunnels ignored) | open: prompt, DNS check and password display to be shown on the VPS |
| Install with one command from the latest release, output as steps with a log (E-59); the active step shows a spinner, its time and what it does, with the count of ready services while it waits | `bash -n`, `shellcheck`, `deploy/lib_test.sh` (also `wait_ready`); rendering checked in a pseudo-terminal (steps, hints, errors with command and end of log; live line in an Ubuntu container, 70 columns, never wraps); 2026-11-26 on the local cell: `make test` and `make e2e` green, a backup target set through the operator stays unchanged on an installer rerun (`TestBootstrapIdempotent`), the join key policy refuses an empty expiry (server dry run) | open: `get.sh` against the published release and the installation to be shown on the VPS |
| Backup target in the console and through the API (E-60) | `go test` (API: roles, inputs, no keys in the response, 409; operator: job, message, status, cache), `npm run check`; envtest suite in the daily run | open: probe against real S3 (Hetzner Object Storage) and the view in both tones to be shown on the VPS |
| Backup targets over https only (E-68) | one case table (`internal/cell/testdata/endpoints.txt`, 30 cases: host names, public and private IPs, CGNAT edges, IPv6 ULA, link-local, IPv4-mapped, short IPv4 forms) checked by `go test ./internal/cell` and by `deploy/lib_test.sh` against the installer's check; the installer stops on a refused target or an S3 value with a line break or rclone config syntax before its first `kubectl` call (`deploy/lib_test.sh`, fake `kubectl`); API (`go test ./internal/api`: http without the flag, to a host name or to a public IP is 400, http to 10.0.0.5 with the flag is 202), operator (envtest `TestBackupTargetCheck`: such a request ends failed without a job), console (`npm run check`); `bash -n`, `shellcheck` | built, waiting for the daily run: `make e2e` with the local plain-http target through `CELL_BACKUP_S3_PRIVATE_NETWORK=true` (`TestBootstrapIdempotent` reruns the installer), and an installer rerun on the two VPS cells (installed with v0.2.4/v0.2.5, backups present): the stored target is kept and not judged again, a rerun with the same https variables changes nothing |

### Cell across several machines

Further machines join through the platform (E-42, E-44): an admin gets a command in the console or through `POST /v0/nodes/joins` and runs it on the new machine. The machine loads everything from the cell, connects over WireGuard and gets the platform image from the first machine, without a registry. Installation and updates to a release tag: `deploy/release.sh <tag>` on the server (built there, from Git); has not run yet, the first run is the installation on the VPS.

Status 2026-11-24:

| Check | How | Result |
|---|---|---|
| Second machine joins | `make vm-test`: two fresh VMs (Ubuntu 24.04.5 LTS in Lima, 4 vCPU/5 GiB and 2 vCPU/3 GiB, shared network `user-v2`); admin token as in `test/e2e`, `POST /v0/nodes/joins`, the command runs unchanged on the second VM. Since then the key is no longer in the URL but in the command (`CELL_JOIN_KEY`); the next daily run is the first to check this form | joined in 21 s; `GET /v0/nodes` shows server and agent, both ready, with the addresses that `node.sh` found itself |
| Connected, encrypted | `flannel-wg` on both machines; a pod on the second machine reaches `cell-api` on the first | passed |
| Image without registry | pod with the platform image on the second machine; the image exists only on the first (mirror of k3s) | passed; the mirror does not share images loaded with `ctr import`, hence the images folder of k3s |
| Storage on the second machine | Longhorn volume and snapshot on the second machine | passed |
| Server installation | same run, first machine | 47 s VM start, 438 s to the login page; second run changes nothing |
| Resources | `kubectl top` after 60 s idle | server 3.6 GiB, agent 0.8 GiB |
| API and key | `make test`, `TestNodes`, `TestJoin` against `api/openapi.yaml`: only admins get keys, the key is a bootstrap token valid for one hour; the policy `cell-api-join-keys` lets the API create nothing else and read nothing in `kube-system` (checked against the local cell) | passed |
| Own address for 80/443 (E-53) | Ubuntu VM (Lima), `install.sh`: login answers on the address of the cell (302), 443 on localhost stays free, no ServiceLB; `node.sh` aborts if another program holds 443 on the address. The full `make vm-test` ran up to the identity and failed there on the 13 min download of the Postgres image | passed (2026-11-25); full `make vm-test` waits for the daily run |

### App manifest and operator

An app is installed by its manifest alone.

Status 2026-11-19: Helm through Flux (E-18), manifest sharpened (`app-manifest.md`).

| Check | How | Result |
|---|---|---|
| App from manifest, subdomain, SSO without manual steps | `make e2e`, `TestReferenceApps`: login as a test user through the authentik flow, then the test asks the app who is signed in | passed for Forgejo, Nextcloud, custom-build placeholder |
| Delete pod | same test deletes all app pods and waits until the session answers again | passed |
| Removal keeps data | same test deletes the app, checks namespace and volumes, reinstalls and signs in again | passed |
| Apps isolated (E-45) | `make e2e`, `TestReferenceApps`: a pod outside the platform does not reach the service of the app, one in the namespace of the app does; a privileged pod is rejected. Helm apps run as the `helm-deployer` of the namespace (envtest) | passed (2026-11-25) for Forgejo, Nextcloud, custom-build placeholder; full `make e2e` afterwards 15 min green |
| Operator logic | `make test`: envtest with a real API server (database first, placeholder, login, proxy, Helm, `NeedsAttention`, removal) | passed |
| Local run times | first installation including image download | custom-build placeholder 10 s, Forgejo about 2 min, Nextcloud about 2 min |

### Day-2 operations

Backup, restore and update run deterministically.

Status 2026-11-19: backups through barman-cloud and restic (E-23 to E-28), local backup target next to the cell.

| Check | How | Result |
|---|---|---|
| Restore in a fresh cell | `make dr-test`: marker in the database and volume of Forgejo, backup, delete and rebuild the cell, install Forgejo, restore from the bucket, then login with the new identity | data complete, login works; about 12 min including rebuild of the cell |
| Restore in the same cell | `make e2e`, `TestBackupAndRestore`: marker, backup, overwrite marker, restore | Forgejo and Nextcloud back at the state of the backup, about 100 s each |
| Backups through the gateway (E-47) | `make e2e`, `TestBackupAndRestore` against `backup-gateway` with the local S3 target behind it; beforehand by hand: an app key sees only `<root>/apps/<app>/`, another app sees nothing there, a wrong secret gets 403; unit test `TestAppSecret` keeps operator and auth proxy identical | passed (2026-11-25), layout in the target unchanged |
| Failed update | `make e2e`, `TestFailedUpdateRollsBack`: `ChangeRequest` to a version that does not exist | after the 90 s health deadline back on 15.0.11, data as before, app `NeedsAttention`, `ChangeRequest` `RolledBack` |
| Change log | every `ChangeRequest` holds author, reason, risk, patch, previous spec, backup, restore, result and times | checked in `make test` (envtest) and `make e2e` |
| Control logic | `make test`: scheduled backup, approval, successful change, rollback with restore, rejection | passed |

### Platform API

Everything around apps and operations works without `kubectl`.

Status 2026-11-19: platform API `cell-api` (E-29), approvals only through the API (E-30), install, remove and restore as actions of a `ChangeRequest` (E-31).

| Check | How | Result |
|---|---|---|
| Apps and operations only through the API | `make e2e`: install, login, remove and reinstall of the three reference apps, backup and restore, failed update with rollback, restore into a fresh cell (`make dr-test`), all through `https://api.<domain>` with tokens of the identity and approval by an admin. `kubectl` only plays the world around the cell (delete pods, write test data) and looks inside where the API shows nothing (kept namespace) | passed |
| Approvals | `make e2e`, `TestChangeApproval`: the agent proposes and may not approve; a cluster admin can neither create an approved `ChangeRequest` nor approve past the API; the admin declines through the API; the event stream reports the change | passed |
| Access | `make e2e`, `TestAccessGroups`: app restricted to a group, a member gets in, the identity rejects others, after opening up everyone gets in | passed |
| OpenAPI file up to date | `make test`: every route is in `api/openapi.yaml` and vice versa, every response in the API tests matches its schema (no additional fields allowed) | passed |
| Control logic | `make test`: install, remove with and without data, restore as a change, access groups, validation of the actions; tokens (audience, rejection by the identity, cache) | passed |

### Ops agent

The agent solves two tasks that no rule covers.

Status 2026-11-19: agent service `cell-agent` with its own account (E-32), customer's own model and key (E-33), tasks as the resource `Task` with steps (E-34), install from a manifest with a check by a dry run (E-35). Compose files are packaged; Helm charts are O-15.

| Check | How | Result |
|---|---|---|
| Scenario A | `make e2e`, `TestAgentRepairsApp`: a wrong value (`WHOAMI_PORT_NUMBER=8080`) set behind the platform's back, the operator creates a diagnosis task, the agent reads app, pods and logs through the API, finds the cause in the log and proposes the repair; after approval the app is healthy | passed |
| Scenario B | `make e2e`, `TestAgentPackagesApp`: Memos from its Compose file; the first proposal with a subdomain of the cell is rejected by the dry run, the corrected one is installed after approval without rework | passed |
| Every step in the log | `make e2e` and `make test`: every call of the agent is a step in its task, also with parallel calls; without a task or in foreign tasks the API rejects the call; proposals carry their task | passed |
| API creates no apps | `make e2e`, `TestAPICannotCreateApps`: the account of the API may create apps only in a dry run (ValidatingAdmissionPolicy) | passed |
| Agent | `make test`: diagnosis and packaging with a scripted model (fake of the Messages API), lost tasks (restart, lost response when taking over or finishing), model not reachable; one diagnosis per cause, also after a failed restore | passed |

The model is scripted in the tests: everything around the model is checked (tools, permissions, log, dry run, installation), not the quality of the diagnosis. A run with a real model needs a key (O-16).

### MCP gateway

One endpoint for the tools of all apps.

Status 2026-11-20: agentgateway as MCP gateway, configured by the operator from the manifests (E-36), field `mcp` in the manifest, Memos in the catalog as the first app with its own MCP endpoint. Memos answers calls to Memos tools with its login error, because the gateway has no Memos token (O-18); apps without their own endpoint are O-19, the limits of v0 are O-20.

| Check | How | Result |
|---|---|---|
| Tools of two apps through one endpoint | `make e2e`, `TestMCPGateway`: two Memos from the catalog (`notes` for everyone, `journal` only for a group); an account of the group sees the tools of both through `https://mcp.<domain>/mcp`, with the app name in front; its call reaches the app | passed |
| No access, no tools | same check: an account outside the group sees only `notes_*`, its call to a `journal_*` tool is rejected; without a token or with a token of the API the gateway answers 401, a token of the gateway is not valid at the API | passed |
| Access rules | `make test`, `TestGatewayConfig`: the rules evaluated with cel-go, also with group names that contain CEL themselves; apps without `mcp`, apps never healthy and deleted apps are missing | passed |
| Memory | gateway idle, local cell | 11 MiB |

### Console

Admins sign in to the console of the cell in the browser.

Status 2026-11-24: console in Svelte at `https://console.<domain>`, served by `cell-api` (E-39), own login client with PKCE, for `cell-admins` and read-only for `cell-viewers` (E-40, E-46), look from design tokens in two tones, Night and Paper (E-41). So far: login, base layout, overview, approvals, apps, machines, organization, agents and history of the cell; the other views follow with the features in the backlog.

| Check | How | Result |
|---|---|---|
| Login in the browser | `make e2e`, `TestConsoleLogin`: an admin signs in in Chromium (Playwright), sees their name and the navigation, switches the tone, reloads; every error in the browser console on console pages, also a rejection by the CSP, fails the test | passed |
| Logout and errors | same test: logout discards the token and lands on the logout page of the identity; its single sign-on stays (E-40); if the API rejects a token right after login, the console shows the reason instead of signing in again endlessly | passed |
| Login protocol | same test without a browser: code with PKCE, exchange for a token from the origin of the console (CORS of the identity), with the token `GET /v0/whoami` with role `admin`; the identity rejects an account outside `cell-admins` | passed |
| Roles from groups (E-46) | unit tests `internal/api` (`TestRoleOf`, `TestAuthentication`): without a group role `none`, `whoami` answers, everything else 403 with a hint to `cell-viewers`; viewers read only the state of the cell (E-63): `TestViewerRoutes` pins the routes open to them, `TestViewerRedaction` checks that spec, patch, agent reasons, status messages and the access key are stripped (sentinel values in every viewer read, `TestEventStream` for the live events) and that people, groups, tasks, pods, events and logs answer 403; may change nothing | passed (2026-11-26); console as viewer (`viewer.spec.ts`) waits for the daily run; console as viewer waits for the daily run |
| Delivery | `make test`, `TestConsole`: console only on its host, `/v0/` stays the API there, unknown API paths stay JSON errors, missing files under `/assets/` give 404 instead of the page, headers (CSP, cache); `TestLogin` against `api/openapi.yaml`; the subdomain `console` cannot be taken by an app (envtest) | passed |
| Machines | `make e2e`, `TestConsoleLogin` (Playwright, `machines.spec.ts`): view "Machines" after the Figma design "16 Cell": key figures, picture of the connections (people over https, machines over WireGuard), per machine apps, processor, memory and disk (measurements from `metrics.k8s.io`, disk from Longhorn); "Add machine" shows the join command, on the local cell the reason why none is possible; a login that starts on `/machines` returns there | first version passed; revised version waits for the daily run |
| Apps | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): view "Apps" after "06 Apps": installed apps with state, groups, last backup and address; catalog with "Install" (change waits for approval); an app opens in the drawer after "07 Drawer - app" at `/apps/<name>`: version, change access, backups with "Back up now" and "Bring back", events, "Let Ops diagnose it", removal with confirmation. Viewed with simulated API responses in both tones | waits for the daily run |
| Approvals | `make e2e`, `TestConsoleLogin` (Playwright, `approvals.spec.ts`): view "Approvals" after "02 Approvals": every waiting change as a ticket with requester (human or agent), sentence on the change, picture of requester, app and groups, reason, risk, protection during the run, steps after the yes and a patch to expand; Approve and Decline (with confirmation); next to it the admins from `cell-admins` and what a yes needs. Viewed with simulated API responses | waits for the daily run |
| History | `make e2e`, `TestConsoleLogin` (Playwright, `history.spec.ts`): view "History" after "25 History": the last 30 days as a strip (changes, agent tasks, backups, the worst per day), filters (waiting, by humans, by agents, not applied), export of the shown rows as CSV, table with requester, decider, risk, time and outcome; a change opens at `/history/<name>` in the drawer after "26 Drawer - change rolled back" with the steps and their outcome. Viewed with simulated API responses | waits for the daily run |
| Agents | `make e2e`, `TestConsoleLogin` (Playwright, `agents.spec.ts`): view "Agents" after "21 Agents": who worked in 30 days (rules: scheduled backups, backups before changes, rollbacks; agents: their tasks; humans: their changes), Ops and Packer with their tasks, work board (waiting for you, agents working, finished this week), what may happen without humans; a task opens at `/agents/<name>` with the order, every API call, response and proposed changes; tickets and history link the task of an agent. Viewed with simulated API responses | waits for the daily run |
| Organization | `make e2e`, `TestConsoleLogin` (Playwright, `organization.spec.ts`): view "Organization" after "18 Organization", with flat groups instead of departments: key figures, "Everyone" with the apps for all, per group members and their apps, new group, agent accounts; people with groups, add person (with first password), app password (shown only once); a group opens at `/organization/<group>` to change the members; saving sends only who joins and who leaves (`PATCH /v0/groups/<group>/members`, one call to authentik per person), so no admin undoes the simultaneous change of another (`TestIdentity`, `TestChangeGroupMembers`). App passwords are created one at a time. Viewed with simulated API responses | waits for the daily run |
| Overview | `make e2e`, `TestConsoleLogin` (Playwright, `home.spec.ts`): start page after "01 Home": key figures (state, running apps, open approvals, last backup, agents), a headline from the state ("Notes is down. A fix is waiting for your OK."), per app a shape whose height shows how many people may open the app (lying down if its health check fails; an app whose change failed and which keeps running does not count as down), the newest open approval as a ticket with yes and no, the apps with state and reach, the latest changes and who works in the cell (agents, rules, people). The headline, the reach and the last backup are checked as unit tests (`console/unit/home.test.ts`). Viewed with simulated API responses in both tones | waits for the daily run |
| Pods and logs per app | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): in the app drawer "What runs": per pod its containers with a dot (ready, waiting, failing), restarts, last exit and message; "Log" shows the last 300 lines, "Before the restart" the run before the last crash. Viewed with simulated API responses | waits for the daily run |
| Update an app from the console | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): in the app drawer "Version" enter a new version or take the one from the catalog; this becomes a change `PATCH /v0/apps/<name>` with `{version}`, risk medium, which waits for approval like any change (backup before, rollback on a failed health check). While an update of the app is open, no second one can be requested | waits for the daily run |
| Package from the console | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): under Apps "From an open-source project": name of the app and Compose file (upload or paste) become the task `POST /v0/tasks` of kind `package`; Packer proposes the installation, which waits for approval like any change. The name is checked beforehand (like the API: starts with a letter, lowercase letters, digits, hyphens, at most 40, not already installed), the file is limited to 65,536 characters (`console/unit/package.test.ts`); a link leads to the task under Agents | waits for the daily run |
| Live events in the console | `make e2e`, `TestConsoleLogin` (Playwright, `home.spec.ts`): the overview reads `GET /v0/events` (Server-Sent Events, with `fetch`, because `EventSource` cannot send a token) and reloads on every event, several in quick succession count once; if the stream ends, it reconnects, the pause grows from 1 to 30 seconds. "Live" shows whether the stream is up. Parsing of the stream, pauses and reconnecting are checked as unit tests (`console/unit/live.test.ts`) | waits for the daily run |
| Helper functions | `make test` (`npm run check`): time stamps like "12 min ago", sentences, groups, participants and steps of a change (`console/unit/`, `node --test`) | passed |
| Tokens | `make test`, `TestTokens`: tokens of the console are valid at the API, tokens of the MCP gateway and of apps are not | passed |
| Types and contract | `make test` checks the console with `svelte-check`, the API types come from `api/openapi.yaml` | passed |
| Size | production build | JavaScript 71 KB (28 KB gzip), CSS 15 KB, fonts 54 KB |

## Risks

| Risk | Handling |
|---|---|
| Every app needs its own manual steps for login | Three deliberately different reference apps; move the pattern into the manifest |
| Volumes live on one machine (one replica, E-14) | If the machine fails, the app is gone until it returns; backups outside the cell; replicas across machines are a separate step |
| Ports between the machines open without a firewall | Machines talk only on the shared network (E-57); the firewall blocks 6443, 10250, 5001 and 51820 from outside; without a firewall they keep listening on all addresses |
| Base services need too much memory for a small VM | Measured (see Cell by command); if needed, swap components |
| "Runs" is not "integrated" | Make two levels visible in the catalog: generic and integrated |
| Trust in agents | Approval as the default, log from the start |

## Local development

For the fast cycle, a local cell (k3s in Docker). It has no snapshot-capable storage; everything around snapshots and restore is accepted on a real VM.
