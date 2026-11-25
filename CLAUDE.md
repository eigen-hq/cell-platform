# CLAUDE.md

Instructions for coding agents in this repo.

## What this is

A platform that runs any self-hostable software per customer in a "cell" of its own (a k3s cluster). Its own operator does everything deterministic (install, restart, back up, update, roll back). AI agents take over only what rules cannot cover, and they act only through the platform API.

## Read first

1. `docs/status.md`: what runs today and how it is verified
2. `docs/architecture.md`
3. `docs/app-manifest.md`
4. `docs/decisions.md`

## How we work

- Before the first code: a short implementation plan (files, order, risks), then build.
- Features are in `docs/backlog.md`, with the line between the platform (technical operation) and the office UI (the work). A platform feature includes its logic and its screen in the console; both are built together. The console shows only what the API delivers.
- Smallest working slice first. Every acceptance condition becomes an automated test or a step in `make demo`.
- When something new runs: update `docs/status.md` with the evidence.
- A new architecture decision or a new dependency: an entry in `docs/decisions.md`.
- Assumptions (A-xx) may be used. If one turns out wrong or expensive: stop, state the finding and the alternative, do not rebuild silently.
- Do not decide open points (O-xx) yourself; ask as soon as they block the current work.

## Architecture guardrails

1. Deterministic first. What a rule can solve gets no LLM.
2. The manifest is the truth. The desired state is declared in Kubernetes resources; nobody changes running systems by hand.
3. Agents act only through the platform API. No direct cluster or shell access past the operator.
4. Every change runs the same way: snapshot, change, health check, rollback on failure, entry in the change log.
5. One cell per customer. No multi-tenant code inside a cluster.
6. Compose building blocks instead of building new ones. Own code only for the operator, API, agents and console.
7. Name-neutral. No product name in identifiers, API groups, namespaces, image paths or CLI names; the technical prefix is `cell`.
8. No secrets in the repo. Credentials are created in the cell and live there as Secrets.

## Conventions

- Language: English everywhere, for a global audience: docs, code, identifiers, comments, commits and UI text.
- Commits: Conventional Commits, small and on one topic.
- Go: current stable version, `gofmt`, `go vet`, `golangci-lint`.
- Tests: unit tests next to the code, controllers with envtest, end to end against a local cell.
- The `Makefile` is the only entry point: `make cell-up`, `make cell-down`, `make test`, `make e2e`, `make demo`.
- Never take versions from memory. When adding a building block, look up its current stable version, pin it and note it in `docs/decisions.md`. The research notes there show that several building blocks changed a lot recently.

## Technical names

| What | Name |
|---|---|
| API group of our own resources | `cell.internal` (placeholder, defined in one place) |
| System namespace | `cell-system` |
| Namespace per app | `app-<name>` |
| CLI | `cellctl` |

## Do not

- No central control service across cells, no billing.
- No migration tools for existing customers.
- No push except to the agreed remote (E-09).
