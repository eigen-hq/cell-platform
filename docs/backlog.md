# Backlog

All features in one list, without phases (E-37). The order within an area follows the dependencies.

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
| Install by IT: catalog, own manifest | running (catalog); manifest: API running | |
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
| Warnings and notifications to IT | open | System mail |
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
| Own service account in authentik with narrow permissions | open | |
| Pin third-party images by digest | open | |
| Short-lived tokens for the tool gateway | open | |
| Do not count the wait for the login proxy against the deadline of the app | open | |
| Restart of an app as an API action | open | |
| Console: hide actions for viewers | open | |

### Identity

| Feature | Status | Depends on |
|---|---|---|
| Accounts and groups, members, app password | running | |
| Access per app through groups | running | |
| Departments and teams as structure of the identity service | open | |
| Person changes with approval, remove a person | open | |
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

### Models

| Feature | Status | Depends on |
|---|---|---|
| Run models in the cell | open | |
| Models through the customer's own key | open | O-16 |
| Set the model key and model in the console and the API, not only at install (`CELL_LLM_API_KEY`) | open | |
| Provider-agnostic model access: agents speak one model API through a gateway in the cell, any provider or own model behind it; no agent code bound to one provider's SDK | open | O-30 |
| Usage and costs, budget and limits per agent or department | open | |

### Cell

| Feature | Status | Depends on |
|---|---|---|
| Cell and domain | open | |
| System mail of the cell (send only: invitations, password links, warnings) | open | |

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
| Office UI as an app of the cell | open | O-26 |

### Self-hosting

| Feature | Status | Depends on |
|---|---|---|
| Cell across two servers without a private network (node traffic over public addresses, firewall per server) | running, not yet verified (E-61) | Add machine |
| Machines of mixed CPU architecture: place apps only where their images run | open | |
| Update of the platform to a release tag | running (E-59) | |
| Short install address `get.eigen-platform.com` | running (E-59) | |
| Prebuilt platform images from a public registry instead of building on the server | open | |

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
