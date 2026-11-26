# Backlog

All features in one list, without phases (E-37). The order within an area follows the dependencies.

## Direction (E-78)

An agentic operations platform for any software. It runs the apps of a company, and it gives agents one shared brain over them: context collected in the platform where it can be, MCP tools of the apps where it cannot, each agent getting what its permissions allow. Model access is a core part and provider-agnostic. The work happens in the office UI, an app of the cell like any other: everything it gets from the platform, every app can get.

Every feature is thought through to how it is used in the end: by IT in the console, by staff in the office UI and in the apps' own clients, by agents through the API.

## The boundary

- **Platform and console: technical operation.** Run, monitor, back up, check and approve apps. Here IT sees what happens in the cell and decides everything that applies to more than one person.
- **Office UI: the work.** All employees use the apps there, request new ones, ask for features and try them out for themselves first. The office UI lives in separate repos.
- **The seam:** requests from the office UI arrive through the platform API. What affects only one person runs restricted, without approval. What applies to everyone goes through the approval by IT in the console. The platform enforces which app may see which data.

Status:

- **running**: logic and UI are there (`status.md`)
- **API running**: the logic is there (platform API); the UI is still missing
- **open**: logic and UI are built together, once the feature is next

What the console shows comes from the API. Without an endpoint there is no UI, not even one with sample data.

## Platform and console

### Foundation

| Feature | Status | Depends on |
|---|---|---|
| Console in the browser, login through the identity service | running | |
| Overview: state of the cell, open approvals, latest events | running | |
| Live events | running (overview) | |
| Light and dark tone | running | |
| First setup of a new cell | open | |

### Approvals

| Feature | Status | Depends on |
|---|---|---|
| Approve and reject changes | running | |
| Approve requests from the office UI (new app, feature for everyone) | open | Requests API |
| See before approval which data and permissions an app gets | open | Data access per app |
| Improve changes, preview, undo with one call | open | |
| Four-eyes principle | open | O-12 |
| Approval policy (what may run automatically) | open | |
| Update policy | open | O-09 |
| Roles in the console: managers approve for their department, auditors read only | open | |

### Apps in operation

| Feature | Status | Depends on |
|---|---|---|
| Installed apps with state, version, address | running | |
| Install by IT: catalog, own manifest | running (catalog); manifest: API running, console open | |
| Default set: apps a new cell starts with (among them parts of the office UI), installed and updated like any catalog app | open | First setup |
| Own domain per app (e.g. the office UI on `eigen-office.com`): DNS check, certificate, login redirects and the app's base URL follow the domain; the cell address stays as a fallback | open | |
| Own clients per app (mobile, desktop, CLI): the manifest declares them, each gets its own login client (public, PKCE) and reaches the app on its domain | open | Own domain per app |
| Model access for apps: an app declares it in the manifest and gets the cell's model gateway with its own key and budget, no provider key in the app | built (E-81), not yet verified on a cell | Model gateway |
| Packaging of an open-source project by an agent (Compose) | running | Helm charts: O-15 |
| Update and remove | running (update by hand; update policy: O-09) | O-09 |
| Enforce data access per app: which people, which data | open | |
| Reachability per app (public, behind login, company network only) | open | O-05 |
| Support tiers (built-in, integrated, generic, custom-built, adapted) | open | |
| Services of the cell (login, ingress, databases, storage, tool gateway, system mail) | open | |
| Technical actions in an app by an agent (e.g. set up in authentik) | open | O-14, O-18 |

### Adapted apps, technical side

What is wanted and tried out happens in the office UI; the platform builds, operates and approves.

| Feature | Status | Depends on |
|---|---|---|
| Build pipeline for custom-built and adapted apps | open | |
| Personal trial version: an adapted app for one person only, restricted, without approval | open | Build pipeline, data access per app |
| Rollout to everyone after approval by IT | open | |
| Reapply adaptations on every update | open | |

### Monitoring and observability

