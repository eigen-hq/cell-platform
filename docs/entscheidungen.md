# Entscheidungen

Drei Listen: was feststeht, was als Annahme gilt, was offen ist. Neue Einträge unten anhängen, Nummern nicht neu vergeben.

## Entschieden

| Nr. | Entscheidung |
|---|---|
| E-01 | Deterministisch zuerst. Agenten übernehmen nur, was sich nicht in Regeln fassen lässt. |
| E-02 | Gehostet wird jede self-hostbare Software, auch Eigenbau des Kunden. Paketform ist der Container. |
| E-03 | ~~Oberfläche~~ (siehe E-38), Office-Suite und zentrale UI liegen in anderen Repos. Dieses Repo liefert Plattform, Plattform-API und MCP-Gateway. |
| E-04 | Die zentrale Datenschicht hält Metadaten, nicht die Daten der Tools. |
| E-05 | Namensneutral. Der Produktname (derzeit „Eigen") steht nur hier und später in einer einzigen Konfigurationsvariable. Technische Bezeichner nutzen `cell`. |
| E-06 | Fokus zuerst auf junge Unternehmen ohne bestehende IT. Self-Hosting beim Kunden bleibt eine Option. |
| E-07 | Sprachmodelle zunächst über API-Keys des Kunden. |
| E-08 | Vorerst kein Remote; das Repo bleibt lokal. Abgelöst durch E-09. |
| E-09 | Remote ist ein privates GitHub-Repo `cell-platform`. Gearbeitet wird vorerst direkt auf `main`; jeder abgeschlossene Schritt wird gepusht. |
| E-10 | Agenten-Laufzeit (O-01): eigener schlanker Dienst in Go. Leitlinie: Der Ops-Agent kann alles, was ein Administrator kann (Status, Logs, Ereignisse, Ressourcen lesen, Befehle in Containern ausführen, Konfiguration ändern, neu starten, zurückrollen). Diese Fähigkeiten stellt die Plattform-API bereit, damit jede Aktion protokolliert und freigabefähig bleibt; der Weg über die API ist keine Einschränkung des Umfangs. |
| E-11 | Basisdienste installiert `deploy/bootstrap.sh` mit `kubectl` allein: Upstream-Manifeste (cert-manager, CloudNativePG) per Server-Side Apply, Helm-Charts (authentik, Longhorn) über den in k3s eingebauten Helm-Controller (`HelmChart`). Auf der VM ist damit nur k3s nötig. |
| E-12 | Lokale Zelle: k3d, Domain `cell.localhost`, eigene Root-CA über cert-manager (ClusterIssuer `cell`). `*.localhost` löst jeder Browser ohne DNS-Eintrag auf; öffentliche Wildcard-Dienste wie sslip.io scheitern an DNS-Rebind-Schutz. Auf der VM: eigene Domain und Let's Encrypt (`CELL_TLS=acme`). |
| E-14 | Volumes (A-06 bestätigt): Longhorn auf der VM mit CSI-Snapshots, eine Replik pro Volume. Geprüft auf Ubuntu 26.04.1 LTS. Die lokale Zelle nutzt `local-path` ohne Snapshots. |
| E-15 | Ziel-VM (A-12 bestätigt): 4 vCPU, 8 GB reichen. Die leere Zelle belegt 3,4 GB und lässt 4,5 GB für Apps. Messung in `stand.md` unter „Zelle per Befehl". |
| E-16 | VM-Abnahme läuft lokal in Lima (`make vm-test`), ohne Cloud-Konto. |
| E-17 | Operator (A-03 bestätigt): Go mit controller-runtime und controller-gen, also den Kubebuilder-Bibliotheken ohne dessen Projektgerüst. Controller-Tests laufen mit envtest. |
| E-18 | Helm-Anbindung (A-07 bestätigt): Flux source- und helm-controller. Der Operator schreibt `OCIRepository` oder `HelmRepository` und `HelmRelease`; ein Post-Renderer setzt `helm.sh/resource-policy: keep` auf alle PVCs der Charts, damit Daten das Deinstallieren überleben. |
| E-19 | Referenz-Apps (A-09 bestätigt): Forgejo (Helm, OIDC), Nextcloud (Image, OIDC über `user_oidc` per Start-Hook), `traefik/whoami` als Eigenbau-Platzhalter (Image, Login-Proxy). Alle drei bestehen denselben Ende-zu-Ende-Test. |
| E-20 | `App` ist cluster-weit, jede App hat den Namespace `app-<name>`. Entfernen behält die Daten (Volumes bleiben, Datenbank schläft, Namespace mit Label `retained`); erneutes Anlegen übernimmt sie. |
| E-21 | Lokale Zelle: Pods erreichen Zellen-Hosts über eine CoreDNS-Umschreibung auf Traefik. libcurl (PHP, curl) löst `*.localhost` immer auf Loopback auf; deshalb bekommen Image-Apps in `*.localhost`-Zellen eine kleine Loopback-Brücke als Sidecar (Unterbefehl des Operator-Images). Zellen mit echter Domain brauchen sie nicht. |
| E-22 | Vertrauen in die Zellen-CA: In jedem App-Namespace liegt die ConfigMap `cell-ca`. Image-Apps bekommen sie automatisch eingehängt, Helm-Apps über ihre Chart-Werte. |
| E-23 | Backups (A-05 bestätigt): Datenbanken über das CloudNativePG-Plugin barman-cloud (fortlaufendes WAL-Archiv, Basissicherung je Backup), Volumes über restic-Jobs, beides im Bucket unter `<prefix>/apps/<app>/`. Der restic-Snapshot eines Backups trägt Backup-Name und Koordinaten der Datenbanksicherung als Tags; eine frische Zelle findet damit alles allein aus dem Bucket. Aufbewahrung 30 Tage. |
| E-24 | Der Snapshot vor einer Änderung ist ein vollständiges Backup außerhalb der Zelle, kein Longhorn-Snapshot. Ein Weg für lokale und VM-Zellen; Longhorn-Snapshots bleiben eine spätere Beschleunigung. |
| E-25 | Jeder Datenbank-Cluster archiviert in ein eigenes WAL-Archiv (`db-<Zeit>`), ein wiederhergestellter also in ein neues. Der Besitzer der App-Datenbank ist eine verwaltete Rolle; ihr Passwort folgt immer dem Secret der Zelle, auch nach dem Restore aus einer anderen Zelle. |
| E-26 | Änderungen laufen als `ChangeRequest`: Freigabe (A-10), Backup, JSON Merge Patch auf die App-Spec, Health-Check innerhalb von `health.timeoutSeconds`, sonst Rückkehr zur vorigen Spec plus Restore des Backups. Pro App läuft höchstens eine Änderung oder Wiederherstellung; das Entfernen der App wartet, bis sie beendet ist. Eine Wiederherstellung hält eine Helm-App erst an, wenn Flux die ausgesetzte Release gesehen hat (`observedGeneration`), weil eine schon laufende Abstimmung sonst Workloads zurückbringen kann. Eine gescheiterte Änderung setzt die App auf `NeedsAttention`, bis eine spätere gelingt. Quelltyp (`helm`/`image`) und Login-Art lassen sich nicht ändern (Neuinstallation), die Größe von Datenbank und Volumes noch nicht (ein Rollback könnte sie nicht verkleinern). Eine Wiederherstellung setzt auch CronJobs der App aus und wartet, bis deren eigene Jobs beendet sind. Sind die Daten zurück, konfiguriert der Operator die App erst für ihre aktuelle Spec (bei einem Rollback die vorige), bevor die Workloads wieder laufen: sonst liefe die zurückgerollte Version auf den wiederhergestellten Daten. Bei Helm-Apps zählt nur eine Release, die Flux für diese Spec ausgerollt hat (`deployed`); `observedGeneration` allein genügt nicht, weil Flux es auch nach einem gescheiterten Upgrade setzt. Gibt Flux auf (`Stalled`), scheitert die Wiederherstellung, ohne die App zu starten. Beim Wiederanlaufen bekommen Workloads des Charts Replikas und CronJob-Aussetzung, wie sie die zuletzt ausgerollte Release vorgibt (aus Helms Release-Eintrag, mit den Kubernetes-Vorgaben, wo der Chart nichts setzt); der vorher erfasste Stand kann bei einem Rollback von der zurückgerollten Version stammen. Andere Workloads bekommen den erfassten Stand, außer jemand anderes hat das Feld seit dem Anhalten gesetzt (Feldbesitz `cell-restore`). Backups halten die App wie Änderungen und Wiederherstellungen (das Backup einer Änderung läuft unter deren Halt), damit nie ein Backup Daten kopiert, die gerade ersetzt werden. Ein gelöschtes laufendes Backup wird abgebrochen statt zu Ende geführt (es liest nur, Abbrechen ist gefahrlos); es hält die App, bis sein Job gestoppt ist. Eine gelöschte laufende Wiederherstellung läuft dagegen zu Ende, weil ein Abbruch halb ersetzte Daten hinterließe. Eine Datenbanksicherung ist an den Cluster gebunden, für den sie angefordert wurde; ersetzt eine Wiederherstellung ihn vorher, scheitert sie, statt eine Sicherung des alten Clusters mit dem WAL-Archiv des neuen zu kombinieren. |
| E-27 | Der Backup-Schlüssel (restic-Passwort, `CELL_BACKUP_PASSWORD`) wird bei der Einrichtung übergeben und muss außerhalb der Zelle verwahrt werden; ohne ihn kein Restore in eine neue Zelle. |
| E-28 | Lokales Backup-Ziel: `rclone serve s3` als Container neben der Zelle, Daten in `.cell/backup-s3`. Es überlebt `make cell-down` wie ein Bucket beim Anbieter. |
| E-29 | Plattform-API: eigener Dienst `cell-api` in Go, nur Standardbibliothek (`net/http`), läuft aus dem Operator-Image. Tokens prüft die Identität selbst: authentiks `userinfo` nimmt nur Tokens an, die es ausgestellt hat und noch kennt, und liefert die aktuellen Gruppen; zusätzlich muss die Zielgruppe (`aud`) des Tokens der API-Client `cell-api` sein, damit Tokens, die eine App für ihre Nutzer bekommt, hier nicht gelten. Ergebnis 30 s zwischengespeichert. Maschinen-Logins über Client-Credentials mit Nutzername und App-Passwort. Rollen aus Gruppen: `cell-admins` alles, `cell-agents` lesen und vorschlagen, alle anderen lesen Katalog, Apps und Änderungsprotokoll. Nutzer und Gruppen reicht die API an authentik durch. Vertrag: `api/openapi.yaml`, jede Antwort der Unit-Tests wird dagegen geprüft (kin-openapi, nur in Tests). |
| E-30 | Freigaben (A-10): Jede Änderung, auch die eines Admins, entsteht ohne Entscheidung und wartet; ein Admin entscheidet über `POST /v0/changes/{name}/approve` oder `/reject`, einmal. Durchgesetzt von der Admission-Policy `cell-change-approval`: Niemand legt einen `ChangeRequest` mit Entscheidung oder ohne Freigabepflicht an, und nur das Konto der API trägt eine Entscheidung ein, auch Cluster-Admins nicht. Das Konto der API darf `App`s nicht schreiben. Die Risikostufe legt die API fest, nicht der Antragsteller: nur Zugriffsgruppen niedrig, nur die Version mittel, alles andere (Quelle, Werte, Bedarf, Subdomain) hoch; ebenso hoch ist eine Installation, die Daten einer früher entfernten App gleichen Namens übernimmt, und die Begründung sagt das. |
| E-31 | Installieren, Entfernen und Wiederherstellen sind Aktionen eines `ChangeRequest` (`action`: `update`, `install`, `remove`, `restore`) und laufen durch dieselbe Freigabe, dasselbe Backup vorher und dasselbe Protokoll. Install legt die App gesperrt für die Änderung an und endet mit dem ersten Health-Check; wird sie nicht gesund, bleibt sie zur Diagnose installiert. Remove sichert, löscht die App und mit `deleteData` auch ihren Namespace mit allen Daten in der Zelle; Backups im Bucket bleiben. Restore sichert erst den aktuellen Stand und nennt das Backup immer beim Namen, damit Freigebende sehen, was zurückkommt. Zugriff (`access.groups`) ist ein Update mit geringem Risiko; der Operator bindet die Gruppen an die Anwendung in authentik, neue Bindungen vor dem Entfernen alter, damit ein Fehler die App nie öffnet. |
| E-32 | Ops-Agent (E-10): eigener Dienst `cell-agent` in Go aus dem Operator-Image, ohne Rechte im Cluster (kein Service-Account-Token). Sein Konto legt der Operator an: Dienstkonto `cell-agent` in authentik, Gruppe `cell-agents`, App-Passwort einmalig als Secret `cell-system/cell-agent`. Er arbeitet Aufgaben nacheinander ab; seine Werkzeuge sind je ein Aufruf der Plattform-API (Apps, Pods, Logs, Ereignisse, Änderungen, Katalog lesen; Update und Installation vorschlagen). Ohne Modell-Schlüssel warten Aufgaben, bis ein Agent mit Schlüssel läuft. Startet der Agent neu, beendet er seine laufenden Aufgaben als gescheitert. |
| E-33 | Sprachmodell des Agenten (E-07): Claude Opus 5.5 (`claude-opus-5-5`) über die Messages API mit dem Schlüssel des Kunden (`CELL_LLM_API_KEY`, Secret `cell-agent-llm`), Aufwand `high`, höchstens 40 Modellaufrufe je Aufgabe. Modell über `CELL_AGENT_MODEL` austauschbar. Bibliothek: anthropic-sdk-go mit seinem Tool-Runner; jede Anfrage schickt das ganze Gespräch (keine serverseitigen Threads), damit Tests ein einfaches Ersatzmodell nutzen können. |
| E-34 | Aufgaben (`Task`, `cell.internal/v1alpha1`): `diagnose` oder `package`, angelegt vom Operator (eine Diagnose je Ursache, aus der eine App `NeedsAttention` wird) oder von einem Admin (`POST /v0/tasks`). Ein Agent übernimmt sie (`claim`) und beendet sie mit seiner Antwort (`finish`). Jeder andere Aufruf eines Agenten nennt seine Aufgabe im Header `X-Cell-Task`; die API trägt ihn als Schritt in die Aufgabe ein, bevor sie ihn ausführt, und lehnt ihn ab, wenn das nicht geht (keine laufende Aufgabe dieses Agenten, mehr als 200 Schritte). Änderungen, die ein Agent vorschlägt, nennen die Aufgabe (`spec.task`) und gelten nur der App der Aufgabe. Aufgaben lesen nur Admins und Agenten, weil Antworten Logs zitieren können. Änderungsprotokoll sind damit `ChangeRequest`s und `Task`s zusammen. |
| E-35 | Installation aus einem Manifest (`POST /v0/apps` mit `manifest` statt `catalog`), zum Beispiel von einem Agenten paketiert: hohes Risiko, das Manifest wird vor der Freigabe vom Cluster geprüft (Dry-Run des `App`-Objekts, also Schema und CEL-Regeln). Das Konto der API darf `App`s dafür anlegen, die Admission-Policy `cell-api-dry-run` lässt aber nur Dry-Runs zu; angelegt wird weiterhin nur vom Operator nach Freigabe (E-30). |
| E-36 | MCP-Gateway (A-08 bestätigt): agentgateway v1.7.0 als einzelner Dienst `cell-mcp` in `cell-system` unter `https://mcp.<domain>/mcp`, ohne den Kubernetes-Controller von agentgateway (keine Gateway-API-Ressourcen, ein Baustein weniger). Seine Konfiguration (ConfigMap `cell-mcp`) schreibt der Operator aus den Apps mit `mcp`: ein Ziel je App, sobald sie gesund war, Werkzeugnamen immer mit dem App-Namen vorn, zustandslos (ein Neustart verliert nichts), eine ausgefallene App fällt aus der Liste, statt die anderen mitzureißen. Tokens müssen von der Identität für den Client `cell-mcp` stammen (Signatur und Zielgruppe geprüft); Tokens der API gelten nicht und umgekehrt. Zugriff wie beim Login der App: Mitglieder von `access.groups`, ohne Gruppen alle Konten. Werkzeuge anderer Apps fehlen in der Liste, Aufrufe darauf lehnt das Gateway ab. Das Token des Nutzers geht nicht an die App. Andere Kopfzeilen des Clients reicht agentgateway weiter; die des Login-Proxys (`X-authentik-*`), denen Proxy-Apps vertrauen dürfen, setzt das Gateway deshalb selbst: den Nutzernamen aus dem Token, die übrigen entfernt es. So gibt sich kein Client einer App gegenüber als jemand anderes aus. |
| E-13 | Zugangsdaten der Identität (DB-Passwort, Secret Key, Admin-Passwort, API-Token) erzeugt der Bootstrap einmalig und legt sie nur als Secrets in `cell-system` ab. |
| E-37 | Kein MVP und keine Meilensteine. Entwickelt wird nach Bedarf; was läuft, steht mit Nachweis in `stand.md`. Bisherige Ausschlüsse (mehrere Knoten, VPN, Build-Pipeline, Oberfläche) gelten nicht mehr. |
| E-38 | Die Konsole (Verwaltungsoberfläche der Zelle) entsteht in diesem Repo. Office-Suite und zentrale UI bleiben in anderen Repos (E-03). |
| E-39 | Konsole: Vite, Svelte 5 und TypeScript in `console/`, gebaut im Image (Node-Stufe) und von `cell-api` unter `https://console.<domain>` ausgeliefert (Ordner `/console`, `CONSOLE_DIR`). Weil API und Konsole denselben Ursprung haben, ruft die Konsole `/v0/...` ohne CORS; unbekannte `/v0/`-Pfade bleiben JSON-Fehler der API, auf dem API-Host gibt es keine Konsole. Strenge CSP: nur eigene Skripte und Stile, Verbindungen nur zur eigenen Herkunft und zur Identität, Schriften selbst gehostet. Typen der API erzeugt `openapi-typescript` aus `api/openapi.yaml`, die Konsole kennt also nur, was der Vertrag sagt. Kein Backend für die Konsole: sie nutzt dieselbe API wie CLI und Agenten. |
| E-40 | Login der Konsole: eigener öffentlicher Client `cell-console` in authentik (Autorisierungscode mit PKCE, eine feste Rücksprungadresse, kein Geheimnis), angelegt von `cell-api` beim Start. `GET /v0/login` nennt Endpunkte, Client und Scopes. Tokens akzeptiert die API, wenn ihre Zielgruppe einer der eigenen Clients ist (`cell-api`, `cell-console`); Tokens von Apps und vom MCP-Gateway nicht. Künftige eigene Clients (Mobil, Desktop) bekommen je einen Client in dieser Liste, keine eigene Logik. Die Konsole ist auf `cell-admins` beschränkt (Bindung in authentik), Tokens liegen in `sessionStorage`; ist eines abgelaufen, meldet sich die Konsole neu an, was die Identität ohne Rückfrage beantwortet, solange ihre Sitzung gilt. Abmelden verwirft das Token und beendet die Sitzung der Konsole beim Provider (OIDC End-Session); die Sitzung der Identität selbst (Single Sign-on) bleibt wie bei den Apps der Zelle. |
| E-41 | Aussehen der Konsole: Design-Tokens im DTCG-Format in `console/tokens/`, drei Schichten: Markenwerte (`brand.json`, aus Sketches über die Figma-Datei Platform Console 02), ihre Bedeutung je Ton (`night.json`, `paper.json`) und was Komponenten malen (`components.json`). `scripts/tokens.mjs` macht daraus CSS-Variablen, Verweise bleiben `var()`, also lässt sich jede Schicht später je Kunde überschreiben. Komponenten (`src/lib/ui`) kennen die API nicht, Ansichten (`src/views`) setzen beides zusammen. Der Ton folgt der Systemeinstellung, ein Schalter in der Konsole überschreibt ihn im Browser. |

## Annahmen

Vorschläge aus der Planung, noch nicht bestätigt. Sie dürfen genutzt werden; wer eine kippt, trägt es hier ein.

| Nr. | Annahme | Begründung | Alternative |
|---|---|---|---|
| A-01 | Kubernetes in Form von k3s | Selbstheilung und Rollouts eingebaut; die deklarative API ist dieselbe Schnittstelle für Operator und Agenten; ein Binary, schnell installiert | Docker Compose pro App (einfacher, aber alles Deklarative müsste selbst gebaut werden) |
| A-02 | Eine Zelle pro Kunde: eigene VM, eigener Cluster | Harte Trennung, wichtig bei Eigenbau-Code; gehosteter und selbst betriebener Fall sind identisch | Geteilter Cluster mit Namespaces (günstiger, aber Mandanten-Thema) |
| A-03 | Operator in Go mit Kubebuilder | Standard im Kubernetes-Umfeld, beste Bibliotheken | Python (leichter zu lesen, schwächeres Ökosystem) |
| A-04 | Identität: authentik | Deckt OIDC, SAML, LDAP und Login-Proxy ab, also auch Apps ohne modernes Login | Keycloak, Zitadel |
| A-05 | Datenbanken: CloudNativePG, Sicherung über das Barman-Cloud-Plugin | Eine Datenbank je App mit einheitlichem Backup | Datenbank pro App im eigenen Container |
| A-06 | Volumes mit Snapshots: Longhorn | Snapshots und Auslagerung in einen S3-Bucket in einem Baustein | ZFS-LocalPV; auf echter VM vergleichen |
| A-07 | Helm-Installationen über den Helm-Controller von Flux | Upgrade und Rollback sind dort ausgereift | Helm-Bibliothek direkt im Operator |
| A-08 | MCP-Gateway: agentgateway | Offen lizenziert, spricht MCP, läuft auf Kubernetes | Eigenes schlankes Gateway |
| A-09 | Referenz-Apps: Forgejo (einfach), Nextcloud (Härtetest mit echten Migrationen), ein Eigenbau-Container als Platzhalter für eine App aus der Office-Suite | Drei bewusst verschiedene Fälle | GitLab als Härtetest (braucht deutlich mehr Ressourcen) |
| A-10 | Vorerst braucht jede schreibende Agenten-Aktion eine Freigabe | Vertrauen in Agenten ist eine ungeprüfte Hypothese | Umkehrbare Aktionen automatisch |
| A-11 | Doku auf Deutsch; Code, Bezeichner und Commits auf Englisch | Arbeitssprache des Teams | Alles auf Englisch, spätestens bei Veröffentlichung |
| A-12 | Ziel-VM: 4 vCPU, 8 GB RAM, Ubuntu LTS | Startwert; messen | Größer oder kleiner nach Messung |

## Offen

| Nr. | Frage | Fällig |
|---|---|---|
| O-01 | ~~Agenten-Laufzeit~~ entschieden, siehe E-10 | – |
| O-02 | Objektspeicher in der Zelle: Garage oder SeaweedFS | Sobald eine App S3 braucht |
| O-03 | Endgültige API-Gruppe. `cell.internal` ist ein Platzhalter | Vor der ersten Veröffentlichung |
| O-04 | Lizenz des Repos | Vor der ersten Veröffentlichung |
| O-05 | Privates Netz pro Zelle („nur im Firmennetz erreichbar") | Mit der Konsole (Freigabe je App) |
| O-06 | Anbieter und Standort für die ersten gehosteten Zellen | Vor der ersten Demo mit Externen |
| O-07 | Operator und Plattform-API sprechen authentik mit dem Bootstrap-Token (Admin-Rechte) an. Eigenes Dienstkonto mit engen Rechten | Vor der ersten Kundenzelle |
| O-09 | Update-Regel im Manifest (`update.auto`): noch nicht umgesetzt, es fehlt eine Quelle für neue Versionen. Updates laufen über `ChangeRequest` | Mit der Katalogpflege |
| O-10 | App-konsistente Backups (Hooks wie Wartungsmodus vor dem Backup); bisher werden laufende Apps gesichert, Datenbank und Volumes kurz nacheinander | Vor der ersten Kundenzelle |
| O-08 | Images und Chart-Quellen sind per Tag gepinnt, nicht per Digest; das Operator-Image hat noch keine Registry (VM bekommt es als Archiv) | Vor der ersten Kundenzelle |
| O-12 | Vier-Augen-Prinzip für Menschen: Ein Admin darf derzeit die eigene Änderung freigeben (mit eigenem Aufruf, protokolliert). Alternative: Freigabe nur durch einen anderen Admin, dann braucht jede Zelle zwei | Vor der ersten Kundenzelle |
| O-13 | Agenten verwalten keine Nutzer und Gruppen (das hat keinen Freigabe-Ablauf); Zugriff auf Apps können sie über `access` vorschlagen. Der Ops-Agent brauchte es bisher nicht | Sobald ein Agent es braucht |
| O-14 | Befehle in Containern ausführen und Apps neu starten (E-10) gibt es in der API noch nicht; der Ops-Agent kam bisher ohne aus. Offen, wie sie freigegeben und protokolliert werden (ein Neustart ändert keinen gewünschten Zustand) | Wenn eine Diagnose es braucht |
| O-15 | Paketieren aus Helm-Charts: Der Agent kann Charts nicht lesen (Werte, README), nur Compose-Dateien aus der Aufgabe. Braucht einen API-Aufruf, der einen Chart abruft | Wenn eine Katalog-App nur als Chart vorliegt |
| O-16 | Abnahme mit echtem Modell: Die Tests nutzen ein Ersatzmodell mit festem Ablauf. Ein Lauf mit Claude kostet Geld und schickt Logs und Manifeste an den Anbieter, braucht also Schlüssel und Zustimmung des Kunden | Vor der Demo |
| O-17 | Datenschutz: Was der Agent liest (Logs, Ereignisse, Manifeste) geht an den Modellanbieter. Für Kunden mit Auflagen: Verarbeitung in der EU, Cloud-Anbieter mit Auftragsverarbeitung oder ein eigenes Modell (dann E-33 ändern) | Vor der ersten Kundenzelle |
| O-11 | Die Backup-Zugangsdaten gelten für die ganze Zelle und liegen als Secret `backup-s3` in jedem App-Namespace (das CloudNativePG-Plugin braucht sie dort). Eine kompromittierte App mit Lesezugriff auf Secrets ihres Namespace, oder ein Manifest, das das Secret einbindet, könnte die Backups aller Apps der Zelle lesen oder löschen. Optionen: Zugangsdaten je App mit Rechten nur auf `<prefix>/apps/<app>/` (hängt am Anbieter, O-06), restic-Passwort je App aus dem Zellen-Schlüssel abgeleitet, Bucket mit Objektsperre gegen Löschen | Vor der ersten Kundenzelle |
| O-18 | Zugang des Gateways zu Apps mit eigenen Tokens: Memos listet seine Werkzeuge ohne Anmeldung, Aufrufe brauchen ein persönliches Memos-Token (nur in der Oberfläche von Memos anzulegen). Optionen: ein Token je App, das ein Admin hinterlegt (alle Nutzer handeln dann als dieses Konto in der App); ein Token je Nutzer und App; Apps, die Tokens der Identität annehmen (Eigenbau-Apps). Bis dahin zeigt das Gateway die Werkzeuge, Aufrufe beantwortet die App mit ihrem Anmeldefehler | Bevor Kunden Werkzeuge fremder Apps aufrufen |
| O-19 | Apps ohne eigenen MCP-Endpunkt (Forgejo, Nextcloud): MCP-Server als zweiter Container im Manifest oder Werkzeuge aus der OpenAPI-Beschreibung der App (kann agentgateway). Beides braucht O-18 | Wenn eine Katalog-App Werkzeuge anbieten soll |
| O-20 | Grenzen des Gateways v0: Gruppenänderungen und gesperrte Konten wirken erst mit dem nächsten Token (authentik stellt Tokens für eine Stunde aus; das Gateway prüft die Signatur, fragt die Identität nicht wie die API). Konfigurationsänderungen erreichen das Gateway nach etwa einer Minute (Kubelet). Eine Zelle ohne MCP-Apps beantwortet die Werkzeugliste mit einem Fehler. Kürzere Tokens oder ein Abgleich bei der Identität beheben das Erste | Vor der ersten Kundenzelle |
| O-21 | Login-Proxy: Der eingebettete Outpost von authentik erfährt von neuen Proxy-Apps über eine Websocket-Verbindung; bricht sie ab, verbindet er sich erst nach 300 s neu und bedient neue Hosts so lange nicht. Häufigster Abbruch war, dass gunicorn alle 1000 Anfragen einen Worker neu startet; das ist abgeschaltet (`AUTHENTIK_WEB__MAX_REQUESTS=0`). Bleibt ein Abbruch durch andere Ursachen: Apps mit kurzer Frist (whoami: 120 s) scheitern dann bei der Installation und bleiben zur Diagnose stehen. Abhilfe: Wartezeit auf den Outpost nicht auf die Frist der App anrechnen | Vor der ersten Kundenzelle |
| O-22 | Netz in der Zelle: Jeder Pod erreicht die Dienste aller Apps direkt, am Login-Proxy und am Gateway vorbei. Eine kompromittierte App könnte einer Proxy-App also Kopfzeilen des Login-Proxys vorspielen. Abhilfe: NetworkPolicies, nach denen App-Dienste nur Traefik, das Gateway und den Operator (Health-Checks) annehmen | Vor der ersten Kundenzelle |
| O-23 | Zelle über mehrere Rechner und Standorte: Wie verteilt sich eine Zelle (ein k3s-Cluster über Standorte oder ein Cluster je Standort), und was wird aus Trennung auf VM-Ebene (A-02), Volumes (A-06) und Backups, die von einem Knoten ausgehen | Vor dem ersten Code dafür |
| O-24 | VPN oder Mesh: Verbindung zwischen Standorten einer Zelle und Zugang für Menschen ins Firmennetz; Baustein und Verhältnis zu O-05 | Mit O-23 |
| O-25 | Gemeinsame Anmeldung von Konsole und Office-Oberfläche: dieselbe Identität der Zelle, eigener Client je Oberfläche oder ein gemeinsamer | Wenn die Office-Oberfläche angebunden wird |
| O-26 | Office-Oberfläche in der Zelle: als App aus dem Katalog oder als Teil der Plattform; wie Nutzer zwischen beiden Oberflächen wechseln | Wenn die Office-Oberfläche angebunden wird |
| O-27 | Was die Office-Oberfläche von der Plattform braucht: Plattform-API, Tool-Gateway, Ereignisse; welche Rechte ihr Konto hat | Wenn die Office-Oberfläche angebunden wird |

## Versionen

Gepinnt in `deploy/versions.env` (einzige Quelle), geprüft am 19.11.2026 über GitHub-Releases, den k3s-Kanal `stable` und die Paketquellen.

| Baustein | Version | Bemerkung |
|---|---|---|
| k3s | v1.37.2+k3s1 | Kanal `stable`; bringt Traefik v3.7 mit |
| k3d | v5.9.0 | nur lokale Zelle |
| kubectl | v1.37.2 | passend zu k3s |
| cert-manager | v1.21.3 | |
| CloudNativePG | 1.30.2 | |
| authentik | 2026.8.5 (Chart) | braucht kein Redis mehr, nur Postgres |
| Longhorn | 1.13.0 | nur VM |
| external-snapshotter | v8.7.0 | k3s bringt keinen Snapshot-Controller mit |
| Go | 1.27.4 | controller-runtime v0.25.3, client-go v0.37.2, controller-tools v0.23.0, envtest 1.37.0 |
| Flux | 2.10.0 | nur source- und helm-controller |
| Lima | 2.3.0 | nur `make vm-test` |
| barman-cloud-Plugin | 0.16.0 | CloudNativePG-Backups |
| restic | 0.19.1 | Volume-Backups |
| rclone | 1.76.0 | nur lokales Backup-Ziel (`serve s3`) |
| Operator-Image | golang:1.27.4-trixie, distroless static-debian13 | |
| Forgejo | Chart 17.3.2, App 15.0.11 (LTS) | |
| Nextcloud | 34.0.6-apache (`stable`), user_oidc 8.13.0 | |
| traefik/whoami | v1.12.0 | Eigenbau-Platzhalter |
| kin-openapi | v0.153.0 | nur Tests: prüft Antworten der API gegen `api/openapi.yaml` |
| anthropic-sdk-go | v2.12.0 | Ops-Agent (E-33) |
| Memos | neosmemo/memos 26.11.1 | Katalog-App mit eigenem MCP-Endpunkt (`/mcp`, seit Memos 0.27); im Test paketiert der Agent sie auch aus einer Compose-Datei |
| agentgateway | v1.7.0 (`cr.agentgateway.dev/agentgateway`) | MCP-Gateway (E-36) |
| cel-go | v0.29.2 | nur Tests: wertet die Zugriffsregeln des Gateways aus |
| Node | 26.13.1 (`node:26.13.1-trixie-slim`) | nur Build der Konsole im Image (E-39) |
| Vite | 8.3.8 | Konsole, mit @sveltejs/vite-plugin-svelte 7.3.2 |
| Svelte | 5.59.1 | Konsole; svelte-check 4.7.8, @tsconfig/svelte 5.0.9 |
| TypeScript | 5.9.3 | nicht 7.x: svelte-check und openapi-typescript unterstützen sie noch nicht (Peer-Bereiche) |
| openapi-typescript / openapi-fetch | 7.13.0 / 0.17.0 | Typen und Client der Konsole aus `api/openapi.yaml` |
| Funnel Display, Funnel Sans | @fontsource-variable 5.3.0 (OFL-1.1) | Schriften der Konsole, selbst gehostet |
| Playwright | 1.65.1 | nur Tests: Anmeldung im echten Browser (`TestConsoleLogin`) |

## Recherche-Notizen

Stand 18.11.2026. Vor dem Pinnen von Versionen erneut prüfen.

- **k3s**: Ende August 2026 war die Reihe v1.36 aktuell. Lizenz Apache 2.0.
  https://jacar.es/en/k3s-vs-k0s-vs-talos-comparison/
- **CloudNativePG**: Im September 2026 erschien 1.30.1. Backups in einen Objektspeicher laufen über das Barman-Cloud-Plugin; die früher eingebaute Variante ist abgekündigt.
  https://cloudnative-pg.io/plugin-barman-cloud/
- **MinIO**: Die Community-Variante wird nicht mehr weiterentwickelt und ist archiviert. Für Neues kommen Garage (AGPLv3) oder SeaweedFS (Apache 2.0) in Frage.
  https://www.infoq.com/news/2025/12/minio-s3-api-alternatives
- **authentik**: Stellt als einziger der drei gängigen Kandidaten selbst ein LDAP-Verzeichnis bereit und bringt einen Login-Proxy für Apps ohne eigenes Login mit.
  https://www.ssdnodes.com/learn/keycloak-vs-authentik-vs-zitadel
- **agentgateway**: Apache 2.0, bei der Linux Foundation, unterstützt MCP und lässt sich über die Kubernetes Gateway API betreiben.
  https://agentgateway.dev/docs/kubernetes/latest/
- **kagent**: CNCF-Sandbox-Projekt, das Agenten als Kubernetes-Ressourcen betreibt und Werkzeuge über MCP anbindet.
  https://www.kagent.dev
- **Arbeitsteilung Agent und Operator**: Das Muster „Agent urteilt, deterministische Controller führen aus" wird für Kubernetes ausdrücklich empfohlen.
  https://thenewstack.io/ai-agents-kubernetes-control/
- **Cloudron-Manifest** als Vorbild für `app-manifest.md`:
  https://docs.cloudron.io/packaging/manifest und https://docs.cloudron.io/packaging/addons
