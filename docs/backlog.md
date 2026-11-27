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
| Platform database: one Postgres (`cell-db`) for records that are not desired state (task conversations, model usage, later memory and the context index); only the API reaches it, except own apps writing to the context index; backed up like an app's database, restored by `make dr-test`; after a restore, conversations whose header is gone come back read-only and no run resumes, and the test proves that no finished change runs again; today's tasks move in once | built: `cell-db` with the transcripts of tasks (E-98); backup, `make dr-test`, model usage and the move of today's tasks open | |

### Approvals

| Feature | Status | Depends on |
|---|---|---|
| Approve and reject changes | running | |
| Approve requests from the office UI (new app, feature for everyone) | open | Requests API |
| See before approval which data and permissions an app gets | open | Data access per app |
| Improve changes, preview, undo with one call | open | |
| Four-eyes principle | open | O-12 |
| Checks on a change: a change names the checks that prove it worked (from a fixed catalog with parameters, each with its data source: healthy, processor or memory below or above a value and no restarts first, response time with the probes, a log line gone once logs are kept), editable before approval; a result is passed, failed or inconclusive, and inconclusive never counts as passed; each kind of change says which rollback it has, and data a rollback would lose is shown before approval; the operator runs the checks in a verify window (rollback where the kind has one) and a watch window (task reopens, IT is warned; a change without a task gets a diagnosis task), keeps their state across restarts, and writes a receipt with before and after | open | Metrics, availability and response times per app |
| Try on a copy: before it proposes a risky change, an agent applies it to a restored copy of the app and runs the checks there | open | Restore as a copy, checks on a change |
| Approval policy: what the platform can undo applies itself, a person decides the rest (E-95) | decided, open | Autonomy levels |
| Update policy | open | O-09 |
| Roles in the console: managers approve for their department, auditors read only | open | |

### Apps in operation

| Feature | Status | Depends on |
|---|---|---|
| Installed apps with state, version, address | running | |
| Install by IT: catalog, own manifest | running | |
| Default set: apps a new cell starts with (among them parts of the office UI), installed and updated like any catalog app | open | First setup |
| Own domain per app (e.g. the office UI on `eigen-office.com`): DNS check, certificate, login redirects and the app's base URL follow the domain; the cell address stays as a fallback | open | |
| Own clients per app (mobile, desktop, CLI): the manifest declares them, each gets its own login client (public, PKCE) and reaches the app on its domain | open | Own domain per app |
| Model access for apps: an app declares it in the manifest and gets the cell's model gateway with its own key and budget, no provider key in the app | running (E-81, verified on the test cell 2026-11-27) | Model gateway |
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
| Metrics: CPU, RAM, disk per app and cell, history | CPU and memory per container and machine, now and over 1 h, 24 h or 7 d, running (E-87); disk history open | Metrics component |
| Availability and response times per app: two probes per app (blackbox_exporter, read by the collector), one through the public address for what people reach, the login included, and one inside the cell straight to the app's service for the app itself; requests, errors and durations per app from the ingress | open | Metrics |
| Action record: every action in the cell (changes, restores, restarts, rollbacks, machines joining or leaving, cell settings), by people, agents or the operator, with actor, start and end, targets, what it affected, outcome and task; in the platform database | open | Platform database |
| Impact of every change: every action record (by people, agents or the operator) is a marker on every chart it touches; each shows the key figures of what it affected 1 hour, 1 day and 1 week before and after, with the other actions in the same window; computed by the API, no model | open | Action record, metrics, availability and response times per app |
| Trends: what shifted in the cell over a period, next to the changes around it | open | Impact of every change |
| Map: wiring, traffic, outbound connections, agents | open | Metrics |
| Findings with an agent action (e.g. unused or duplicate apps) | open | Usage numbers |
| Warnings and notifications to IT | built: a mail to the admins when an app needs attention (E-82); other events and channels open | System mail |
| Capacity: when the cell needs to grow | open | Metrics |
| Memory and CPU limits per app: set in the manifest, changed as a change with approval, shown next to the use | open | Metrics |

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

