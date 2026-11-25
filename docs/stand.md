# Stand

Was die Plattform heute kann und wie es geprüft ist. Jede Fähigkeit hat einen automatischen Nachweis.

## Überblick

- Zuletzt aktualisiert: 24.11.2026
- `make demo` führt die Ziel-Demo gegen die lokale Zelle vor (Schritte 2 bis 6, je ein Ende-zu-Ende-Test mit Protokoll); Schritt 1 auf einer frischen VM ist `make vm-test`
- Offene Fragen stehen in `entscheidungen.md`

## Demo

Diese Vorführung läuft ohne Handarbeit durch:

1. Frische VM, ein Befehl, wenige Minuten später steht die Zelle.
2. Drei Apps werden installiert und sind sofort per Single Sign-on nutzbar.
3. Eine App wird absichtlich kaputt gemacht. Der Ops-Agent findet die Ursache, schlägt die Reparatur vor und stellt sie nach Freigabe wieder her.
4. Ein Update schlägt fehl und wird automatisch zurückgerollt, ohne Datenverlust.
5. Eine bisher unbekannte App wird vom Agenten paketiert und installiert.
6. Ein MCP-Client sieht die Tools der installierten Apps über einen einzigen Endpunkt.

## Was läuft

### Zelle per Befehl

Aus einer frischen VM wird eine lauffähige Zelle.

Stand 19.11.2026:

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Frische VM bis Login-Seite | `make vm-test`: Ubuntu 26.04.1 LTS in Lima, 4 vCPU, 8 GiB | 522 s gesamt (149 s VM-Start inkl. Image-Download, 373 s Installation) |
| Idempotent | zweiter Lauf von `install.sh` bzw. `make cell-up`; Generationen und Ressourcen-Versionen unverändert | bestanden (`make vm-test`, `make e2e`) |
| Snapshots | PVC auf Longhorn, `VolumeSnapshot` wird `readyToUse` | bestanden (`make vm-test`) |
| Lokale Zelle | `make cell-up` aus dem Nichts, Images im Cache | 234 s |
| Ressourcen leere Zelle (VM) | `free -m`, `kubectl top` nach 60 s Ruhe | 3,4 GB belegt, 4,5 GB verfügbar, 0,36 CPU im Leerlauf; authentik allein 965 MiB, Longhorn rund 450 MiB |
| Ressourcen leere Zelle (lokal) | `kubectl top` | Pods 1,05 GiB, Knoten 2,6 GiB (ohne Longhorn) |
| Automatische Adresse (E-55) | Unit-Tests `internal/names`: Registrieren nur mit Schlüssel, Name und Adresse geprüft, Wildcard gesetzt, Umzug und Nachweise nur mit dem eigenen Token, Unterdomain streng geprüft, der neueste Nachweis ersetzt den vorigen, Hetzner-Client gegen eine nachgestellte API; `TestACMEAccounts` (Konto je App-Host), `deploy/lib_test.sh` (Plattform-Konten) | bestanden (25.11.2026); echtes Zertifikat über Hetzner DNS und Let's Encrypt noch nicht gezeigt, braucht Zone und Token |
| Mesh (E-56) | Lokale Zelle, Mesh von Hand mit Adresse des Knotens: Headscale startet erst, wenn die Anmeldung bei authentik erreichbar ist; der Router meldet sich mit dem einmaligen Schlüssel an (Tag `tag:cell`), seine Route wird automatisch freigegeben; ein Test-Gerät tritt bei, findet das eigene Relay (`Nearest DERP: Cell`, UDP ja) und erreicht Traefik über die Route (80 und 443), 6443 und 10250 der Zelle bleiben über das Mesh gesperrt (direkt erreichbar); der Mesh-Abschnitt von `bootstrap.sh` lief gegen die lokale Zelle; `make vm-test`: Einrichtung samt Mesh auf frischer Ubuntu-VM bestanden (777 s), zweiter Lauf unverändert (der spätere Longhorn-Schritt lief in eine Zeitüberschreitung, die Server-VM mit 5 GiB war überlastet); `deploy/lib_test.sh` (Port-Prüfung, auch mit GNU-Werkzeugen unter Ubuntu 24.04) | bestanden (25.11.2026); Anmeldung eines Geräts über den Browser und Clients außerhalb der Zelle noch nicht gezeigt |
| Netz der Rechner getrennt von der Adresse der Zelle (E-57) | `bash -n`, `deploy/lib_test.sh`; Einrichtung mit `CELL_NODE_IP` (privat) und `CELL_INGRESS_IP` (öffentlich) | offen: wird auf den beiden VPS mit privatem Netz gezeigt |
| Installation mit wenig Handarbeit (E-58) | `bash -n`, `shellcheck`, `deploy/lib_test.sh` (private Adresse einer Netzwerkkarte erkannt, Brücken und Tunnel ignoriert) | offen: Abfrage, DNS-Prüfung und Passwort-Anzeige werden auf dem VPS gezeigt |
| Installation mit einem Befehl aus dem neuesten Release, Ausgabe als Schritte mit Protokoll (E-59) | `bash -n`, `shellcheck`, `deploy/lib_test.sh`; Darstellung im Pseudo-Terminal geprüft (Schritte, Hinweise, Fehler mit Befehl und Protokollende) | offen: `get.sh` gegen das veröffentlichte Release und die Installation werden auf dem VPS gezeigt |
| Backup-Ziel in der Konsole und über die API (E-60) | `go test` (API: Rollen, Eingaben, keine Schlüssel in der Antwort, 409; Operator: Job, Meldung, Status, Cache), `npm run check`; envtest-Suite im täglichen Lauf | offen: Probe gegen echtes S3 (Hetzner Object Storage) und die Ansicht in beiden Tönen werden auf dem VPS gezeigt |

