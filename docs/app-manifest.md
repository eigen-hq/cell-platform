# App-Manifest

Stand v1alpha1 (M1, an den drei Referenz-Apps geschärft). Das Manifest ist die zentrale Abstraktion der Plattform: Eine App beschreibt, was sie braucht, und die Plattform stellt es bereit. Die Go-Typen liegen in `api/v1alpha1/app_types.go`, die CRD in `deploy/crd/`.

Vorbild ist das Manifest von Cloudron, bei dem Datenbank, Login und Mail als geteilte Dienste der Plattform angefordert werden.

## Grundidee

- Die App bringt nur sich selbst mit (Helm-Chart oder Container-Image).
- Alles Geteilte fordert sie an: Datenbank, Volumes, generierte Geheimnisse, Login, Subdomain.
- Die Plattform setzt die konkreten Werte beim Installieren ein (Platzhalter).
- Der Kunde gibt nur an, was nur er wissen kann, zum Beispiel die gewünschte Subdomain.

Ein Katalog-Manifest (`catalog/<app>/manifest.yaml`) ist selbst eine `App`. Installieren heißt im MVP: dieses Objekt anlegen, gegebenenfalls mit geänderter Subdomain.

## Beispiel

Gekürzt aus `catalog/forgejo/manifest.yaml`:

```yaml
apiVersion: cell.internal/v1alpha1
kind: App
metadata:
  name: forgejo                     # ergibt Namespace app-forgejo
spec:
  title: Forgejo
  version: "15.0.11"                # als {{ .app.version }} verfügbar
  source:
    helm:                           # alternativ: image
      repo: oci://code.forgejo.org/forgejo-helm
      chart: forgejo
      version: "17.3.2"
      values:
        image:
          tag: "{{ .app.version }}"
        gitea:
          oauth:
            - name: authentik
              provider: openidConnect
              existingSecret: "{{ .auth.secretName }}"
              autoDiscoverUrl: "{{ .auth.discoveryUrl }}"
          config:
            database:
              HOST: "{{ .database.host }}:{{ .database.port }}"
  needs:
    database:
      engine: postgres
      size: 2Gi
  access:
    subdomain: git                  # ergibt git.<domain-der-zelle>
    port: 3000
    service: "{{ .app.name }}-http" # Service des Charts
    auth:
      mode: oidc
      redirectPaths: [/user/oauth2/authentik/callback]
      secretKeys: {clientId: key, clientSecret: secret}
    groups: [entwicklung]           # nur diese Gruppen; leer: alle Nutzer
  health:
    path: /api/healthz
    timeoutSeconds: 600
```

## Felder

| Feld | Bedeutung |
|---|---|
| `version` | Version der App. Bei Images der Standard-Tag, bei Charts über `{{ .app.version }}` in den Werten |
| `source.helm` | `repo` (`https://…` oder `oci://…`), `chart`, `version`, `values` mit Platzhaltern |
| `source.image` | `repository`, `tag` (Standard: `version`), `command`, `args`, `env` (wie bei Kubernetes, inklusive `valueFrom`), `files` |
| `source.image.files` | Dateien, die schreibgeschützt in den Container kommen (`path`, `content`, `executable`). So laufen app-spezifische Schritte nach der Installation, zum Beispiel als Start-Hook des Images |
| `needs.database` | `engine: postgres`, `size`. Eine eigene CloudNativePG-Datenbank je App |
| `needs.volumes` | `name`, `size`, `mountPath` (Image-Apps), höchstens 16. Charts legen ihre Volumes selbst an. Die Größe lässt sich wie bei der Datenbank noch nicht ändern, weil das Zurückrollen ein Volume nicht verkleinern kann |
| `needs.secrets` | Namen generierter Geheimnisse (Schlüssel `password`), einmal erzeugt, bleiben über Neuinstallationen erhalten |
| `metadata.name` | Name der App, höchstens 40 Zeichen; Namen mit `cell-` am Anfang gehören der Plattform (ihre eigenen Clients in der Identität) |
| `access` | `subdomain` (nicht `auth` und `api`, die gehören der Zelle; je Zelle nur einmal: ein Host, den schon eine andere Route bedient, bleibt bei ihr; sonst reserviert der Operator den Host atomar als ConfigMap `host-<subdomain>` in `cell-system`, bevor er etwas einrichtet; wer zuerst reserviert, behält ihn, bis seine Routen ihn verlassen haben oder die App entfernt ist), `port`, `service` (Standard: App-Name), `auth`, `groups` |
| `access.auth` | `mode`, `redirectPaths` (OIDC-Rückrufpfade), `secretKeys` (Schlüsselnamen im Client-Secret, wie Chart oder App sie erwarten) |
| `access.groups` | Gruppen der Identität, deren Mitglieder die App nutzen dürfen; leer heißt alle Nutzer der Zelle. Braucht einen Login (`oidc` oder `proxy`); andere werden schon beim Login abgewiesen |
| `health` | `path` (Standard `/`) und `timeoutSeconds` (Standard 300), siehe Status. Auch die Frist, in der ein Update gesund werden muss |
| `backup.schedule` | `none`, `hourly`, `daily` (Standard), `weekly`. Gesichert werden Apps mit Datenbank oder Volumes, das erste Mal direkt nach der Installation. Aufbewahrung 30 Tage |

