# Plattform

Die Plattform hinter dem Produkt: Sie hostet beliebige self-hostbare Software (Open Source und Eigenbau des Kunden), betreibt sie so weit wie möglich deterministisch und überlässt KI-Agenten nur das, was sich nicht in Regeln fassen lässt.

Das Repo ist namensneutral gehalten. Der Produktname steht an genau einer Stelle (`docs/entscheidungen.md`, E-05); technische Bezeichner nutzen das neutrale Präfix `cell`.

## Stand

Planungsstand, noch kein Code. Nächster Schritt: Meilenstein M0 in `docs/mvp-plan.md`.

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