### Zelle über mehrere Rechner

Weitere Rechner treten über die Plattform bei (E-42, E-44): Ein Admin holt in der Konsole oder über `POST /v0/nodes/joins` einen Befehl und führt ihn auf dem neuen Rechner aus. Der Rechner lädt alles von der Zelle, verbindet sich über WireGuard und bekommt das Plattform-Image vom ersten Rechner, ohne Registry. Installation und Updates auf einen Release-Tag: `deploy/release.sh <tag>` auf dem Server (Build dort, aus Git); noch nicht gelaufen, der erste Lauf ist die Installation auf dem VPS.

Stand 24.11.2026:

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Zweiter Rechner tritt bei | `make vm-test`: zwei frische VMs (Ubuntu 24.04.5 LTS in Lima, 4 vCPU/5 GiB und 2 vCPU/3 GiB, gemeinsames Netz `user-v2`); Admin-Token wie in `test/e2e`, `POST /v0/nodes/joins`, der Befehl läuft unverändert auf der zweiten VM. Seitdem steht der Schlüssel nicht mehr in der URL, sondern im Befehl (`CELL_JOIN_KEY`); diese Form prüft erst der nächste tägliche Lauf | beigetreten in 21 s; `GET /v0/nodes` zeigt Server und Agent, beide bereit, mit den Adressen, die `node.sh` selbst gefunden hat |
| Verschlüsselt verbunden | `flannel-wg` auf beiden Rechnern; ein Pod auf dem zweiten Rechner erreicht `cell-api` auf dem ersten | bestanden |
| Image ohne Registry | Pod mit dem Plattform-Image auf dem zweiten Rechner; das Image liegt nur auf dem ersten (Spiegel von k3s) | bestanden; mit `ctr import` geladene Images teilt der Spiegel nicht, daher der Bilder-Ordner von k3s |
| Speicher auf dem zweiten Rechner | Longhorn-Volume und Snapshot auf dem zweiten Rechner | bestanden |
| Server-Installation | derselbe Lauf, erster Rechner | 47 s VM-Start, 438 s bis zur Login-Seite; zweiter Lauf ändert nichts |
| Ressourcen | `kubectl top` nach 60 s Ruhe | Server 3,6 GiB, Agent 0,8 GiB |
| API und Schlüssel | `make test`, `TestNodes`, `TestJoin` gegen `api/openapi.yaml`: nur Admins holen Schlüssel, er ist ein Bootstrap-Token mit einer Stunde Gültigkeit; die Policy `cell-api-join-keys` lässt die API in `kube-system` nichts anderes anlegen und nichts lesen (gegen die lokale Zelle geprüft) | bestanden |
| Eigene Adresse für 80/443 (E-53) | Ubuntu-VM (Lima), `install.sh`: Login antwortet auf der Adresse der Zelle (302), 443 auf localhost bleibt frei, kein ServiceLB; `node.sh` bricht ab, wenn ein anderes Programm 443 auf der Adresse hält. Der volle `make vm-test` lief bis zur Identität und brach dort am 13 min langen Download des Postgres-Images ab | bestanden (25.11.2026); voller `make vm-test` wartet auf den täglichen Lauf |

