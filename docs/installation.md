# Zelle installieren

Diese Anleitung richtet eine Zelle auf einem eigenen Server ein. Danach erreichen Sie die Konsole unter `https://console.<Ihre Domain>` und können Apps installieren.

Dauer: rund 30 Minuten, davon etwa 15 Minuten Warten auf die Einrichtung.

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
| Zugang zur Plattform | Adresse des Repositorys und Release-Tag, zum Beispiel `v0.1.0` |
| Backup-Ziel | Ein S3-Bucket außerhalb des Servers (kann nach der Installation folgen) |

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

Melden Sie sich per SSH als root am Server an und führen Sie aus:

```bash
git clone <Repository-Adresse> /opt/cell
cd /opt/cell
deploy/release.sh v0.1.0
```

Die Installation fragt nach der Domain und der E-Mail-Adresse und richtet dann alles ein. Sie prüft vorher, ob der DNS-Eintrag stimmt und die Ports frei sind, und bricht mit einer Erklärung ab, falls nicht.

Am Ende erscheint:

```
==> cell installed in 840s

  Console:  https://console.cell.example.com
  Login:    akadmin / 3f9c...
```

Notieren Sie das Passwort. Es erscheint nur einmal.

## 3. Anmelden

1. Öffnen Sie `https://console.cell.example.com`.
2. Melden Sie sich als `akadmin` mit dem Passwort aus Schritt 2 an.
3. Ändern Sie das Passwort unter `https://auth.cell.example.com`, Benutzereinstellungen.

## 4. Backups einrichten

Ohne Backup-Ziel sichert die Zelle nichts. Richten Sie es ein, bevor Sie Daten in Apps ablegen.

1. Legen Sie einen S3-Bucket an, zum Beispiel in Hetzner Object Storage, und erzeugen Sie Zugangsschlüssel dafür.
2. Führen Sie auf dem Server aus (Werte ersetzen):

   ```bash
   cd /opt/cell
   CELL_BACKUP_S3_ENDPOINT=https://fsn1.your-objectstorage.com \
   CELL_BACKUP_S3_REGION=fsn1 \
   CELL_BACKUP_S3_BUCKET=<Bucket> \
   CELL_BACKUP_S3_ACCESS_KEY=<Access Key> \
   CELL_BACKUP_S3_SECRET_KEY=<Secret Key> \
   deploy/release.sh v0.1.0
   ```

3. Am Ende erscheint das **Backup-Passwort**. Bewahren Sie es außerhalb der Zelle auf, zum Beispiel in einem Passwort-Manager.

> **Wichtig:** Ohne das Backup-Passwort lässt sich kein Backup wiederherstellen. Es erscheint nur dieses eine Mal.

## Weitere Rechner hinzufügen

Voraussetzung: Der neue Rechner hängt im selben privaten Netz wie der erste und erfüllt die [Voraussetzungen](#voraussetzungen). Er braucht keine offenen Ports; als Firewall genügt TCP 22 von Ihrer IP-Adresse.

1. In der Konsole: *Machines*, *Add machine*.
2. Den angezeigten Befehl auf dem neuen Rechner als root ausführen.

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

```bash
cd /opt/cell
deploy/release.sh <neues Release-Tag>
```

Alle Einstellungen bleiben erhalten.

## Wenn etwas nicht klappt

| Meldung oder Verhalten | Ursache und Lösung |
|---|---|
| `*.cell.example.com has no DNS record` | Der DNS-Eintrag fehlt oder ist noch nicht aktiv. Eintrag prüfen, einige Minuten warten, erneut ausführen. |
| `ports 80/443 (tcp) or 3478 (udp) ... are taken` | Ein anderes Programm belegt die Ports, etwa ein Webserver. Es beenden oder einen eigenen Server nutzen. |
| Installation hängt bei den Zertifikaten | Port 80 ist von außen nicht erreichbar. Firewall prüfen. |
| Neuer Rechner erscheint nicht | Er hängt nicht im privaten Netz des ersten Rechners. |
| Startpasswort nicht notiert | Auf dem Server: `k3s kubectl -n cell-system get secret identity -o jsonpath='{.data.AUTHENTIK_BOOTSTRAP_PASSWORD}' \| base64 -d` (gilt, bis Sie es ändern) |
| Backup-Passwort nicht notiert | Auf dem Server: `k3s kubectl -n cell-system get secret backup-target -o jsonpath='{.data.password}' \| base64 -d` |

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

Einstellungen werden als Umgebungsvariablen vor `deploy/release.sh` gesetzt und für spätere Läufe gespeichert.

| Variable | Bedeutung | Standard |
|---|---|---|
| `CELL_DOMAIN` | Domain der Zelle | wird abgefragt |
| `CELL_ACME_EMAIL` | E-Mail für Let's Encrypt | wird abgefragt; leer: selbstsignierte Zertifikate |
| `CELL_INGRESS_IP` | Öffentliche Adresse der Zelle | Adresse der Standardroute |
| `CELL_NODE_IP` | Adresse für den Verkehr zwischen den Rechnern | Adresse im privaten Netz, sonst `CELL_INGRESS_IP` |
| `CELL_BACKUP_S3_*` | Backup-Ziel (`ENDPOINT`, `REGION`, `BUCKET`, `ACCESS_KEY`, `SECRET_KEY`) | kein Backup |
| `CELL_BACKUP_PASSWORD` | Verschlüsselt die Backups | wird erzeugt |
| `CELL_LLM_API_KEY` | Anthropic-Schlüssel für den Ops-Agenten | ohne: Agenten-Aufgaben warten |
