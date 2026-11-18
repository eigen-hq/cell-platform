# App-Manifest

Entwurf v0. Das Manifest ist die zentrale Abstraktion der Plattform: Eine App beschreibt, was sie braucht, und die Plattform stellt es bereit. Das Format wird in M1 gegen die drei Referenz-Apps geschärft.

Vorbild ist das Manifest von Cloudron, bei dem Datenbank, Login und Mail als geteilte Dienste der Plattform angefordert werden.

## Grundidee

- Die App bringt nur sich selbst mit (Helm-Chart oder Container-Image).
- Alles Geteilte fordert sie an: Datenbank, Speicher, Login, Subdomain, Backup.
- Die Plattform setzt die konkreten Werte beim Installieren ein.
- Der Kunde gibt nur an, was nur er wissen kann, zum Beispiel die gewünschte Subdomain.

## Beispiel

```yaml
apiVersion: cell.internal/v1alpha1
kind: App
metadata:
  name: forgejo
spec:
  title: Forgejo
  version: "<app-version>"

  source:
    helm:                         # alternativ: image
      chart: oci://<registry>/<chart>
      version: "<chart-version>"
      values:                     # Schlüssel beispielhaft; Platzhalter ersetzt die Plattform
        database:
          host: "{{ .database.host }}"
          name: "{{ .database.name }}"
          user: "{{ .database.user }}"
          passwordSecret: "{{ .database.secretName }}"
        oidc:
          issuer: "{{ .auth.issuer }}"
          clientId: "{{ .auth.clientId }}"
          clientSecretRef: "{{ .auth.secretName }}"
        url: "https://{{ .access.host }}"

  needs:
    database:
      engine: postgres
    volumes:
      - name: data
        size: 10Gi
        mountPath: /data
    objectStorage: false
    smtp: false

  access:
    subdomain: git                # ergibt git.<domain-der-zelle>
    port: 3000
    auth:
      mode: oidc                  # oidc | proxy | ldap | none
      groupsClaim: groups

  health:
    path: /api/healthz
    timeoutSeconds: 300

  backup:
    schedule: daily
    hooks:
      pre: []                     # Befehle vor dem Snapshot, falls nötig
      post: []

  update:
    auto: patch                   # none | patch | minor
    strategy: snapshot-then-upgrade

  provisioning:
    users: jit                    # jit | scim | api | none

  mcp:
    endpoint: ""                  # leer, wenn die App keinen MCP-Server hat
```

## Felder

| Feld | Bedeutung |
|---|---|
| `source` | Woher die App kommt: `helm` (Chart, Version, Werte) oder `image` (ein Container mit Ports, Umgebungsvariablen, Kommando) |
| `needs` | Geteilte Dienste, die die Plattform bereitstellt |
| `access` | Subdomain, Port und Art des Logins |
| `health` | Woran die Plattform erkennt, dass die App gesund ist |
| `backup` | Zeitplan und optionale Befehle für einen konsistenten Stand |
| `update` | Was automatisch aktualisiert werden darf und wie |
| `provisioning` | Wie Nutzer in die App kommen |
| `mcp` | MCP-Endpunkt der App für das Gateway |

## Login-Arten

| `auth.mode` | Wann | Was die Plattform tut |
|---|---|---|
| `oidc` | App kann OpenID Connect | Legt einen Client in der Identität an und setzt die Werte ein |
| `ldap` | App kann nur LDAP | Stellt ein LDAP-Verzeichnis aus der Identität bereit |
| `proxy` | App hat kein eigenes Login | Schaltet einen Login-Proxy vor die App |
| `none` | App ist bewusst offen oder verwaltet Nutzer selbst | Nichts |

## Platzhalter

Werte, die erst in der Zelle feststehen, werden im Manifest als Platzhalter geschrieben und vom Operator ersetzt.

| Platzhalter | Inhalt |
|---|---|
| `.database.*` | Host, Name, Nutzer und Secret der App-Datenbank |
| `.auth.*` | Aussteller, Client-ID und Secret für den Login |
| `.access.host` | Vollständiger Hostname der App |
| `.volumes.<name>.claim` | Name des angelegten Volumes |

## Status

Der Operator schreibt in `status`:

- `phase`: `Installing`, `Ready`, `Updating`, `RollingBack`, `NeedsAttention`, `Removing`
- `version`: tatsächlich laufende Version
- `url`: Adresse der App
- `lastBackup`: Zeitpunkt und Ergebnis der letzten Sicherung
- `conditions`: Einzelzustände mit Begründung, lesbar für Menschen und Agenten

## Zwei Stufen im Katalog

- **Integriert**: gepflegtes Manifest mit Login, geprüften Updates und, wo vorhanden, MCP. Das kuratierte Standard-Set.
- **Generisch**: Manifest aus einem Chart oder einer Compose-Datei abgeleitet, vom Paketier-Agenten erzeugt. Läuft mit Subdomain, Backup und Neustart, aber ohne Garantie für Login und Updates.

## Offene Punkte für M1

- Wie werden app-spezifische Schritte nach der Installation abgebildet, zum Beispiel ein Login-Plugin aktivieren?
- Reicht ein Platzhalter-Mechanismus, oder braucht es pro App ein kleines Übersetzungsstück?
- Wie beschreibt ein Manifest Apps, die aus mehreren Containern bestehen und kein Chart haben?