### App-Manifest und Operator

Eine App wird allein durch ihr Manifest installiert.

Stand 19.11.2026: Helm über Flux (E-18), Manifest geschärft (`app-manifest.md`).

| Prüfung | Wie | Ergebnis |
|---|---|---|
| App aus Manifest, Subdomain, SSO ohne Handgriff | `make e2e`, `TestReferenceApps`: Login als Testnutzer über den authentik-Flow, danach fragt der Test die App, wer angemeldet ist | bestanden für Forgejo, Nextcloud, Eigenbau-Platzhalter |
| Pod löschen | derselbe Test löscht alle App-Pods und wartet, bis die Sitzung wieder antwortet | bestanden |
| Entfernen behält Daten | derselbe Test löscht die App, prüft Namespace und Volumes, installiert neu und meldet sich wieder an | bestanden |
| Apps getrennt (E-45) | `make e2e`, `TestReferenceApps`: ein Pod außerhalb der Plattform erreicht den Dienst der App nicht, einer im Namespace der App schon; ein privilegierter Pod wird abgewiesen. Helm-Apps laufen als `helm-deployer` des Namespace (envtest) | bestanden (25.11.2026) für Forgejo, Nextcloud, Eigenbau-Platzhalter; voller `make e2e` danach 15 min grün |
| Operator-Logik | `make test`: envtest mit echtem API-Server (Datenbank zuerst, Platzhalter, Login, Proxy, Helm, `NeedsAttention`, Entfernen) | bestanden |
| Laufzeiten lokal | erste Installation inklusive Image-Download | Eigenbau-Platzhalter 10 s, Forgejo rund 2 min, Nextcloud rund 2 min |

### Tag-2-Betrieb

Sichern, Wiederherstellen und Updaten laufen deterministisch.

