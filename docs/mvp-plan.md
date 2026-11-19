# MVP-Plan

Erste Ausbaustufe der Plattform. Jeder Meilenstein endet mit etwas Vorführbarem.

## Stand

- Aktueller Meilenstein: **M2**
- Abgeschlossen: M0, M1 (19.11.2026)

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

### M3 – Plattform-API v0

Alles aus M1 und M2 geht ohne `kubectl`.

Umfang:
- REST-API für Katalog, Apps, Backups, Zugriff, Nutzer und Gruppen, Änderungsprotokoll, Ereignis-Stream
- Anmeldung über Tokens der Identität
- OpenAPI-Beschreibung im Repo als Vertrag mit der Oberfläche

Akzeptanz:
- Der Ende-zu-Ende-Test aus M1 und M2 läuft ausschließlich über die API
- Die OpenAPI-Datei ist aktuell und wird im Test geprüft

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

### M5 – MCP-Gateway v0

Ein Endpunkt für die Tools aller Apps.

Umfang:
- Feld `mcp` im Manifest
- Gateway, das die Endpunkte bündelt und Aufrufe gegen die Rechte des Kontos prüft

Akzeptanz:
- Ein MCP-Client sieht über einen Endpunkt die Tools von mindestens zwei installierten Apps
- Ein Konto ohne Zugriff auf eine App sieht deren Tools nicht

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
