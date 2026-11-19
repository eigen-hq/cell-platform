# CLAUDE.md

Anweisungen für Coding Agents in diesem Repo.

## Worum es geht

Eine Plattform, die beliebige self-hostbare Software pro Kunde in einer eigenen „Zelle" (ein k3s-Cluster) betreibt. Ein eigener Operator erledigt alles Deterministische (installieren, neu starten, sichern, updaten, zurückrollen). KI-Agenten übernehmen nur, was sich nicht in Regeln fassen lässt, und handeln dabei ausschließlich über die Plattform-API.

## Zuerst lesen

1. `docs/mvp-plan.md` – aktueller Meilenstein steht unter „Stand"
2. `docs/architektur.md`
3. `docs/app-manifest.md`
4. `docs/entscheidungen.md`
5. `docs/kontext.md` – bei Produktfragen

## So arbeiten wir

- Ein Meilenstein nach dem anderen. Nichts aus späteren Meilensteinen vorziehen.
- Vor dem ersten Code eines Meilensteins: kurzer Umsetzungsplan (Dateien, Reihenfolge, Risiken), dann bauen.
- Kleinster lauffähiger Schnitt zuerst. Jede Akzeptanzbedingung wird ein automatischer Test oder ein Schritt in `make demo`.
- Nach Abschluss eines Meilensteins: „Stand" in `docs/mvp-plan.md` aktualisieren.
- Neue Architekturentscheidung oder neue Abhängigkeit: Eintrag in `docs/entscheidungen.md`.
- Annahmen (A-xx) dürfen genutzt werden. Erweist sich eine als falsch oder teuer: stoppen, Befund und Alternative nennen, nicht still umbauen.
- Offene Punkte (O-xx) nicht selbst entscheiden, sondern nachfragen, sobald sie den aktuellen Meilenstein blockieren.

## Leitplanken der Architektur

1. Deterministisch zuerst. Was eine Regel lösen kann, bekommt kein LLM.
2. Das Manifest ist die Wahrheit. Der gewünschte Zustand steht deklarativ in Kubernetes-Ressourcen; niemand ändert laufende Systeme von Hand.
3. Agenten handeln nur über die Plattform-API. Kein direkter Cluster- oder Shell-Zugriff am Operator vorbei.
4. Jede Änderung läuft gleich ab: Snapshot, Änderung, Health-Check, bei Fehler Rollback, Eintrag ins Änderungsprotokoll.
5. Eine Zelle pro Kunde. Kein Mandanten-Code innerhalb eines Clusters.
6. Bausteine komponieren statt neu bauen. Eigener Code nur für Operator, API und Agenten.
7. Namensneutral. Kein Produktname in Bezeichnern, API-Gruppen, Namespaces, Image-Pfaden oder CLI-Namen; das technische Präfix ist `cell`.
8. Keine Geheimnisse im Repo. Zugangsdaten entstehen in der Zelle und liegen dort als Secrets.

## Konventionen

- Sprache: Doku Deutsch. Code, Bezeichner, Kommentare und Commits Englisch.
- Commits: Conventional Commits, klein und thematisch.
- Go: aktuelle stabile Version, `gofmt`, `go vet`, `golangci-lint`.
- Tests: Unit-Tests neben dem Code, Controller mit envtest, Ende-zu-Ende gegen eine lokale Zelle.
- Das `Makefile` ist die einzige Einstiegstür: `make cell-up`, `make cell-down`, `make test`, `make e2e`, `make demo`.
- Versionen nie aus dem Gedächtnis. Bei Einführung eines Bausteins die aktuelle stabile Version nachschlagen, pinnen und in `docs/entscheidungen.md` vermerken. Die Recherche-Notizen dort zeigen, dass sich mehrere Bausteine zuletzt stark verändert haben.

## Technische Namen

| Was | Name |
|---|---|
| API-Gruppe der eigenen Ressourcen | `cell.internal` (Platzhalter, an einer Stelle definiert) |
| System-Namespace | `cell-system` |
| Namespace je App | `app-<name>` |
| CLI | `cellctl` |

## Nicht tun

- Keine UI in diesem Repo.
- Kein zentraler Steuerungsdienst über mehrere Zellen, keine Abrechnung.
- Keine Hochverfügbarkeit, keine Mehr-Knoten-Cluster.
- Kein VPN oder Mesh-Netz im MVP.
- Keine Build-Pipeline für Eigenbau-Apps; im MVP kommen sie als fertige Container.
- Keine Migrationswerkzeuge für Bestandskunden.
- Kein Push außer auf das vereinbarte Remote (E-09).