Stand 19.11.2026: Backups über barman-cloud und restic (E-23 bis E-28), lokales Backup-Ziel neben der Zelle.

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Restore in frischer Zelle | `make dr-test`: Markierung in Datenbank und Volume von Forgejo, Backup, Zelle löschen und neu aufbauen, Forgejo installieren, Restore aus dem Bucket, danach Login mit der neuen Identität | Daten vollständig, Login funktioniert; rund 12 min inklusive Neuaufbau der Zelle |
| Restore in derselben Zelle | `make e2e`, `TestBackupAndRestore`: Markierung, Backup, Markierung überschreiben, Restore | Forgejo und Nextcloud wieder auf dem Stand des Backups, je rund 100 s |
| Backups über das Gateway (E-47) | `make e2e`, `TestBackupAndRestore` gegen `backup-gateway` mit dem lokalen S3-Ziel dahinter; vorab von Hand: App-Schlüssel sieht nur `<root>/apps/<app>/`, eine andere App sieht dort nichts, ein falsches Geheimnis bekommt 403; Unit-Test `TestAppSecret` hält Operator und Auth-Proxy gleich | bestanden (25.11.2026), Ablage im Ziel unverändert |
| Fehlerhaftes Update | `make e2e`, `TestFailedUpdateRollsBack`: `ChangeRequest` auf eine Version, die es nicht gibt | nach 90 s Health-Frist zurück auf 15.0.11, Daten wie vorher, App `NeedsAttention`, `ChangeRequest` `RolledBack` |
| Änderungsprotokoll | jeder `ChangeRequest` hält Urheber, Begründung, Risiko, Patch, vorige Spec, Backup, Restore, Ergebnis und Zeiten | geprüft in `make test` (envtest) und `make e2e` |
| Steuerlogik | `make test`: geplantes Backup, Freigabe, erfolgreiche Änderung, Rollback mit Restore, Ablehnung | bestanden |

### Plattform-API

Alles rund um Apps und Betrieb geht ohne `kubectl`.

Stand 19.11.2026: Plattform-API `cell-api` (E-29), Freigaben nur über die API (E-30), Installieren, Entfernen und Wiederherstellen als Aktionen eines `ChangeRequest` (E-31).

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Apps und Betrieb nur über die API | `make e2e`: Installieren, Login, Entfernen und Neuinstallieren der drei Referenz-Apps, Backup und Restore, fehlerhaftes Update mit Rollback, Restore in eine frische Zelle (`make dr-test`), alles über `https://api.<domain>` mit Tokens der Identität und Freigabe durch einen Admin. `kubectl` spielt nur die Welt um die Zelle (Pods löschen, Testdaten schreiben) und schaut hinein, wo die API nichts zeigt (behaltener Namespace) | bestanden |
| Freigaben | `make e2e`, `TestChangeApproval`: Agent schlägt vor, darf nicht freigeben; ein Cluster-Admin kann weder einen freigegebenen `ChangeRequest` anlegen noch an der API vorbei freigeben; der Admin lehnt über die API ab; der Ereignis-Stream meldet die Änderung | bestanden |
| Zugriff | `make e2e`, `TestAccessGroups`: App auf eine Gruppe beschränkt, Mitglied kommt hinein, andere weist die Identität ab, nach dem Öffnen kommen alle hinein | bestanden |
| OpenAPI-Datei aktuell | `make test`: jede Route steht in `api/openapi.yaml` und umgekehrt, jede Antwort der API-Tests passt zu ihrem Schema (keine zusätzlichen Felder erlaubt) | bestanden |
| Steuerlogik | `make test`: Installieren, Entfernen mit und ohne Daten, Wiederherstellen als Änderung, Zugriffsgruppen, Validierung der Aktionen; Tokens (Zielgruppe, Ablehnung durch die Identität, Cache) | bestanden |

### Ops-Agent

Der Agent löst zwei Aufgaben, die keine Regel abdeckt.