How people work with agents is in `agents.md`, the design in `architecture.md` (Agent harness), the reasons in E-88 to E-90. The tables are in dependency order. Each feature counts as running only with its check: the scripted-model tests (a fake of the model API) for everything around the model, `make e2e` for the flow through the API, Playwright for the console.

#### Harness and conversation

| Feature | Status | Depends on |
|---|---|---|
| Agent tasks with all steps | running | |
| Free-form tasks for the ops agent from the console (ask box on Home and per app, E-85), acting through the API, with approval | running (asks about the cell verified on the test cell); credentials and cell-level changes open (O-32) | |
| Neutral model API: the agent's own lean loop and a thin client for one open, stateless model API through the model gateway; no provider SDK in agent code (anthropic-sdk-go goes). Same behaviour as today: scenarios A and B and the agent tests pass against a scripted fake of the new API. Wire API Chat Completions (E-94). First check on the test cell: the gateway's Chat Completions route with tool calls and reasoning against an Anthropic and an OpenAI-compatible provider | built, fake-tested; check on the test cell open | Model gateway |
| Conversations: a task holds turns of typed items; follow-ups on any task, also a finished one; the agent asks back and the task waits for you; several tasks at once, up to a limit per agent; resume after a restart without acting twice (one owner per run, an idempotency key on every tool call, catch-up before events; checked by scripted crash tests); a token budget per model, with *limit reached* when a task outgrows it; stop, also of child tasks; every step keeps an excerpt of its response, without the cap of 200 | built: turns in the transcript (E-98), follow-ups and questions (E-99); several tasks at once, resume without acting twice, the token budget, stop and the step excerpts open; the 200 steps and 2000 items still count over the whole conversation | Neutral model API, platform database |
| Live progress: run, step, tool call, text, stage and waiting-for-input on the event stream; the console and the agent stop polling; task content only on the task's own stream, `GET /v0/events` says only that a task changed; the model client streams | open | Conversations |
| Task thread in the console: reply box, *Waiting for you* with the agent's question, stop | built: reply box on every task an agent finished, *Waiting for your reply* with the question on Home and in Agents, replies in the conversation (E-99); stop open | Conversations, live progress |
| Memory across tasks: what agents learned per app and about the cell, shown to admins, who can correct or delete it | open | Conversations |

#### Answers

| Feature | Status | Depends on |
|---|---|---|
| Structured answers: a turn ends in `answer` or `ask`; Understood, Causes (confirmed, ruled out, suspected, each with its evidence steps), Options (change, risk and reversibility from the API, effort, checks that define done, a recommendation with its reason), Receipt; the options of one answer exclude each other and an approval names the revision it saw; the figures a claim rests on are kept with the answer; the API rejects evidence that is not a step of the task and marks claims without evidence | open | Conversations |
| Credentials and cell-level asks: the agent asks for a credential through a question the API issues and binds to its Secret; the value goes straight there, never into the transcript, events or logs; cell-level proposals (backup target, mail, models) as changes | open | Structured answers, O-32 |
| Task page with stages (Understood, Causes, Options, Approval, Apply, Verify, Watch, Done) and depth (Summary, Reasoning, Evidence, Raw) | open | Structured answers |
| Answer blocks: an answer shows the console's own components (state, chart with change markers, log excerpt, diff, checks); a block names an allowed reading operation of the API with checked parameters, and the console loads the numbers with the viewer's rights; a block behind a claim has a fixed window, others are marked live | open | Structured answers, impact of every change |
| Act on an answer: *Correct me* on what the agent understood, *Ask about this* on any block, step or chart | open | Task thread in the console, task page with stages and depth, answer blocks |
| The agent follows its change: a verify or watch result wakes the task; the agent writes the receipt or proposes the next step | open | Structured answers, checks on a change |

#### Tools and agents

