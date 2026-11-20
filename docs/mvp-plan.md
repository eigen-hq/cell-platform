# MVP-Plan

Erste Ausbaustufe der Plattform. Jeder Meilenstein endet mit etwas Vorführbarem.

## Stand

- Alle Meilensteine abgeschlossen: M0 bis M4 (19.11.2026), M5 (20.11.2026)
- Als Nächstes: `make demo` (Ziel-Demo) und die offenen Entscheidungen in `entscheidungen.md`

## Ziel-Demo

Am Ende des MVP läuft diese Vorführung ohne Handarbeit durch:

1. Frische VM, ein Befehl, wenige Minuten später steht die Zelle.
2. Drei Apps werden installiert und sind sofort per Single Sign-on nutzbar.
3. Eine App wird absichtlich kaputt gemacht. Der Ops-Agent findet die Ursache, schlägt die Reparatur vor und stellt sie nach Freigabe wieder her.
4. Ein Update schlägt fehl und wird automatisch zurückgerollt, ohne Datenverlust.
5. Eine bisher unbekannte App wird vom Agenten paketiert und installiert.
6. Ein MCP-Client sieht die Tools der installierten Apps über einen einzigen Endpunkt.

## Meilensteine

### M0 – Zelle per Befehl

Aus einer frischen VM wird eine lauffähige Zelle.

Umfang:
- Installationsskript für eine VM (Ubuntu LTS) und `make cell-up` für eine lokale Zelle
- k3s, Ingress mit TLS, Identität, Postgres-Operator, Volumes mit Snapshots
- Backup-Ziel (S3-Bucket) konfigurierbar
- `cellctl status` zeigt den Zustand der Basisdienste

Akzeptanz:
- Frische VM bis erreichbare Login-Seite in unter 10 Minuten
- Zweimaliges Ausführen ändert nichts (idempotent)
- Ressourcenbedarf der leeren Zelle ist gemessen und dokumentiert

Zu klären in M0: Volume-Baustein (A-06) und Zielgröße der VM (A-12).

Ergebnis (19.11.2026):

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Frische VM bis Login-Seite | `make vm-test`: Ubuntu 26.04.1 LTS in Lima, 4 vCPU, 8 GiB | 522 s gesamt (149 s VM-Start inkl. Image-Download, 373 s Installation) |
| Idempotent | zweiter Lauf von `install.sh` bzw. `make cell-up`; Generationen und Ressourcen-Versionen unverändert | bestanden (`make vm-test`, `make e2e`) |
| Snapshots | PVC auf Longhorn, `VolumeSnapshot` wird `readyToUse` | bestanden (`make vm-test`) |
| Lokale Zelle | `make cell-up` aus dem Nichts, Images im Cache | 234 s |
| Ressourcen leere Zelle (VM) | `free -m`, `kubectl top` nach 60 s Ruhe | 3,4 GB belegt, 4,5 GB verfügbar, 0,36 CPU im Leerlauf; authentik allein 965 MiB, Longhorn rund 450 MiB |
| Ressourcen leere Zelle (lokal) | `kubectl top` | Pods 1,05 GiB, Knoten 2,6 GiB (ohne Longhorn) |

### M1 – App-Manifest und Operator

Eine App wird allein durch ihr Manifest installiert.

Umfang:
- Ressource `App` und Operator, der Namespace, Datenbank, Login-Client, App, Subdomain und Zertifikat anlegt
- Drei Referenz-Apps im Katalog (A-09)
- Aufräumen beim Entfernen; Daten bleiben als letzte Sicherung erhalten

Akzeptanz:
- `App` anlegen, danach läuft die App unter eigener Subdomain, Login per Single Sign-on ohne manuellen Schritt
- Pod löschen: App kommt von selbst zurück
- Alle drei Referenz-Apps bestehen denselben Ende-zu-Ende-Test

Zu klären in M1: Helm-Anbindung (A-07) und das Manifest-Format gegen die drei Apps schärfen.

Ergebnis (19.11.2026): Helm über Flux (E-18), Manifest geschärft (`app-manifest.md`).