| Feature | Status | Depends on |
|---|---|---|
| Diagnosis of an app: pods, logs, events | running | |
| Diagnosis by the ops agent | running | |
| Metrics: CPU, RAM, disk per app and cell, history | open | Metrics component |
| Availability and response times per app | open | |
| Map: wiring, traffic, outbound connections, agents | open | Metrics |
| Findings with an agent action (e.g. unused or duplicate apps) | open | Usage numbers |
| Warnings and notifications to IT | built: a mail to the admins when an app needs attention (E-82); other events and channels open | System mail |
| Capacity: when the cell needs to grow | open | Metrics |

### Backups and data

| Feature | Status | Depends on |
|---|---|---|
| Backups per app, trigger a backup now | running | |
| Restore over the current state | running | |
| Restore as a copy | open | |
| Set and change the backup target (console, API) | running | |
| Backup of the whole cell or selected apps at once | open | |
| Include app-own backups (hooks in the manifest: app export, maintenance mode) | open | E-52 |
| Restore test | open | |
| Export of all data and move to own infrastructure | open | |

### Audit and security

| Feature | Status | Depends on |
|---|---|---|
| Change history with originator and result | running | |
| Audit log with export | export of the change history (CSV) running; audit log open | |
| Security overview: second factor, orphaned accounts, admins | open | |
| What goes to the model provider | open | E-48 |
| Who can open what, what leaves the cell | open | Data access per app |
| Escalation to human support (only with consent, time-limited, logged) | open | |

### Before the first customer cell (E-52)

| Feature | Status | Depends on |
|---|---|---|
| Own service account in authentik with narrow permissions | running, not yet verified (E-77; a superuser, see there) | |
| Pin third-party images by digest | running (`deploy/images.lock`, E-76) | |
| Short-lived tokens for the tool gateway | running, not yet verified (E-74) | |
| Do not count the wait for the login proxy against the deadline of the app | running, not yet verified (E-75) | |
| Restart of an app as an API action | running | E-73 |
| Console: hide actions for viewers | running | E-72 |
| Encrypt database backups and their WAL in the cell before they leave it (today only volume backups are encrypted with the backup key) | running, not yet verified (E-67) | |
| Backup targets over HTTPS only, plain HTTP only for a private network on purpose | running, not yet verified (E-68) | |
| A target set again by the installer goes through the operator's checked switch under the gateway lock | running, not yet verified (E-69) | |
| Join keys: the joining machine reads the key without the command line, the key expires on join, the policy bounds its lifetime | running, not yet verified on a real cell (E-64) | |
| Namespaced permissions for operator and API instead of cluster-wide ones | running, not yet verified (E-70) | |
| Signed releases: the installer checks the release and its downloads against a signature | running, release key committed; not yet verified (E-65) | |

### Identity

| Feature | Status | Depends on |
|---|---|---|
| Accounts and groups, members, app password | running | |
| Access per app through groups | running | |
| Departments and teams as structure of the identity service | open | |
| Invite a person: a mail with a link, the person sets password and second factor; no credentials sent by hand | built, not yet verified on a cell | System mail |
| Remove a person (admins; not themselves, superusers or service accounts) | built, not yet verified on a cell | |
| Person changes with approval | open | |
| Account help: password, second factor, lock, sessions | open | |
| Login policy | open | |

### Agents

| Feature | Status | Depends on |
|---|---|---|
| Agent tasks with all steps | running | |
| Free-form tasks for the ops agent from the console (e.g. "set up backups to this bucket"), acting through the API, with approval | open | |
| Agents as accounts with permissions per app | open | |
| Autonomy levels | open | |
| Tool gateway for all apps | API running | O-18, O-19, O-20 |
| Tools per agent: each agent gets the MCP tools of the apps it may use, changing with apps and permissions | open | Tool gateway |
| Context index: documents, tickets, people and their references across all apps, in the platform's database and storage; own apps write directly, third-party apps are indexed | open | Data access per app |

### Models

