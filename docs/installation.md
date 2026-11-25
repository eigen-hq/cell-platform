# Installation auf eigenen Servern

Was eine Zelle braucht und wie sie auf einen oder mehrere Server kommt.

## Voraussetzungen

### Rechner

| | Mindestens | Empfohlen |
|---|---|---|
| CPU | 2 vCPU | 4 vCPU |
| Arbeitsspeicher | 4 GB (die Plattform allein belegt rund 3 GB) | 8 GB |
| Festplatte | 40 GB | 80 GB, je nach Daten der Apps |
| Architektur | x86_64 | x86_64 (arm64 sollte laufen, ist aber nicht geprüft) |

Weitere Rechner brauchen dasselbe, nur weniger Speicher: Auf ihnen laufen nur Apps.

### Betriebssystem

Ein frisches Ubuntu oder Debian mit Sicherheitsupdates:

| System | Status |
|---|---|
| Ubuntu 24.04 LTS | geprüft |
| Ubuntu 22.04 LTS, 26.04 LTS | sollte laufen |
| Debian 12, 13 | sollte laufen, nicht geprüft |
| Ältere Versionen, andere Distributionen | nicht unterstützt |

Warum nicht älter: Kubernetes startet seit Version 1.35 nur noch mit cgroup v2 (Ubuntu ab 21.10, Debian ab 11), und ein System ohne Sicherheitsupdates kommt für Kundendaten nicht in Frage. Andere Distributionen fehlen, weil die Einrichtung `apt` nutzt.

Alles Weitere installiert die Einrichtung selbst: k3s, die Pakete `curl`, `git`, `openssl`, `perl`, `python3`, für Longhorn `open-iscsi` und `nfs-common`, für den Bau des Plattform-Images Docker. Sie läuft als root.

### Netz

Alle Rechner einer Zelle liegen in einem gemeinsamen Netz, zum Beispiel einem privaten Netz des Anbieters (E-57). Aus dem Internet erreichbar ist nur der erste Rechner, und nur so:

| Port | Wofür |
|---|---|
| 80/TCP | Let's Encrypt prüft hier die Domain; Browser werden auf HTTPS umgeleitet |
| 443/TCP | Konsole, Login, API, alle Apps, Anmeldung und Relay des Mesh |
| 3478/UDP | Geräte im Mesh fragen nach ihrer öffentlichen Adresse, um sich direkt zu verbinden |
| 22/TCP | SSH, nur von der eigenen Adresse (gehört nicht zur Plattform) |

Weitere Rechner brauchen keine offenen Ports. Die Rechner sprechen untereinander nur im gemeinsamen Netz (6443, 10250, 5001/TCP, 51820/UDP); diese Ports dürfen aus dem Internet nicht erreichbar sein. Ausgehend braucht die Zelle das Internet: für Images, Zertifikate, das Sprachmodell und das Backup-Ziel.

Die Ports 80, 443 und 3478 müssen auf der Adresse der Zelle frei sein. Andere Dienste auf dem Rechner stören nicht, solange sie andere Adressen nutzen (E-53).

### Was sonst gebraucht wird

- **Eine Domain**, deren Name und alle Unternamen (`cell.example.com`, `*.cell.example.com`) auf die öffentliche Adresse des ersten Rechners zeigen, und eine E-Mail-Adresse für Let's Encrypt.
- **Ein Backup-Ziel außerhalb der Zelle**, zum Beispiel eine Storage Box (SFTP), ein S3-Bucket oder ein NAS, und ein langes Backup-Passwort, das außerhalb der Zelle aufbewahrt wird. Ohne dieses Passwort lässt sich nichts wiederherstellen.
- **Optional ein Anthropic-Schlüssel** für den Ops-Agenten. Ohne ihn läuft die Plattform, Agenten-Aufgaben warten.

## Installation am Beispiel Hetzner

1. Server anlegen (Ubuntu 24.04), bei mehreren Rechnern ein privates Netz anlegen und alle Server anhängen.
2. Firewall für den ersten Server: eingehend TCP 80, TCP 443 und UDP 3478 von überall, TCP 22 nur von der eigenen Adresse. Weitere Server: eingehend nur TCP 22 von der eigenen Adresse. Ausgehend nichts eintragen (alles erlaubt). Private Netze filtert die Hetzner-Firewall nicht.
3. DNS: A-Einträge für `cell.example.com` und `*.cell.example.com` auf die öffentliche Adresse des ersten Servers.
4. Backup-Ziel: eine `rclone.conf` mit einem Abschnitt `[target]`, zum Beispiel für die Storage Box.
5. Auf dem ersten Server als root:

   ```
   git clone <Repo> /opt/cell && cd /opt/cell
   CELL_DOMAIN=cell.example.com CELL_TLS=acme CELL_ACME_EMAIL=ops@example.com \
   CELL_NODE_IP=<private Adresse> CELL_INGRESS_IP=<öffentliche Adresse> \
   CELL_BACKUP_RCLONE_CONFIG=/root/rclone.conf CELL_BACKUP_RCLONE_ROOT=cell-backups \
   CELL_BACKUP_PASSWORD=<Backup-Passwort> CELL_LLM_API_KEY=<Schlüssel> \
   deploy/release.sh <Release-Tag>
   ```

   Ohne privates Netz entfallen `CELL_NODE_IP` und `CELL_INGRESS_IP`. Die Einrichtung dauert rund 15 Minuten; ein zweiter Lauf ändert nichts. Updates: `deploy/release.sh <neuer Tag>`.
6. Erster Login unter `https://console.cell.example.com` als `akadmin`. Das Passwort:

   ```
   k3s kubectl -n cell-system get secret identity -o jsonpath='{.data.AUTHENTIK_BOOTSTRAP_PASSWORD}' | base64 -d
   ```

7. Weitere Rechner: in der Konsole „Machines", „Add machine", den Befehl auf dem neuen Server als root ausführen.
8. Geräte ins Mesh: Tailscale-App installieren, `tailscale up --login-server=https://mesh.cell.example.com`, im Browser mit dem Zellen-Konto anmelden.
