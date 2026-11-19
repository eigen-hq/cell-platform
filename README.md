# Plattform

Die Plattform hinter dem Produkt: Sie hostet beliebige self-hostbare Software (Open Source und Eigenbau des Kunden), betreibt sie so weit wie möglich deterministisch und überlässt KI-Agenten nur das, was sich nicht in Regeln fassen lässt.

Das Repo ist namensneutral gehalten. Der Produktname steht an genau einer Stelle (`docs/entscheidungen.md`, E-05); technische Bezeichner nutzen das neutrale Präfix `cell`.

## Stand

M0 bis M3 sind abgeschlossen: Eine Zelle entsteht per Befehl, lokal (`make cell-up`) oder auf einer Ubuntu-VM (`deploy/install.sh`). Apps werden allein über ihr Manifest installiert, mit Datenbank, Single Sign-on und Zertifikat. Sie werden außerhalb der Zelle gesichert, lassen sich wiederherstellen (auch in eine neue Zelle), und Änderungen laufen mit Backup, Health-Check und automatischem Rollback. Alles das geht über die Plattform-API unter `https://api.<domain>`; jede Änderung wartet dort auf die Freigabe eines Admins. Details und Messwerte in `docs/mvp-plan.md`, der Vertrag der API in `api/openapi.yaml`.

```
make cell-up     # lokale Zelle (k3d), Login unter https://auth.cell.localhost
make status      # Zustand der Basisdienste
make test        # Unit-, API- und Controller-Tests (envtest)
make e2e         # Ende-zu-Ende-Tests gegen die lokale Zelle, über die API
make dr-test     # Restore in eine frisch aufgebaute Zelle (löscht die lokale Zelle)
make vm-test     # Abnahme auf einer frischen Ubuntu-VM (Lima, lokal)
make cell-down   # lokale Zelle löschen
```

Die API spricht man mit einem Token der Identität an. Ein Admin (Gruppe `cell-admins`, anfangs `akadmin`) legt in authentik ein App-Passwort an, danach:

```
curl -d grant_type=client_credentials -d client_id=cell-api -d username=akadmin \
     -d password=<App-Passwort> -d 'scope=openid profile' https://auth.cell.localhost/application/o/token/
curl -H 'Authorization: Bearer <access_token>' https://api.cell.localhost/v0/apps
curl -H 'Authorization: Bearer <access_token>' -d '{"catalog":"forgejo"}' https://api.cell.localhost/v0/apps
curl -H 'Authorization: Bearer <access_token>' -X POST https://api.cell.localhost/v0/changes/<name>/approve
```

## Lesereihenfolge

1. `docs/kontext.md` – wofür und für wen wir das bauen
2. `docs/architektur.md` – wie die Plattform aufgebaut ist
3. `docs/mvp-plan.md` – Meilensteine mit Akzeptanzkriterien
4. `docs/app-manifest.md` – Entwurf der zentralen Abstraktion
5. `docs/entscheidungen.md` – was entschieden ist, was Annahme ist, was offen ist

Für Coding Agents zusätzlich: `CLAUDE.md`.

## Geplante Struktur

Noch nicht angelegt; entsteht mit den Meilensteinen.

```
cmd/         Einstiegspunkte: operator, api, cellctl
api/         CRD-Typen (Go)
internal/    Controller, API-Server, Clients für Plattformdienste
catalog/     App-Manifeste, ein Ordner pro App
deploy/      Zellen-Bootstrap: k3s und Basisdienste
agent/       Ops-Agent als eigener Dienst
test/e2e/    Ende-zu-Ende-Tests gegen eine lokale Zelle
docs/        Diese Dokumente
```

## Abgrenzung

Oberfläche, Office-Suite und die zentrale UI liegen in anderen Repos. Dieses Repo liefert die Plattform und zwei Schnittstellen dorthin: die Plattform-API und das MCP-Gateway.