Stand 19.11.2026: Agenten-Dienst `cell-agent` mit eigenem Konto (E-32), Modell und Schlüssel des Kunden (E-33), Aufgaben als Ressource `Task` mit Schritten (E-34), Installieren aus einem Manifest mit Prüfung durch einen Probelauf (E-35). Paketiert werden Compose-Dateien; Helm-Charts sind O-15.

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Szenario A | `make e2e`, `TestAgentRepairsApp`: falscher Wert (`WHOAMI_PORT_NUMBER=8080`) an der Plattform vorbei gesetzt, der Operator legt eine Diagnose-Aufgabe an, der Agent liest App, Pods und Logs über die API, findet die Ursache im Log und schlägt die Reparatur vor; nach Freigabe ist die App gesund | bestanden |
| Szenario B | `make e2e`, `TestAgentPackagesApp`: Memos aus seiner Compose-Datei; der erste Vorschlag mit einer Subdomain der Zelle wird vom Probelauf abgewiesen, der korrigierte nach Freigabe ohne Nacharbeit installiert | bestanden |
| Jeder Schritt im Protokoll | `make e2e` und `make test`: jeder Aufruf des Agenten steht als Schritt in seiner Aufgabe, auch bei parallelen Aufrufen; ohne Aufgabe oder in fremden Aufgaben weist die API ab; Vorschläge tragen ihre Aufgabe | bestanden |
| API legt keine Apps an | `make e2e`, `TestAPICannotCreateApps`: das Konto der API darf Apps nur im Probelauf anlegen (ValidatingAdmissionPolicy) | bestanden |
| Agent | `make test`: Diagnose und Paketierung mit einem geskripteten Modell (Fake der Messages-API), verlorene Aufgaben (Neustart, verlorene Antwort beim Übernehmen oder Beenden), Modell nicht erreichbar; eine Diagnose je Ursache, auch nach gescheiterter Wiederherstellung | bestanden |

Das Modell ist in den Tests geskriptet: geprüft ist alles um das Modell herum (Werkzeuge, Rechte, Protokoll, Probelauf, Installation), nicht die Qualität der Diagnose. Ein Lauf mit echtem Modell braucht einen Schlüssel (O-16).

### MCP-Gateway

Ein Endpunkt für die Tools aller Apps.

Stand 20.11.2026: agentgateway als MCP-Gateway, konfiguriert vom Operator aus den Manifesten (E-36), Feld `mcp` im Manifest, Memos im Katalog als erste App mit eigenem MCP-Endpunkt. Aufrufe von Memos-Werkzeugen beantwortet Memos mit seinem Anmeldefehler, weil das Gateway kein Memos-Token hat (O-18); Apps ohne eigenen Endpunkt sind O-19, die Grenzen von v0 O-20.

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Werkzeuge zweier Apps über einen Endpunkt | `make e2e`, `TestMCPGateway`: zwei Memos aus dem Katalog (`notes` für alle, `journal` nur für eine Gruppe); ein Konto der Gruppe sieht über `https://mcp.<domain>/mcp` die Werkzeuge beider, mit dem App-Namen vorn; sein Aufruf erreicht die App | bestanden |
| Ohne Zugriff keine Werkzeuge | dieselbe Prüfung: ein Konto außerhalb der Gruppe sieht nur `notes_*`, sein Aufruf eines `journal_*`-Werkzeugs wird abgewiesen; ohne Token oder mit einem Token der API antwortet das Gateway 401, ein Token des Gateways gilt nicht an der API | bestanden |
| Zugriffsregeln | `make test`, `TestGatewayConfig`: die Regeln mit cel-go ausgewertet, auch mit Gruppennamen, die selbst CEL enthalten; Apps ohne `mcp`, noch nie gesunde und gelöschte Apps fehlen | bestanden |
| Speicher | Gateway im Leerlauf, lokale Zelle | 11 MiB |

### Konsole

Admins melden sich im Browser an der Konsole der Zelle an.

Stand 24.11.2026: Konsole in Svelte unter `https://console.<domain>`, ausgeliefert von `cell-api` (E-39), eigener Login-Client mit PKCE, für `cell-admins` und lesend für `cell-viewers` (E-40, E-46), Aussehen aus Design-Tokens in zwei Tönen, Nacht und Papier (E-41). Bisher Anmeldung, Grundgerüst, Übersicht, Freigaben, Apps, Rechner, Organisation, Agenten und Verlauf der Zelle; die übrigen Ansichten folgen mit den Features im Backlog.