| Prüfung | Wie | Ergebnis |
|---|---|---|
| App aus Manifest, Subdomain, SSO ohne Handgriff | `make e2e`, `TestReferenceApps`: Login als Testnutzer über den authentik-Flow, danach fragt der Test die App, wer angemeldet ist | bestanden für Forgejo, Nextcloud, Eigenbau-Platzhalter |
| Pod löschen | derselbe Test löscht alle App-Pods und wartet, bis die Sitzung wieder antwortet | bestanden |
| Entfernen behält Daten | derselbe Test löscht die App, prüft Namespace und Volumes, installiert neu und meldet sich wieder an | bestanden |
| Operator-Logik | `make test`: envtest mit echtem API-Server (Datenbank zuerst, Platzhalter, Login, Proxy, Helm, `NeedsAttention`, Entfernen) | bestanden |
| Laufzeiten lokal | erste Installation inklusive Image-Download | Eigenbau-Platzhalter 10 s, Forgejo rund 2 min, Nextcloud rund 2 min |

### M2 – Tag-2-Betrieb

Sichern, Wiederherstellen und Updaten laufen deterministisch.

Umfang:
- Geplante Backups von Datenbank und Volumes außerhalb der Zelle
- Restore einer App
- Update-Ablauf: Snapshot, Upgrade, Health-Check, bei Fehler Rollback
- Ressource `ChangeRequest` als Änderungsprotokoll

Akzeptanz:
- Restore-Test: App in einer frischen Zelle aus dem Backup wiederhergestellt, Daten vollständig
- Fehlerhaftes Update wird automatisch zurückgerollt; Daten und Version sind wie vorher
- Jede Änderung hat einen Protokolleintrag mit Urheber, Diff und Ergebnis

Ergebnis (19.11.2026): Backups über barman-cloud und restic (E-23 bis E-28), lokales Backup-Ziel neben der Zelle.

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Restore in frischer Zelle | `make dr-test`: Markierung in Datenbank und Volume von Forgejo, Backup, Zelle löschen und neu aufbauen, Forgejo installieren, Restore aus dem Bucket, danach Login mit der neuen Identität | Daten vollständig, Login funktioniert; rund 12 min inklusive Neuaufbau der Zelle |
| Restore in derselben Zelle | `make e2e`, `TestBackupAndRestore`: Markierung, Backup, Markierung überschreiben, Restore | Forgejo und Nextcloud wieder auf dem Stand des Backups, je rund 100 s |
| Fehlerhaftes Update | `make e2e`, `TestFailedUpdateRollsBack`: `ChangeRequest` auf eine Version, die es nicht gibt | nach 90 s Health-Frist zurück auf 15.0.11, Daten wie vorher, App `NeedsAttention`, `ChangeRequest` `RolledBack` |
| Änderungsprotokoll | jeder `ChangeRequest` hält Urheber, Begründung, Risiko, Patch, vorige Spec, Backup, Restore, Ergebnis und Zeiten | geprüft in `make test` (envtest) und `make e2e` |
| Steuerlogik | `make test`: geplantes Backup, Freigabe, erfolgreiche Änderung, Rollback mit Restore, Ablehnung | bestanden |

### M3 – Plattform-API v0

Alles aus M1 und M2 geht ohne `kubectl`.

Umfang:
- REST-API für Katalog, Apps, Backups, Zugriff, Nutzer und Gruppen, Änderungsprotokoll, Ereignis-Stream
- Anmeldung über Tokens der Identität
- OpenAPI-Beschreibung im Repo als Vertrag mit der Oberfläche
- Freigaben nur über die API: Wer eine Änderung anfragt, kann `approval` nicht setzen; entscheiden darf nur eine berechtigte Rolle über einen eigenen Aufruf. Bis dahin schreiben nur Cluster-Admins `ChangeRequest`s

Akzeptanz:
- Der Ende-zu-Ende-Test aus M1 und M2 läuft ausschließlich über die API
- Die OpenAPI-Datei ist aktuell und wird im Test geprüft

Ergebnis (19.11.2026): Plattform-API `cell-api` (E-29), Freigaben nur über die API (E-30), Installieren, Entfernen und Wiederherstellen als Aktionen eines `ChangeRequest` (E-31).