Noch nicht umgesetzt und deshalb nicht im Schema: Backup-Hooks (O-10), `update` (O-09), `provisioning`, `mcp` (M5), Objektspeicher, SMTP.

## Login-Arten

| `auth.mode` | Wann | Was die Plattform tut | Referenz-App |
|---|---|---|---|
| `oidc` | App kann OpenID Connect | Legt in authentik Provider und Application an (implizite Zustimmung, also kein Klick) und ein Secret `oidc` mit Client-ID und -Secret | Forgejo (Chart), Nextcloud (über `user_oidc`, eingerichtet per Start-Hook) |
| `proxy` | App hat kein eigenes Login | Forward-Auth-Provider am eingebetteten Outpost, Traefik-Middleware vor der App; die App bekommt den Nutzer als `X-authentik-*`-Header | Eigenbau-Platzhalter (`traefik/whoami`) |
| `none` | App ist bewusst offen oder verwaltet Nutzer selbst | Nichts | – |

`ldap` ist vorgesehen, aber erst umgesetzt, wenn eine App es braucht.

## Platzhalter

Strings in `source` und `access.service` sind Go-Templates. Ein Platzhalter, den es für die App nicht gibt (zum Beispiel `.database.host` ohne Datenbank), ist ein Fehler und steht im Status. Passwörter gibt es nie als Platzhalter, nur Secret-Namen.

| Platzhalter | Inhalt |
|---|---|
| `.app.name`, `.app.version`, `.app.namespace` | Name, Version, Namespace `app-<name>` |
| `.cell.domain`, `.cell.caConfigMap` | Domain der Zelle; ConfigMap mit `ca.crt` (Zellen-CA) und `ca-certificates.crt` (öffentliche Wurzeln plus Zellen-CA) |
| `.access.host`, `.access.url` | Hostname und `https://`-URL der App |
| `.database.host`, `.port`, `.name`, `.user`, `.secretName` | Datenbank; das Secret hat `username` und `password` |
| `.auth.issuer`, `.auth.discoveryUrl`, `.auth.clientId`, `.auth.secretName` | OIDC-Werte; das Secret hat die Schlüssel aus `secretKeys` |
| `.volumes.<name>.claim` | Name des PersistentVolumeClaims |
| `.secrets.<name>` | Name des generierten Secrets |

## Was der Operator anlegt

In dieser Reihenfolge, jeder Schritt mit eigener Bedingung im Status:

1. Namespace `app-<name>` und ConfigMap `cell-ca`
2. generierte Secrets, Datenbank (`DatabaseReady`), wartet bis sie bereit ist
3. Login-Client in authentik (`LoginReady`); wird bei Spec-Änderung, nach einem Operator-Neustart und alle 10 Minuten neu geschrieben, damit Abweichungen heilen
4. Workload (`WorkloadReady`): Image-Apps als Deployment (Strategie `Recreate`), Service, PVCs; Helm-Apps als Flux `OCIRepository`/`HelmRepository` und `HelmRelease`
5. Ingress mit Zertifikat vom ClusterIssuer `cell`, bei `proxy` zusätzlich Middleware und Outpost-Route
6. Health-Check direkt gegen den Service (`Ready`)