| Prüfung | Wie | Ergebnis |
|---|---|---|
| Anmeldung im Browser | `make e2e`, `TestConsoleLogin`: ein Admin meldet sich in Chromium (Playwright) an, sieht seinen Namen und die Navigation, wechselt den Ton, lädt neu; jeder Fehler in der Browser-Konsole auf Seiten der Konsole, auch eine Abweisung durch die CSP, lässt den Test scheitern | bestanden |
| Abmelden und Fehler | derselbe Test: Abmelden verwirft das Token und landet auf der Abmeldeseite der Identität; deren Single Sign-on bleibt (E-40); lehnt die API ein Token direkt nach der Anmeldung ab, zeigt die Konsole den Grund, statt sich endlos neu anzumelden | bestanden |
| Login-Protokoll | derselbe Test ohne Browser: Code mit PKCE, Tausch gegen ein Token von der Herkunft der Konsole aus (CORS der Identität), mit dem Token `GET /v0/whoami` mit Rolle `admin`; ein Konto außerhalb von `cell-admins` weist die Identität ab | bestanden |
| Rollen aus Gruppen (E-46) | Unit-Tests `internal/api` (`TestRoleOf`, `TestAuthentication`): ohne Gruppe Rolle `none`, `whoami` antwortet, alles andere 403 mit Hinweis auf `cell-viewers`; Viewer lesen Apps, Nutzer, Aufgaben, Rechner, Pods und Ereignisse und dürfen nichts ändern | bestanden (25.11.2026); Konsole als Viewer wartet auf den täglichen Lauf |
| Auslieferung | `make test`, `TestConsole`: Konsole nur auf ihrem Host, `/v0/` bleibt dort die API, unbekannte API-Pfade bleiben JSON-Fehler, fehlende Dateien unter `/assets/` 404 statt der Seite, Kopfzeilen (CSP, Cache); `TestLogin` gegen `api/openapi.yaml`; die Subdomain `console` kann keine App belegen (envtest) | bestanden |
| Rechner | `make e2e`, `TestConsoleLogin` (Playwright, `machines.spec.ts`): Ansicht „Machines" nach dem Figma-Entwurf „16 Cell": Kennzahlen, Bild der Verbindungen (Menschen per https, Rechner per WireGuard), je Rechner Apps, Prozessor, Speicher und Platte (Messwerte aus `metrics.k8s.io`, Platte aus Longhorn); „Add machine" zeigt den Beitrittsbefehl, auf der lokalen Zelle den Grund, warum keiner möglich ist; eine Anmeldung, die auf `/machines` beginnt, kehrt dorthin zurück | erste Fassung bestanden; überarbeitete Fassung wartet auf den täglichen Lauf |
| Apps | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): Ansicht „Apps" nach „06 Apps": installierte Apps mit Zustand, Gruppen, letzter Sicherung und Adresse; Katalog mit „Install" (Änderung wartet auf Freigabe); eine App öffnet sich im Drawer nach „07 Drawer - app" unter `/apps/<name>`: Version, Zugriff ändern, Sicherungen mit „Back up now" und „Bring back", Ereignisse, „Let Ops diagnose it", Entfernen mit Rückfrage. Angesehen mit nachgestellten API-Antworten in beiden Tönen | wartet auf den täglichen Lauf |
| Freigaben | `make e2e`, `TestConsoleLogin` (Playwright, `approvals.spec.ts`): Ansicht „Approvals" nach „02 Approvals": jede wartende Änderung als Ticket mit Antragsteller (Mensch oder Agent), Satz zur Änderung, Bild von Antragsteller, App und Gruppen, Grund, Risiko, Schutz während des Laufs, Schritten nach dem Ja und Patch zum Aufklappen; Approve und Decline (mit Rückfrage); daneben die Admins aus `cell-admins` und was ein Ja braucht. Angesehen mit nachgestellten API-Antworten | wartet auf den täglichen Lauf |
| Verlauf | `make e2e`, `TestConsoleLogin` (Playwright, `history.spec.ts`): Ansicht „History" nach „25 History": die letzten 30 Tage als Streifen (Änderungen, Agenten-Aufgaben, Sicherungen, je Tag das Schlimmste), Filter (wartend, von Menschen, von Agenten, nicht angewendet), Export der gezeigten Zeilen als CSV, Tabelle mit Antragsteller, Entscheider, Risiko, Zeit und Ausgang; eine Änderung öffnet sich unter `/history/<name>` im Drawer nach „26 Drawer - change rolled back" mit den Schritten und ihrem Ausgang. Angesehen mit nachgestellten API-Antworten | wartet auf den täglichen Lauf |
| Agenten | `make e2e`, `TestConsoleLogin` (Playwright, `agents.spec.ts`): Ansicht „Agents" nach „21 Agents": wer in 30 Tagen gearbeitet hat (Regeln: geplante Sicherungen, Sicherungen vor Änderungen, Rollbacks; Agenten: ihre Aufgaben; Menschen: ihre Änderungen), Ops und Packer mit ihren Aufgaben, Arbeitsbrett (wartet auf dich, Agenten arbeiten, diese Woche fertig), was ohne Menschen geschehen darf; eine Aufgabe öffnet sich unter `/agents/<name>` mit Auftrag, jedem API-Aufruf, Antwort und vorgeschlagenen Änderungen; Tickets und Verlauf verlinken die Aufgabe eines Agenten. Angesehen mit nachgestellten API-Antworten | wartet auf den täglichen Lauf |
| Organisation | `make e2e`, `TestConsoleLogin` (Playwright, `organization.spec.ts`): Ansicht „Organization" nach „18 Organization", mit flachen Gruppen statt Abteilungen: Kennzahlen, „Everyone" mit den Apps für alle, je Gruppe Mitglieder und ihre Apps, neue Gruppe, Agenten-Konten; Personen mit Gruppen, Person hinzufügen (mit erstem Passwort), App-Passwort (nur einmal gezeigt); eine Gruppe öffnet sich unter `/organization/<gruppe>` zum Ändern der Mitglieder; das Speichern schickt nur, wer hinzukommt und wer geht (`PATCH /v0/groups/<gruppe>/members`, je Person ein Aufruf an authentik), so macht kein Admin die gleichzeitige Änderung eines anderen rückgängig (`TestIdentity`, `TestChangeGroupMembers`). App-Passwörter entstehen eins nach dem anderen. Angesehen mit nachgestellten API-Antworten | wartet auf den täglichen Lauf |
| Übersicht | `make e2e`, `TestConsoleLogin` (Playwright, `home.spec.ts`): Startseite nach „01 Home": Kennzahlen (Zustand, laufende Apps, offene Freigaben, letzte Sicherung, Agenten), eine Schlagzeile aus dem Zustand („Notes is down. A fix is waiting for your OK."), je App eine Form, deren Höhe zeigt, wie viele Personen die App öffnen dürfen (liegend, wenn ihr Health-Check scheitert; eine App, deren Änderung scheiterte und die weiterläuft, gilt nicht als ausgefallen), die neueste offene Freigabe als Ticket mit Zusage und Absage, die Apps mit Zustand und Reichweite, die letzten Änderungen und wer in der Zelle arbeitet (Agenten, Regeln, Personen). Die Schlagzeile, die Reichweite und die letzte Sicherung sind als Unit-Tests geprüft (`console/unit/home.test.ts`). Angesehen mit nachgestellten API-Antworten in beiden Tönen | wartet auf den täglichen Lauf |
| Pods und Logs je App | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): im App-Drawer „What runs": je Pod seine Container mit Punkt (bereit, wartet, fällt aus), Neustarts, letztem Exit und Meldung; „Log" zeigt die letzten 300 Zeilen, „Before the restart" den Lauf vor dem letzten Absturz. Angesehen mit nachgestellten API-Antworten | wartet auf den täglichen Lauf |
| Update einer App aus der Konsole | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): im App-Drawer „Version" eine neue Version eintragen oder die des Katalogs übernehmen; daraus wird eine Änderung `PATCH /v0/apps/<name>` mit `{version}`, Risiko mittel, die wie jede Änderung auf Freigabe wartet (Sicherung vorher, Rückrollen beim gescheiterten Health-Check). Solange ein Update der App offen ist, lässt sich kein zweites anfragen | wartet auf den täglichen Lauf |
| Paketieren aus der Konsole | `make e2e`, `TestConsoleLogin` (Playwright, `apps.spec.ts`): unter Apps „From an open-source project": Name der App und Compose-Datei (hochladen oder einfügen) werden zur Aufgabe `POST /v0/tasks` der Art `package`; Packer schlägt die Installation vor, die wie jede Änderung auf Freigabe wartet. Der Name wird vorher geprüft (wie die API: beginnt mit einem Buchstaben, Kleinbuchstaben, Ziffern, Bindestriche, höchstens 40, nicht schon installiert), die Datei auf 65.536 Zeichen begrenzt (`console/unit/package.test.ts`); ein Link führt zur Aufgabe unter Agents | wartet auf den täglichen Lauf |
| Live-Ereignisse in der Konsole | `make e2e`, `TestConsoleLogin` (Playwright, `home.spec.ts`): die Übersicht liest `GET /v0/events` (Server-Sent Events, mit `fetch`, weil `EventSource` kein Token senden kann) und lädt bei jedem Ereignis neu, mehrere kurz hintereinander zählen einmal; endet der Strom, verbindet sie sich neu, die Pause wächst von 1 auf 30 Sekunden. „Live" zeigt, ob der Strom steht. Zerlegen des Stroms, Pausen und Wiederverbinden sind als Unit-Tests geprüft (`console/unit/live.test.ts`) | wartet auf den täglichen Lauf |
| Hilfsfunktionen | `make test` (`npm run check`): Zeitangaben wie „12 min ago", Sätze, Gruppen, Beteiligte und Schritte einer Änderung (`console/unit/`, `node --test`) | bestanden |
| Tokens | `make test`, `TestTokens`: Tokens der Konsole gelten an der API, Tokens des MCP-Gateways und von Apps nicht | bestanden |
| Typen und Vertrag | `make test` prüft die Konsole mit `svelte-check`, die API-Typen kommen aus `api/openapi.yaml` | bestanden |
| Größe | Produktions-Build | JavaScript 71 KB (28 KB gzip), CSS 15 KB, Schriften 54 KB |