| Prüfung | Wie | Ergebnis |
|---|---|---|
| M1 und M2 nur über die API | `make e2e`: Installieren, Login, Entfernen und Neuinstallieren der drei Referenz-Apps, Backup und Restore, fehlerhaftes Update mit Rollback, Restore in eine frische Zelle (`make dr-test`), alles über `https://api.<domain>` mit Tokens der Identität und Freigabe durch einen Admin. `kubectl` spielt nur die Welt um die Zelle (Pods löschen, Testdaten schreiben) und schaut hinein, wo die API nichts zeigt (behaltener Namespace) | bestanden |
| Freigaben | `make e2e`, `TestChangeApproval`: Agent schlägt vor, darf nicht freigeben; ein Cluster-Admin kann weder einen freigegebenen `ChangeRequest` anlegen noch an der API vorbei freigeben; der Admin lehnt über die API ab; der Ereignis-Stream meldet die Änderung | bestanden |
| Zugriff | `make e2e`, `TestAccessGroups`: App auf eine Gruppe beschränkt, Mitglied kommt hinein, andere weist die Identität ab, nach dem Öffnen kommen alle hinein | bestanden |
| OpenAPI-Datei aktuell | `make test`: jede Route steht in `api/openapi.yaml` und umgekehrt, jede Antwort der API-Tests passt zu ihrem Schema (keine zusätzlichen Felder erlaubt) | bestanden |
| Steuerlogik | `make test`: Installieren, Entfernen mit und ohne Daten, Wiederherstellen als Änderung, Zugriffsgruppen, Validierung der Aktionen; Tokens (Zielgruppe, Ablehnung durch die Identität, Cache) | bestanden |

### M4 – Ops-Agent v0

Der Agent löst zwei Aufgaben, die keine Regel abdeckt.

Umfang:
- Agenten-Dienst mit eigenem Konto; Werkzeuge sind nur API-Aufrufe
- Szenario A: ungesunde App diagnostizieren und Reparatur als `ChangeRequest` vorschlagen
- Szenario B: unbekannte App aus Helm-Chart oder Compose-Datei zu einem Manifest paketieren
- Freigabe-Schritt für jede schreibende Aktion (A-10)

Akzeptanz:
- Fehler-Injektion (zum Beispiel falscher Konfigurationswert): Agent benennt die Ursache, nach Freigabe ist die App wieder gesund
- Für eine App außerhalb des Katalogs entsteht ein Manifest, das der Operator ohne Nacharbeit installiert
- Jeder Agenten-Schritt steht im Änderungsprotokoll

Zu klären vor M4: Agenten-Laufzeit (O-01).

Ergebnis (19.11.2026): Agenten-Dienst `cell-agent` mit eigenem Konto (E-32), Modell und Schlüssel des Kunden (E-33), Aufgaben als Ressource `Task` mit Schritten (E-34), Installieren aus einem Manifest mit Prüfung durch einen Probelauf (E-35). Paketiert werden Compose-Dateien; Helm-Charts sind O-15.

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Szenario A | `make e2e`, `TestAgentRepairsApp`: falscher Wert (`WHOAMI_PORT_NUMBER=8080`) an der Plattform vorbei gesetzt, der Operator legt eine Diagnose-Aufgabe an, der Agent liest App, Pods und Logs über die API, findet die Ursache im Log und schlägt die Reparatur vor; nach Freigabe ist die App gesund | bestanden |
| Szenario B | `make e2e`, `TestAgentPackagesApp`: Memos aus seiner Compose-Datei; der erste Vorschlag mit einer Subdomain der Zelle wird vom Probelauf abgewiesen, der korrigierte nach Freigabe ohne Nacharbeit installiert | bestanden |
| Jeder Schritt im Protokoll | `make e2e` und `make test`: jeder Aufruf des Agenten steht als Schritt in seiner Aufgabe, auch bei parallelen Aufrufen; ohne Aufgabe oder in fremden Aufgaben weist die API ab; Vorschläge tragen ihre Aufgabe | bestanden |
| API legt keine Apps an | `make e2e`, `TestAPICannotCreateApps`: das Konto der API darf Apps nur im Probelauf anlegen (ValidatingAdmissionPolicy) | bestanden |
| Agent | `make test`: Diagnose und Paketierung mit einem geskripteten Modell (Fake der Messages-API), verlorene Aufgaben (Neustart, verlorene Antwort beim Übernehmen oder Beenden), Modell nicht erreichbar; eine Diagnose je Ursache, auch nach gescheiterter Wiederherstellung | bestanden |

