# Plattform

Die Plattform hinter dem Produkt: Sie hostet beliebige self-hostbare Software (Open Source und Eigenbau des Kunden), betreibt sie so weit wie möglich deterministisch und überlässt KI-Agenten nur das, was sich nicht in Regeln fassen lässt.

Das Repo ist namensneutral gehalten. Der Produktname steht an genau einer Stelle (`docs/entscheidungen.md`, E-05); technische Bezeichner nutzen das neutrale Präfix `cell`.

## Stand

Eine Zelle entsteht per Befehl, lokal (`make cell-up`) oder auf einem Ubuntu-Rechner (`deploy/install.sh`); weitere Rechner kommen über die Konsole hinzu, verbunden über WireGuard. Apps werden allein über ihr Manifest installiert, mit Datenbank, Single Sign-on und Zertifikat. Sie werden außerhalb der Zelle gesichert, lassen sich wiederherstellen (auch in eine neue Zelle), und Änderungen laufen mit Backup, Health-Check und automatischem Rollback. Alles das geht über die Plattform-API unter `https://api.<domain>`; jede Änderung wartet dort auf die Freigabe eines Admins. Der Ops-Agent diagnostiziert Apps, die Aufmerksamkeit brauchen, und paketiert Apps aus Compose-Dateien; er arbeitet nur über die API und schlägt Änderungen zur Freigabe vor. Das MCP-Gateway unter `https://mcp.<domain>/mcp` bündelt die Werkzeuge aller Apps mit MCP-Endpunkt und zeigt jedem Konto nur die Werkzeuge der Apps, die es nutzen darf. Details und Messwerte in `docs/stand.md`, der Vertrag der API in `api/openapi.yaml`.

```
make cell-up     # lokale Zelle (k3d), Login unter https://auth.cell.localhost
                 # mit CELL_LLM_API_KEY=<Anthropic-Schlüssel> arbeitet der Ops-Agent
make status      # Zustand der Basisdienste
make test        # Unit-, API- und Controller-Tests (envtest)
make e2e         # Ende-zu-Ende-Tests gegen die lokale Zelle, über die API
make demo        # Ziel-Demo Schritt für Schritt gegen die lokale Zelle, danach selbst ausprobieren
make dr-test     # Restore in eine frisch aufgebaute Zelle (löscht die lokale Zelle)
make vm-test     # Abnahme auf zwei frischen Ubuntu-VMs (Lima, lokal), die zweite tritt über die API bei
make cell-down   # lokale Zelle löschen
```

Auf einem eigenen Server (als root, in einem Klon nur für Releases; Voraussetzungen und Schritte in `docs/installation.md`):

```
CELL_DOMAIN=cell.example.com CELL_ACME_EMAIL=ops@example.com deploy/release.sh v0.1.0
deploy/release.sh v0.2.0  # Update: Tag holen, Image auf dem Server bauen, installieren
```

Weitere Rechner: Konsole, „Machines", „Add machine", den Befehl auf dem neuen Rechner ausführen.

Die API spricht man mit einem Token der Identität an. Ein Admin (Gruppe `cell-admins`, anfangs `akadmin`) legt in authentik ein App-Passwort an, danach:

```
curl -d grant_type=client_credentials -d client_id=cell-api -d username=akadmin \
     -d password=<App-Passwort> -d 'scope=openid profile' https://auth.cell.localhost/application/o/token/
curl -H 'Authorization: Bearer <access_token>' https://api.cell.localhost/v0/apps
curl -H 'Authorization: Bearer <access_token>' -d '{"catalog":"forgejo"}' https://api.cell.localhost/v0/apps
curl -H 'Authorization: Bearer <access_token>' -X POST https://api.cell.localhost/v0/changes/<name>/approve
```

Für das MCP-Gateway holt man das Token genauso, nur mit `client_id=cell-mcp`, und trägt es im MCP-Client als Header ein, zum Beispiel:

```
claude mcp add --transport http cell https://mcp.cell.localhost/mcp --header 'Authorization: Bearer <access_token>'
```

## Lesereihenfolge

1. `docs/kontext.md` – wofür und für wen wir das bauen
2. `docs/architektur.md` – wie die Plattform aufgebaut ist
3. `docs/stand.md` – was heute läuft, mit Nachweisen
4. `docs/installation.md` – Voraussetzungen und Installation auf eigenen Servern
5. `docs/backlog.md` – alle Features mit Status
6. `docs/app-manifest.md` – Entwurf der zentralen Abstraktion
7. `docs/entscheidungen.md` – was entschieden ist, was Annahme ist, was offen ist

Für Coding Agents zusätzlich: `CLAUDE.md`.

## Struktur

```
cmd/         Einstiegspunkte: operator, api, agent, cellctl, bridge
api/         CRD-Typen (Go) und der Vertrag der API (openapi.yaml)
internal/    Controller, API-Server, Ops-Agent, Clients für Plattformdienste
console/     Konsole der Zelle (Svelte), ausgeliefert von der API
catalog/     App-Manifeste, ein Ordner pro App
deploy/      Zellen-Bootstrap: k3s und Basisdienste
test/e2e/    Ende-zu-Ende-Tests gegen eine lokale Zelle
docs/        Diese Dokumente
```

## Abgrenzung

Office-Suite und die zentrale UI liegen in anderen Repos. Dieses Repo liefert die Plattform, ihre Konsole (E-38) und zwei Schnittstellen nach außen: die Plattform-API und das MCP-Gateway.

## Lizenz

AGPL-3.0-only ([LICENSE](LICENSE)). Was das für Unternehmen bedeutet, die kommerzielle Lizenz und der Weg für Beiträge: [LICENSING.md](LICENSING.md), [CLA.md](CLA.md), [CONTRIBUTING.md](CONTRIBUTING.md).
