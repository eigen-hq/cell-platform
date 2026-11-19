# Entscheidungen

Drei Listen: was feststeht, was als Annahme gilt, was offen ist. Neue Einträge unten anhängen, Nummern nicht neu vergeben.

## Entschieden

| Nr. | Entscheidung |
|---|---|
| E-01 | Deterministisch zuerst. Agenten übernehmen nur, was sich nicht in Regeln fassen lässt. |
| E-02 | Gehostet wird jede self-hostbare Software, auch Eigenbau des Kunden. Paketform ist der Container. |
| E-03 | Oberfläche, Office-Suite und zentrale UI liegen in anderen Repos. Dieses Repo liefert Plattform, Plattform-API und MCP-Gateway. |
| E-04 | Die zentrale Datenschicht hält Metadaten, nicht die Daten der Tools. |
| E-05 | Namensneutral. Der Produktname (derzeit „Eigen") steht nur hier und später in einer einzigen Konfigurationsvariable. Technische Bezeichner nutzen `cell`. |
| E-06 | Fokus zuerst auf junge Unternehmen ohne bestehende IT. Self-Hosting beim Kunden bleibt eine Option. |
| E-07 | Sprachmodelle zunächst über API-Keys des Kunden. |
| E-08 | Vorerst kein Remote; das Repo bleibt lokal. Abgelöst durch E-09. |
| E-09 | Remote ist ein privates GitHub-Repo `cell-platform`. Gearbeitet wird vorerst direkt auf `main`; jeder abgeschlossene Schritt wird gepusht. |
| E-10 | Agenten-Laufzeit (O-01): eigener schlanker Dienst in Go. Leitlinie: Der Ops-Agent kann alles, was ein Administrator kann (Status, Logs, Ereignisse, Ressourcen lesen, Befehle in Containern ausführen, Konfiguration ändern, neu starten, zurückrollen). Diese Fähigkeiten stellt die Plattform-API bereit, damit jede Aktion protokolliert und freigabefähig bleibt; der Weg über die API ist keine Einschränkung des Umfangs. |
| E-11 | Basisdienste installiert `deploy/bootstrap.sh` mit `kubectl` allein: Upstream-Manifeste (cert-manager, CloudNativePG) per Server-Side Apply, Helm-Charts (authentik, Longhorn) über den in k3s eingebauten Helm-Controller (`HelmChart`). Auf der VM ist damit nur k3s nötig. |
| E-12 | Lokale Zelle: k3d, Domain `cell.localhost`, eigene Root-CA über cert-manager (ClusterIssuer `cell`). `*.localhost` löst jeder Browser ohne DNS-Eintrag auf; öffentliche Wildcard-Dienste wie sslip.io scheitern an DNS-Rebind-Schutz. Auf der VM: eigene Domain und Let's Encrypt (`CELL_TLS=acme`). |
| E-13 | Zugangsdaten der Identität (DB-Passwort, Secret Key, Admin-Passwort, API-Token) erzeugt der Bootstrap einmalig und legt sie nur als Secrets in `cell-system` ab. |

## Annahmen

Vorschläge aus der Planung, noch nicht bestätigt. Sie dürfen genutzt werden; wer eine kippt, trägt es hier ein.

| Nr. | Annahme | Begründung | Alternative |
|---|---|---|---|
| A-01 | Kubernetes in Form von k3s | Selbstheilung und Rollouts eingebaut; die deklarative API ist dieselbe Schnittstelle für Operator und Agenten; ein Binary, schnell installiert | Docker Compose pro App (einfacher, aber alles Deklarative müsste selbst gebaut werden) |
| A-02 | Eine Zelle pro Kunde: eigene VM, eigener Cluster | Harte Trennung, wichtig bei Eigenbau-Code; gehosteter und selbst betriebener Fall sind identisch | Geteilter Cluster mit Namespaces (günstiger, aber Mandanten-Thema) |
| A-03 | Operator in Go mit Kubebuilder | Standard im Kubernetes-Umfeld, beste Bibliotheken | Python (leichter zu lesen, schwächeres Ökosystem) |
| A-04 | Identität: authentik | Deckt OIDC, SAML, LDAP und Login-Proxy ab, also auch Apps ohne modernes Login | Keycloak, Zitadel |
| A-05 | Datenbanken: CloudNativePG, Sicherung über das Barman-Cloud-Plugin | Eine Datenbank je App mit einheitlichem Backup | Datenbank pro App im eigenen Container |
| A-06 | Volumes mit Snapshots: Longhorn | Snapshots und Auslagerung in einen S3-Bucket in einem Baustein | ZFS-LocalPV; in M0 auf echter VM vergleichen |
| A-07 | Helm-Installationen über den Helm-Controller von Flux | Upgrade und Rollback sind dort ausgereift | Helm-Bibliothek direkt im Operator |
| A-08 | MCP-Gateway: agentgateway | Offen lizenziert, spricht MCP, läuft auf Kubernetes | Eigenes schlankes Gateway |
| A-09 | Referenz-Apps: Forgejo (einfach), Nextcloud (Härtetest mit echten Migrationen), ein Eigenbau-Container als Platzhalter für eine App aus der Office-Suite | Drei bewusst verschiedene Fälle | GitLab als Härtetest (braucht deutlich mehr Ressourcen, eher nach M2) |
| A-10 | Im MVP braucht jede schreibende Agenten-Aktion eine Freigabe | Vertrauen in Agenten ist eine ungeprüfte Hypothese | Umkehrbare Aktionen automatisch |
| A-11 | Doku auf Deutsch; Code, Bezeichner und Commits auf Englisch | Arbeitssprache des Teams | Alles auf Englisch, spätestens bei Veröffentlichung |
| A-12 | Ziel-VM: 4 vCPU, 8 GB RAM, Ubuntu LTS | Startwert; in M0 messen | Größer oder kleiner nach Messung |

## Offen

| Nr. | Frage | Fällig |
|---|---|---|
| O-01 | ~~Agenten-Laufzeit~~ entschieden, siehe E-10 | – |
| O-02 | Objektspeicher in der Zelle: Garage oder SeaweedFS | Sobald eine App S3 braucht |
| O-03 | Endgültige API-Gruppe. `cell.internal` ist ein Platzhalter | Vor der ersten Veröffentlichung |
| O-04 | Lizenz des Repos | Vor der ersten Veröffentlichung |
| O-05 | Privates Netz pro Zelle („nur im Firmennetz erreichbar") | Nach dem MVP |
| O-06 | Anbieter und Standort für die ersten gehosteten Zellen | Vor der ersten Demo mit Externen |

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
| Go | 1.27.4 | controller-runtime v0.25.3, client-go v0.37.2 |

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