Das Modell ist in den Tests geskriptet: geprüft ist alles um das Modell herum (Werkzeuge, Rechte, Protokoll, Probelauf, Installation), nicht die Qualität der Diagnose. Ein Lauf mit echtem Modell braucht einen Schlüssel (O-16).

### M5 – MCP-Gateway v0

Ein Endpunkt für die Tools aller Apps.

Umfang:
- Feld `mcp` im Manifest
- Gateway, das die Endpunkte bündelt und Aufrufe gegen die Rechte des Kontos prüft

Akzeptanz:
- Ein MCP-Client sieht über einen Endpunkt die Tools von mindestens zwei installierten Apps
- Ein Konto ohne Zugriff auf eine App sieht deren Tools nicht

Ergebnis (20.11.2026): agentgateway als MCP-Gateway, konfiguriert vom Operator aus den Manifesten (E-36), Feld `mcp` im Manifest, Memos im Katalog als erste App mit eigenem MCP-Endpunkt. Aufrufe von Memos-Werkzeugen beantwortet Memos mit seinem Anmeldefehler, weil das Gateway kein Memos-Token hat (O-18); Apps ohne eigenen Endpunkt sind O-19, die Grenzen von v0 O-20.

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Werkzeuge zweier Apps über einen Endpunkt | `make e2e`, `TestMCPGateway`: zwei Memos aus dem Katalog (`notes` für alle, `journal` nur für eine Gruppe); ein Konto der Gruppe sieht über `https://mcp.<domain>/mcp` die Werkzeuge beider, mit dem App-Namen vorn; sein Aufruf erreicht die App | bestanden |
| Ohne Zugriff keine Werkzeuge | dieselbe Prüfung: ein Konto außerhalb der Gruppe sieht nur `notes_*`, sein Aufruf eines `journal_*`-Werkzeugs wird abgewiesen; ohne Token oder mit einem Token der API antwortet das Gateway 401, ein Token des Gateways gilt nicht an der API | bestanden |
| Zugriffsregeln | `make test`, `TestGatewayConfig`: die Regeln mit cel-go ausgewertet, auch mit Gruppennamen, die selbst CEL enthalten; Apps ohne `mcp`, noch nie gesunde und gelöschte Apps fehlen | bestanden |
| Speicher | Gateway im Leerlauf, lokale Zelle | 11 MiB |

## Bewusst nicht im MVP

- Zentraler Dienst über mehrere Zellen, Kundenverwaltung, Abrechnung
- Mehrere Knoten pro Zelle, Hochverfügbarkeit
- VPN oder Mesh-Netz
- Kontext-Index über alle Apps
- Build-Pipeline für Eigenbau-Apps (im MVP: fertige Container)
- Migration bestehender IT-Landschaften
- Betrieb offener Sprachmodelle in der Zelle
- Jede Art von Oberfläche

## Risiken

| Risiko | Umgang |
|---|---|
| Jede App braucht eigene Handgriffe für den Login | Drei bewusst unterschiedliche Referenz-Apps in M1; Muster ins Manifest ziehen |
| Snapshots auf einem einzelnen Knoten | Volume-Baustein in M0 auf echter VM prüfen, nicht nur lokal |
| Basisdienste brauchen zu viel Speicher für eine kleine VM | In M0 messen; notfalls Bausteine tauschen |
| „Läuft" ist nicht „integriert" | Zwei Stufen im Katalog sichtbar machen: generisch und integriert |
| Vertrauen in Agenten | Freigabe als Standard, Protokoll von Anfang an |

## Lokale Entwicklung

Für den schnellen Zyklus eine lokale Zelle (k3s in Docker). Sie hat keinen Snapshot-fähigen Speicher; alles rund um Snapshots und Restore wird auf einer echten VM abgenommen.
