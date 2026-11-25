# Zelle installieren

Diese Anleitung richtet eine Zelle auf einem eigenen Server ein. Danach erreichen Sie die Konsole unter `https://console.<Ihre Domain>` und können Apps installieren.

Dauer: rund 30 Minuten, davon etwa 20 Minuten Warten auf die Einrichtung.

## Überblick

1. [Voraussetzungen prüfen](#voraussetzungen)
2. [Server vorbereiten](#1-server-vorbereiten)
3. [Zelle installieren](#2-zelle-installieren)
4. [Anmelden](#3-anmelden)
5. [Backups einrichten](#4-backups-einrichten)

Danach nach Bedarf: [weitere Rechner](#weitere-rechner-hinzufügen), [Geräte ins Mesh](#geräte-verbinden).

## Voraussetzungen

| | Benötigt |
|---|---|
| Server | Frisch installiert, mit root-Zugang per SSH |
| Betriebssystem | Ubuntu 24.04 LTS (empfohlen) |
| Prozessor | x86_64, 4 Kerne (mindestens 2) |
| Arbeitsspeicher | 8 GB (mindestens 4) |
| Festplatte | 80 GB (mindestens 40) |
| Domain | Eine Domain oder Subdomain, für die Sie DNS-Einträge setzen können, zum Beispiel `cell.example.com` |
| E-Mail-Adresse | Für die Zertifikate von Let's Encrypt |
| Backup-Ziel | Ein S3-Bucket außerhalb des Servers, zum Beispiel Hetzner Object Storage. Sie richten es nach der Installation in der Konsole ein. |

> **Andere Systeme:** Ubuntu 22.04 LTS und Debian 12 oder 13 sollten ebenfalls funktionieren, sind aber noch nicht getestet. Ältere Versionen erhalten keine Sicherheitsupdates mehr und werden nicht unterstützt.

## 1. Server vorbereiten

Die Beispiele zeigen die Hetzner Cloud Console; bei anderen Anbietern heißen die Einstellungen ähnlich.

1. **Server erstellen** mit Ubuntu 24.04.

   Planen Sie mehr als einen Rechner? Legen Sie unter *Networks* ein privates Netz an und hängen Sie den Server an, **bevor** Sie installieren. Die Installation erkennt das Netz selbst.

2. **Firewall anlegen** (*Firewalls*, *Create Firewall*) und dem Server zuweisen:

   | Richtung | Protokoll | Port | Quelle |
   |---|---|---|---|
   | Eingehend | TCP | 80 | Überall |
   | Eingehend | TCP | 443 | Überall |
   | Eingehend | UDP | 3478 | Überall |
   | Eingehend | TCP | 22 | Nur Ihre eigene IP-Adresse |

   Ausgehende Regeln nicht eintragen: Dann ist ausgehender Verkehr erlaubt, den die Zelle braucht.

3. **DNS-Eintrag setzen** bei Ihrem Domain-Anbieter:

   | Name | Typ | Wert |
   |---|---|---|
   | `*.cell.example.com` | A | Öffentliche IPv4-Adresse des Servers |

   Ein Eintrag genügt: Alle Dienste der Zelle liegen unter dieser Domain, etwa `console.cell.example.com`.

## 2. Zelle installieren

Melden Sie sich per SSH am Server an und führen Sie aus:

```bash
curl -fsSL https://raw.githubusercontent.com/marcelheidebrecht/cell-platform/main/get.sh | sudo bash
```

Der Befehl lädt das neueste Release und fragt nach der Domain und der E-Mail-Adresse. Bevor er etwas einrichtet, prüft er den DNS-Eintrag und die Ports und bricht mit einer Erklärung ab, falls etwas fehlt.

Am Ende erscheint:

```
  ✓ Cell ready in 18m 40s

  Console     https://console.cell.example.com
  User        akadmin
  Password    3f9c...
              Shown only now. Change it after the first login.
  Backups     not set up yet: add a target in the console under Backups
  Backup key  9a1b...
              Shown only now. Keep it outside the cell: a restore into a new cell needs it.
```

Notieren Sie beide Werte, etwa in einem Passwort-Manager. Sie erscheinen nur dieses eine Mal.

> **Wichtig:** Ohne den Backup-Schlüssel lässt sich kein Backup in einer neuen Zelle wiederherstellen.

## 3. Anmelden

1. Öffnen Sie `https://console.cell.example.com`.
2. Melden Sie sich als `akadmin` mit dem Passwort aus Schritt 2 an.
3. Ändern Sie das Passwort unter `https://auth.cell.example.com`, Benutzereinstellungen.

## 4. Backups einrichten

Ohne Backup-Ziel sichert die Zelle nichts. Richten Sie es ein, bevor Sie Daten in Apps ablegen.

1. Legen Sie einen S3-Bucket an, zum Beispiel in Hetzner Object Storage, und erzeugen Sie Zugangsschlüssel dafür.
2. Öffnen Sie in der Konsole *Backups* und tragen Sie Anbieter, Bucket und Schlüssel ein.
3. Die Zelle legt zur Probe eine Datei im Bucket an und löscht sie wieder. Klappt das, gilt das Ziel ab sofort für alle Apps. Klappt es nicht, bleibt alles, wie es war, und die Konsole zeigt den Grund.

Das Ziel lässt sich auch ohne Konsole setzen, etwa aus einem Skript oder durch einen KI-Agenten mit einem Admin-Token: siehe [Backup-Ziel per API](#backup-ziel-per-api).

## Weitere Rechner hinzufügen

Voraussetzung: Der neue Rechner hängt im selben privaten Netz wie der erste und erfüllt die [Voraussetzungen](#voraussetzungen). Er braucht keine offenen Ports; als Firewall genügt TCP 22 von Ihrer IP-Adresse.

1. In der Konsole: *Machines*, *Add machine*.
2. Den angezeigten Befehl auf dem neuen Rechner ausführen.

Der Rechner erscheint nach wenigen Minuten in der Konsole.

## Geräte verbinden

Über das Mesh erreichen Mitarbeitende die Zelle verschlüsselt von ihren Geräten aus.

1. Tailscale installieren: [tailscale.com/download](https://tailscale.com/download).
2. Auf dem Gerät ausführen:

   ```bash
   tailscale up --login-server=https://mesh.cell.example.com
   ```

3. Im Browser mit dem Konto der Zelle anmelden.

Erlaubt sind Administratoren und Mitglieder der Gruppe `cell-mesh`. Gruppen verwalten Sie unter `https://auth.cell.example.com/if/admin/`.

## Updates

Derselbe Befehl wie bei der Installation holt das neueste Release:

```bash
curl -fsSL https://raw.githubusercontent.com/marcelheidebrecht/cell-platform/main/get.sh | sudo bash
```

Alle Einstellungen und Daten bleiben erhalten. Ein bestimmtes Release installieren Sie mit `CELL_VERSION`:

```bash
curl -fsSL https://raw.githubusercontent.com/marcelheidebrecht/cell-platform/main/get.sh | sudo CELL_VERSION=v0.2.0 bash
```

## Wenn etwas nicht klappt

Die Installation zeigt bei einem Fehler den fehlgeschlagenen Schritt, den Grund und die letzten Zeilen des Protokolls. Das vollständige Protokoll liegt in `/var/log/cell-install.log`.

| Meldung oder Verhalten | Ursache und Lösung |
|---|---|
| `*.cell.example.com has no DNS record` | Der DNS-Eintrag fehlt oder ist noch nicht aktiv. Eintrag prüfen, einige Minuten warten, erneut ausführen. |
| `ports 80/443 (tcp) or 3478 (udp) ... are taken` | Ein anderes Programm belegt die Ports, etwa ein Webserver. Es beenden oder einen eigenen Server nutzen. |
| Schritt *Login page* dauert länger als 10 Minuten | Port 80 ist von außen nicht erreichbar, Let's Encrypt kann die Domain nicht prüfen. Firewall prüfen. |
| Neuer Rechner erscheint nicht | Er hängt nicht im privaten Netz des ersten Rechners. |
| Backup-Ziel: Probe schlägt fehl | Die Konsole nennt den Grund, meist ein falscher Schlüssel, ein falscher Bucket-Name oder ein Schlüssel nur zum Lesen. |
| Startpasswort nicht notiert | Auf dem Server: `sudo k3s kubectl -n cell-system get secret identity -o jsonpath='{.data.AUTHENTIK_BOOTSTRAP_PASSWORD}' \| base64 -d` (gilt, bis Sie es ändern) |
| Backup-Schlüssel nicht notiert | Auf dem Server: `sudo k3s kubectl -n cell-system get secret backup-target -o jsonpath='{.data.password}' \| base64 -d` |

Die Installation lässt sich jederzeit erneut ausführen; sie ändert nur, was fehlt.

## Referenz

### Ports

| Port | Von | Zweck |
|---|---|---|
| 80/TCP | Internet | Let's Encrypt prüft die Domain; Weiterleitung auf HTTPS |
| 443/TCP | Internet | Konsole, Anmeldung, API, alle Apps, Mesh |
| 3478/UDP | Internet | Mesh: direkte Verbindungen zwischen Geräten und Zelle |
| 6443, 10250, 5001/TCP, 51820/UDP | Nur privates Netz | Verkehr zwischen den Rechnern der Zelle. Nie aus dem Internet öffnen. |

### Einstellungen

Für eine Installation ohne Rückfragen, etwa aus einem Skript, setzen Sie Einstellungen als Umgebungsvariablen vor `bash`:

```bash
curl -fsSL https://raw.githubusercontent.com/marcelheidebrecht/cell-platform/main/get.sh |
  sudo CELL_DOMAIN=cell.example.com CELL_ACME_EMAIL=ops@example.com bash
```

Die Installation speichert sie in `/etc/cell/install.env`; spätere Läufe brauchen sie nicht noch einmal.

| Variable | Bedeutung | Standard |
|---|---|---|
| `CELL_VERSION` | Release, das installiert wird | das neueste |
| `CELL_DOMAIN` | Domain der Zelle | wird abgefragt |
| `CELL_ACME_EMAIL` | E-Mail für Let's Encrypt | wird abgefragt; leer: selbstsignierte Zertifikate |
| `CELL_INGRESS_IP` | Öffentliche Adresse der Zelle | Adresse der Standardroute |
| `CELL_NODE_IP` | Adresse für den Verkehr zwischen den Rechnern | Adresse im privaten Netz, sonst `CELL_INGRESS_IP` |
| `CELL_BACKUP_S3_*` | Backup-Ziel schon bei der Installation (`ENDPOINT`, `REGION`, `BUCKET`, `ACCESS_KEY`, `SECRET_KEY`) | keins, in der Konsole einrichten |
| `CELL_BACKUP_PASSWORD` | Backup-Schlüssel | wird erzeugt |
| `CELL_LLM_API_KEY` | Anthropic-Schlüssel für den Ops-Agenten | ohne: Agenten-Aufgaben warten |

### Backup-Ziel per API

Die Konsole nutzt dieselbe Schnittstelle wie Skripte und KI-Agenten. Ein Admin erzeugt in der Konsole unter *Organization* ein Token für sein Konto und gibt es zum Beispiel einem Agenten. Das Token wird gegen ein Zugriffstoken getauscht, mit dem das Ziel gesetzt wird:

```bash
curl -fsS https://auth.cell.example.com/application/o/token/ \
  -d grant_type=client_credentials -d client_id=cell-api -d 'scope=openid profile' \
  -d username=akadmin -d password=<Token>
# access_token aus der Antwort:
curl -fsS -X PUT https://api.cell.example.com/v0/backup-target \
  -H 'Authorization: Bearer <access_token>' -H 'Content-Type: application/json' \
  -d '{"endpoint": "https://fsn1.your-objectstorage.com", "region": "fsn1",
       "bucket": "<Bucket>", "accessKey": "<Access Key>", "secretKey": "<Secret Key>"}'
```

`GET /v0/backup-target` zeigt das Ziel und das Ergebnis der Probe. Die vollständige Beschreibung der API liegt unter `https://api.cell.example.com/openapi.yaml`.
