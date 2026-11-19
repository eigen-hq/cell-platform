# Plattform

Die Plattform hinter dem Produkt: Sie hostet beliebige self-hostbare Software (Open Source und Eigenbau des Kunden), betreibt sie so weit wie möglich deterministisch und überlässt KI-Agenten nur das, was sich nicht in Regeln fassen lässt.

Das Repo ist namensneutral gehalten. Der Produktname steht an genau einer Stelle (`docs/entscheidungen.md`, E-05); technische Bezeichner nutzen das neutrale Präfix `cell`.

## Stand

M0 und M1 sind abgeschlossen: Eine Zelle entsteht per Befehl, lokal (`make cell-up`) oder auf einer Ubuntu-VM (`deploy/install.sh`), und Apps werden allein über ihr Manifest installiert, mit Datenbank, Single Sign-on und Zertifikat. Details und Messwerte in `docs/mvp-plan.md`.

```
make cell-up     # lokale Zelle (k3d), Login unter https://auth.cell.localhost
make status      # Zustand der Basisdienste
kubectl apply -f catalog/forgejo/manifest.yaml   # App installieren
make test        # Unit- und Controller-Tests (envtest)
make e2e         # Ende-zu-Ende-Tests gegen die lokale Zelle
make vm-test     # Abnahme auf einer frischen Ubuntu-VM (Lima, lokal)
make cell-down   # lokale Zelle löschen
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