## Risiken

| Risiko | Umgang |
|---|---|
| Jede App braucht eigene Handgriffe für den Login | Drei bewusst unterschiedliche Referenz-Apps; Muster ins Manifest ziehen |
| Volumes liegen auf einem Rechner (eine Replik, E-14) | Fällt der Rechner aus, ist die App bis zu seiner Rückkehr weg; Backups außerhalb der Zelle; Repliken über Rechner sind ein eigener Schritt |
| Ports zwischen den Rechnern ohne Firewall offen | Rechner sprechen nur im gemeinsamen Netz (E-57); die Firewall sperrt 6443, 10250, 5001 und 51820 nach außen; ohne Firewall lauschen sie weiter auf allen Adressen |
| Basisdienste brauchen zu viel Speicher für eine kleine VM | Gemessen (siehe Zelle per Befehl); notfalls Bausteine tauschen |
| „Läuft" ist nicht „integriert" | Zwei Stufen im Katalog sichtbar machen: generisch und integriert |
| Vertrauen in Agenten | Freigabe als Standard, Protokoll von Anfang an |

## Lokale Entwicklung

Für den schnellen Zyklus eine lokale Zelle (k3s in Docker). Sie hat keinen Snapshot-fähigen Speicher; alles rund um Snapshots und Restore wird auf einer echten VM abgenommen.
