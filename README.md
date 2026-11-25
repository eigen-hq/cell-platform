# Platform

The platform behind the product: it hosts any self-hostable software (open source and the customer's own builds), runs it as deterministically as possible, and leaves only what cannot be captured in rules to AI agents.

The repo is kept name-neutral. The product name appears in exactly one place (`docs/decisions.md`, E-05); technical identifiers use the neutral prefix `cell`.

## Status

A cell is created by a single command, locally (`make cell-up`) or on an Ubuntu machine (`deploy/install.sh`); more machines are added through the console, connected over WireGuard. Apps are installed from their manifest alone, with database, single sign-on and certificate. They are backed up outside the cell and can be restored (also into a new cell), and changes run with backup, health check and automatic rollback. All of this goes through the platform API at `https://api.<domain>`; every change waits there for an admin's approval. The ops agent diagnoses apps that need attention and packages apps from Compose files; it works only through the API and proposes changes for approval. The MCP gateway at `https://mcp.<domain>/mcp` bundles the tools of all apps that have an MCP endpoint and shows each account only the tools of the apps it may use. Details and measurements are in `docs/status.md`, the API contract in `api/openapi.yaml`.

```
make cell-up     # local cell (k3d), login at https://auth.cell.localhost
                 # with CELL_LLM_API_KEY=<Anthropic key> the ops agent works
make status      # state of the base services
make test        # unit, API and controller tests (envtest)
make e2e         # end-to-end tests against the local cell, through the API
make demo        # target demo step by step against the local cell, then try it yourself
make dr-test     # restore into a freshly built cell (deletes the local cell)
make vm-test     # acceptance on two fresh Ubuntu VMs (Lima, local), the second joins through the API
make cell-down   # delete the local cell
```

On your own server, for installation and update (requirements and steps in `docs/installation.md`):

```
curl -fsSL https://raw.githubusercontent.com/eigen-hq/cell-platform/main/get.sh | sudo bash
```

This fetches the latest release to `/opt/cell`, builds the image on the server and installs; `CELL_VERSION=v0.2.0` before `bash` selects a specific one. From a clone: `sudo deploy/install.sh`.

More machines: console, "Machines", "Add machine", then run the command on the new machine.

You call the API with a token from the identity service. An admin (group `cell-admins`, initially `akadmin`) creates an app password in authentik, then:

```
curl -d grant_type=client_credentials -d client_id=cell-api -d username=akadmin \
     -d password=<app password> -d 'scope=openid profile' https://auth.cell.localhost/application/o/token/
curl -H 'Authorization: Bearer <access_token>' https://api.cell.localhost/v0/apps
curl -H 'Authorization: Bearer <access_token>' -d '{"catalog":"forgejo"}' https://api.cell.localhost/v0/apps
curl -H 'Authorization: Bearer <access_token>' -X POST https://api.cell.localhost/v0/changes/<name>/approve
```

For the MCP gateway you get the token the same way, only with `client_id=cell-mcp`, and enter it in the MCP client as a header, for example:

```
claude mcp add --transport http cell https://mcp.cell.localhost/mcp --header 'Authorization: Bearer <access_token>'
```

## Reading order

1. `docs/context.md` - what we build this for and for whom
2. `docs/architecture.md` - how the platform is built
3. `docs/status.md` - what runs today, with evidence
4. `docs/installation.md` - requirements and installation on your own servers
5. `docs/backlog.md` - all features with status
6. `docs/app-manifest.md` - draft of the central abstraction
7. `docs/decisions.md` - what is decided, what is an assumption, what is open

For coding agents, additionally: `CLAUDE.md`.

## Structure

```
cmd/         Entry points: operator, api, agent, cellctl, bridge
api/         CRD types (Go) and the API contract (openapi.yaml)
internal/    Controllers, API server, ops agent, clients for platform services
console/     Console of the cell (Svelte), served by the API
catalog/     App manifests, one folder per app
deploy/      Cell bootstrap: k3s and base services
test/e2e/    End-to-end tests against a local cell
docs/        These documents
```

## Scope

The office suite and the central UI live in other repos. This repo delivers the platform, its console (E-38) and two interfaces to the outside: the platform API and the MCP gateway.

## License

AGPL-3.0-only ([LICENSE](LICENSE)). What this means for companies, the commercial license and the path for contributions: [LICENSING.md](LICENSING.md), [CLA.md](CLA.md), [CONTRIBUTING.md](CONTRIBUTING.md).