| Feature | Status | Depends on |
|---|---|---|
| Tool gateway for all apps | API running | O-18, O-19, O-20 |
| Tools from the API contract: every operation in `api/openapi.yaml` marked for agents is a tool with its description and schema; one catalog for the loop and for MCP; replaces the 12 hand-written tools | running for the loop (E-96); MCP with Interop | Neutral model API |
| Agents as accounts with permissions per app: an `Agent` resource with its own account, instructions, tools, apps, model and budget; Ops and Packer become two of them; an agent can hand part of a task to another (a child task, with at most the rights and the remaining budget of its parent, nested to a fixed depth); every call in a task has only the rights that both the agent and the requester have | API running for accounts, kinds, apps, tools and instructions (E-97); open: model and budget per agent, child tasks, requester rights, console screen | Tools from the API contract |
| Tools per agent: each agent gets the MCP tools of the apps it may use, changing with apps and permissions; calls go through the API, which records them | open | Tool gateway, agents as accounts |
| Autonomy levels: agents apply changes the operator can undo themselves, with snapshot, checks and automatic rollback; a person decides deleting data or backups, uninstalling, restoring over the current state, unreviewed manifests, patches to source, needs or subdomain, spend, anything outside the cell and every widening of rights (E-95, replaces A-10); admins tighten per agent and kind of change | open | Checks on a change, agents as accounts |
| Context index: documents, tickets, people and their references across all apps, in the platform's database and storage; own apps write directly, third-party apps are indexed | open | Data access per app, platform database |
| Evals with real models: scenarios with a score per model and prompt | deliberately not now (E-89) | |

#### Other assistants and agents

| Feature | Status | Depends on |
|---|---|---|
| Your own assistant over MCP: platform tools and task tools (open, reply, read the answer) at `mcp.<domain>/mcp`; Claude and other MCP clients sign in through the cell's login (OAuth with refresh tokens); the person's token reaches the API, which accepts it only on the routes for assistants, records person and assistant, and refuses approving; checked with a real connector that registers, signs in and refreshes its token | open | Tools from the API contract, conversations |
| Agent to agent (A2A): an agent card per agent of the cell; an adapter in the API maps a task of the cell to an A2A context and each run of the agent to an A2A task; a follow-up is a new A2A task in the same context, later results go out as push notifications; agents call outside agents an admin registered | open | Agents as accounts, conversations |
| Outside MCP servers for agents: an admin registers an MCP server outside the cell, its credential in a Secret; agents allowed to use it get its tools through the API; a tool an admin has not marked read-only is a proposal that needs approval and that the cell cannot undo | open | Tools per agent |

### Models

A core part of the platform, not an add-on (E-78): agents and apps reach models only through the cell's gateway, in one open API, so a provider can be swapped without changing an agent or an app.

| Feature | Status | Depends on |
|---|---|---|
| Model gateway: one open model API in the cell, any provider or own model behind it; no agent code bound to one provider's SDK | built (E-80); the agent speaks Chat Completions to it (E-94) | |
| Providers and keys in the console and the API (the customer's own keys), not only at install (`CELL_LLM_API_KEY`) | built (E-80) | Model gateway |
| Model per task: several providers in the gateway at once (today one entry for all models), which model an agent uses, fallback to another provider | open | Neutral model API, providers and keys |
| Usage and costs, budget and limits per agent, app or department | built: tokens per account and one hourly token limit for each (E-81); budgets in money, per department and durable usage open (O-31) | Model gateway, platform database |
| Run open models in the cell | open | Model gateway |
| Images and files in a task: attachments as parts of a message, shown in the thread and sent to models that read them | open | Conversations |
| Speech in and out: a spoken request becomes text, an answer can be read aloud; through the gateway's audio routes or a speech model in the cell | open | Images and files in a task |
| Live voice: speech to speech on its own realtime connection; the transcript lands in the task | open | Speech in and out |

### Cell

| Feature | Status | Depends on |
|---|---|---|
| Cell and domain | open | |
| System mail of the cell (send only: invitations, password links, warnings) | built (invitations, warnings), through a mail server of the admin's or the cell's own (E-84) | |

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
| Connect your own assistant | Tool gateway, tokens; later "Your own assistant over MCP" (platform and task tools, sign-in through the cell's login) |
| Appearance per customer | - |
| Mail, mailboxes, central launcher for all tools | - |

## Deliberately out

- Migration of existing IT landscapes
- Billing, central service across several cells
- Device management