A core part of the platform, not an add-on (E-78): agents and apps reach models only through the cell's gateway, in one open API, so a provider can be swapped without changing an agent or an app.

| Feature | Status | Depends on |
|---|---|---|
| Model gateway: one open model API in the cell, any provider or own model behind it; no agent code bound to one provider's SDK | built (E-80); the agent still uses the Anthropic SDK against the gateway | O-30 |
| Providers and keys in the console and the API (the customer's own keys), not only at install (`CELL_LLM_API_KEY`) | built (E-80) | Model gateway |
| Model per task: which model an agent uses, fallback to another provider | open | Model gateway |
| Usage and costs, budget and limits per agent, app or department | built: tokens per account and one hourly token limit for each (E-81); budgets in money, per department and durable usage open (O-31) | Model gateway |
| Run open models in the cell | open | Model gateway |

### Cell

| Feature | Status | Depends on |
|---|---|---|
| Cell and domain | open | |
| System mail of the cell (send only: invitations, password links, warnings) | built (invitations, warnings), not yet verified on a cell | |

### Distributed cell

One cluster across all machines, WireGuard in between (E-42); network for people through a mesh of its own (E-43); machines are added through the console (E-44). Running (`status.md`): machines with state, join command from the console, hardware load per machine with its apps, WireGuard between the machines. The rest is open.

- Machines and sites of a cell with state
- Add a machine: join command from the console (E-44)
- Add a machine over SSH from the console, building on the join command
- Remove a machine (apps and volumes move beforehand)
- Placement: which app runs where, bind an app to a site
- Hardware load per machine and app
- Capacity and redistribution (agent proposes moving or more hardware)
- Failure of a machine or site: what keeps running, what moves
- Latency per site
- Connection between the machines (WireGuard, E-42)
- Access for people through the mesh (running, E-56); console shows how a device joins; apps reachable only in the mesh; connection to the company network

### Seam to the office UI

| Feature | Status | Depends on |
|---|---|---|
| Requests API: new app, feature request, personal trial version, with group of people and data access; status for the requesters | open | O-27 |
| Events for the office UI (request approved, app ready) | open | O-27 |
| Shared login (own client per UI) | open | E-52 |
| Office UI as a catalog app of the cell, on its own domain; no special path in the platform (E-78) | open | Own domain per app, shared login |

### Self-hosting

| Feature | Status | Depends on |
|---|---|---|
| Cell across two servers without a private network (node traffic over public addresses, firewall per server) | running, not yet verified (E-61) | Add machine |
| Machines of mixed CPU architecture: place apps only where their images run | open | |
| Update of the platform to a release tag | running (E-59) | |
| Short install address `get.eigen-platform.com` | running (E-59) | |
| Prebuilt platform images from a public registry instead of building on the server | open | Image registry in the cell |

### The platform's own development on a cell

The platform's own development and releases run on a cell, as the first real user of it.

| Feature | Status | Depends on |
|---|---|---|
| Git hosting and CI as apps of the cell (Forgejo with Forgejo Actions) | open | |
| Image registry in the cell (Forgejo's container registry; Zot or Harbor if it falls short) | open | |
| Releases, install address and platform images served from the cell | open | Git hosting, image registry |

## Belongs to the office UI

Only what the platform provides for it is listed here.

| Feature | What the platform provides |
|---|---|
| Launch the apps one may use | List of apps per person (API running) |
| Request a new app, for certain people, with certain data access | Requests API, approval in the console |
| Ask for a feature for an app | Requests API, build pipeline |
| Try out a requested feature for yourself first | Personal trial version |
| Give agents jobs in your own words | Tasks through the API |
| Guided flows: someone joins, someone leaves, a team needs a tool, create a team, give a department an app, something is broken, get something back | Requests API; execution and approval in the platform |
| Connect your own assistant | Tool gateway, tokens |
| Appearance per customer | - |
| Mail, mailboxes, central launcher for all tools | - |

## Deliberately out

- Migration of existing IT landscapes
- Billing, central service across several cells
- Device management