Image-Apps vertrauen der Zellen-CA automatisch (eingehängtes Bundle, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`). Helm-Apps hängen `{{ .cell.caConfigMap }}` über ihre Chart-Werte ein. In lokalen Zellen (`*.localhost`) bekommen Image-Apps zusätzlich die Loopback-Brücke (E-21).

## Status

- `phase`: `Installing` bis zum ersten erfolgreichen Health-Check, dann `Ready`. Bleibt eine App länger als `health.timeoutSeconds` ungesund oder ist ihre letzte Änderung gescheitert, wird sie `NeedsAttention`. Während einer Änderung `Updating`, beim Zurückrollen `RollingBack`, bei einer Wiederherstellung `Restoring`, beim Entfernen `Removing`.
- `version`: Version, mit der der letzte Health-Check bestanden wurde
- `lastBackup`: Name und Zeitpunkt des letzten vollständigen Backups
- `url`: Adresse der App
- `conditions`: `DatabaseReady`, `LoginReady`, `WorkloadReady`, `Ready`, nach einer gescheiterten Änderung `LastChangeSucceeded=False`, jeweils mit lesbarer Begründung

## Tag-2-Betrieb

Neben `App` gibt es drei Ressourcen (`api/v1alpha1/operations_types.go`):

- **`Backup`**: Datenbank (Basissicherung über barman-cloud) und Volumes (restic) einer App im Bucket der Zelle. Entsteht nach Zeitplan, vor jeder Änderung oder auf Anfrage. Wartet, solange eine Änderung oder Wiederherstellung der App läuft, und umgekehrt. Ein laufendes Backup bricht beim Löschen ab: Datenbanksicherung und Job werden gelöscht, restic gibt die Sperre des Repositorys frei, und das Backup hält die App, bis kein Pod des Jobs mehr läuft.
- **`Restore`**: stoppt die App, stellt Volumes und Datenbank aus einem Backup wieder her (Name oder `latest`) und startet sie. Funktioniert auch in einer frischen Zelle, die das Backup nur im Bucket kennt. Beginnt erst, wenn die App eingerichtet ist (Namespace, Volumes) und ihre Backup-Zugangsdaten zum Backup-Ziel der Zelle passen, damit sie die Installation nicht anhält. Eine App ohne Daten (keine Datenbank, keine Volumes, kein Chart) lässt sich nicht wiederherstellen. Vor dem Start konfiguriert der Operator die App für ihre aktuelle Spec (bei Helm-Apps: Flux hat die Release dafür ausgerollt), damit keine Workloads mit einer anderen Konfiguration auf den wiederhergestellten Daten laufen. Gibt Flux dabei auf, scheitert die Wiederherstellung, und die App bleibt angehalten.
- **`ChangeRequest`**: Urheber, Begründung, Risikostufe, Aktion, Freigabe. Die Summe der `ChangeRequest`s ist das Änderungsprotokoll. Jede Aktion wartet auf die Freigabe, die ein Admin über die Plattform-API in einem eigenen Aufruf einträgt (E-30), und sichert die App vorher, wenn sie Daten hat:
  - `update`: JSON Merge Patch auf die App-Spec, Health-Check innerhalb von `health.timeoutSeconds`, bei Fehler zurück zur vorigen Spec und Restore des Backups.
  - `install`: legt die App mit der Spec aus `patch` an und endet mit ihrem ersten Health-Check. Wird sie nicht gesund, scheitert die Änderung, und die App bleibt zur Diagnose installiert. Läuft noch ein `remove` derselben App, wartet die Installation, bis es fertig ist, statt dessen Daten zu übernehmen.
  - `remove`: löscht die App wie im Abschnitt Entfernen beschrieben; mit `deleteData` danach auch ihren Namespace mit Volumes, Datenbank und Secrets. Backups im Bucket bleiben.
  - `restore`: stellt das Backup `backup` wieder her (immer beim Namen) und wartet auf den Health-Check. Wird die App innerhalb von `health.timeoutSeconds` nicht gesund, scheitert die Änderung und gibt die App frei; das Backup vor der Änderung hält den Stand davor.

  Der Name ist höchstens 244 Zeichen lang, weil Backup und Rollback `<name>-pre`, `<name>-rollback` und `<name>-restore` heißen.

## Entfernen

Das Löschen einer `App` entfernt Workload, Ingress und Login-Client. Die Daten bleiben als letzte Sicherung: Volumes bleiben, die Datenbank wird schlafen gelegt (CloudNativePG-Hibernation), der Namespace bekommt das Label `cell.internal/retained=true`. PVCs aus Charts sind per Post-Renderer mit `helm.sh/resource-policy: keep` vor dem Deinstallieren geschützt. Wird die App wieder angelegt, übernimmt sie die Daten. Läuft gerade eine Änderung, Wiederherstellung oder ein Backup, wartet das Entfernen, bis es beendet ist (eine Änderung bricht dabei ihr laufendes Backup ab und gibt die App erst frei, wenn dessen Job gestoppt ist); eine Wiederherstellung, die noch keine Daten ersetzt hat, bricht ab, und neue starten nicht mehr. Endgültig gelöscht werden die Daten nur mit einer freigegebenen Änderung `remove` mit `deleteData` (über die API: `DELETE /v0/apps/{name}?deleteData=true`).

## Zwei Stufen im Katalog

- **Integriert**: gepflegtes Manifest mit Login, geprüften Updates und, wo vorhanden, MCP. Das kuratierte Standard-Set.
- **Generisch**: Manifest aus einem Chart oder einer Compose-Datei abgeleitet, vom Paketier-Agenten erzeugt. Läuft mit Subdomain, Backup und Neustart, aber ohne Garantie für Login und Updates.

## Antworten auf die offenen Punkte aus M1

- **App-spezifische Schritte nach der Installation**: als Dateien im Manifest (`source.image.files`), meist als Start-Hook des Images, der bei jedem Start idempotent läuft (Nextcloud: `user_oidc` installieren und Provider anlegen). Bei Charts über deren eigene Mechanismen (Forgejo legt die OAuth-Quelle im Init-Container an).
- **Platzhalter oder Übersetzungsstück**: Platzhalter reichen für alle drei Apps. Nötig waren zwei kleine Ergänzungen: `secretKeys` (Charts erwarten eigene Schlüsselnamen) und Platzhalter in `access.service` (Service-Name hängt am Release-Namen).
- **Apps aus mehreren Containern ohne Chart**: noch offen; bis eine solche App kommt, ist der Weg ein Helm-Chart. Kandidat für die Paketierung in M4.
