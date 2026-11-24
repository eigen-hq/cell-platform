# Backlog

Alle Features in einer Liste, ohne Phasen (E-37). Die Reihenfolge innerhalb eines Bereichs folgt den Abhängigkeiten.

## Die Grenze

- **Plattform und Konsole: technischer Betrieb.** Apps ausführen, überwachen, sichern, prüfen und freigeben. Hier sieht die IT, was in der Zelle passiert, und entscheidet über alles, was für mehr als eine Person gilt.
- **Office-Oberfläche: die Arbeit.** Alle Mitarbeiter nutzen dort die Apps, fragen neue an, wünschen sich Features und probieren sie zuerst für sich allein aus. Die Office-Oberfläche liegt beim Mitgründer, in eigenen Repos.
- **Die Naht:** Anfragen aus der Office-Oberfläche kommen über die Plattform-API an. Was nur eine Person betrifft, läuft eingeschränkt ohne Freigabe. Was für alle gilt, geht durch die Freigabe der IT in der Konsole. Die Plattform setzt durch, welche App welche Daten sehen darf.

Status:

- **läuft** – Logik und Oberfläche sind da (`stand.md`)
- **API läuft** – die Logik ist da (Plattform-API); die Oberfläche fehlt noch
- **offen** – Logik und Oberfläche entstehen gemeinsam, sobald das Feature dran ist

Was die Konsole zeigt, kommt aus der API. Ohne Endpunkt gibt es keine Oberfläche, auch keine mit Beispieldaten.

## Plattform und Konsole

### Grundgerüst

| Feature | Status | Hängt ab von |
|---|---|---|
| Konsole im Browser, Anmeldung über die Identität | läuft | |
| Übersicht: Zustand der Zelle, offene Freigaben, letzte Ereignisse | läuft | |
| Live-Ereignisse | API läuft | |
| Helle und dunkle Tonlage | läuft | |
| Erste Einrichtung einer neuen Zelle | offen | |

### Freigaben

| Feature | Status | Hängt ab von |
|---|---|---|
| Änderungen freigeben und ablehnen | läuft | |
| Anfragen aus der Office-Oberfläche freigeben (neue App, Feature für alle) | offen | Anfragen-API |
| Vor der Freigabe sehen, welche Daten und Rechte eine App bekommt | offen | Datenzugriff je App |
| Änderungen nachbessern, Vorschau, Rückgängig mit einem Aufruf | offen | |
| Vier-Augen-Prinzip | offen | O-12 |
| Freigabe-Richtlinie (was automatisch laufen darf) | offen | |
| Update-Richtlinie | offen | O-09 |
| Rollen in der Konsole: Leitungen geben für ihre Abteilung frei, Prüfer lesen nur | offen | |

### Apps im Betrieb

| Feature | Status | Hängt ab von |
|---|---|---|
| Installierte Apps mit Zustand, Version, Adresse | läuft | |
| Installieren durch die IT: Katalog, eigenes Manifest | läuft (Katalog); Manifest: API läuft | |
| Paketieren eines Open-Source-Projekts durch einen Agenten (Compose) | API läuft | Helm-Charts: O-15 |
| Update und Entfernen | Entfernen läuft; Update: API läuft | O-09 |
| Datenzugriff je App durchsetzen: welche Personen, welche Daten | offen | |
| Erreichbarkeit je App (öffentlich, hinter Anmeldung, nur Firmennetz) | offen | O-05 |
| Stufen der Unterstützung (eingebaut, integriert, generisch, Eigenbau, angepasst) | offen | |
| Dienste der Zelle (Anmeldung, Eingang, Datenbanken, Speicher, Tool-Gateway, System-Mail) | offen | |
| Technische Aktionen in einer App durch einen Agenten (z. B. in authentik einrichten) | offen | O-14, O-18 |

### Angepasste Apps, technische Seite

Gewünscht und ausprobiert wird in der Office-Oberfläche; die Plattform baut, betreibt und gibt frei.

| Feature | Status | Hängt ab von |
|---|---|---|
| Build-Pipeline für Eigenbau und angepasste Apps | offen | |
| Persönliche Testversion: eine angepasste App nur für eine Person, eingeschränkt, ohne Freigabe | offen | Build-Pipeline, Datenzugriff je App |
| Übernahme für alle nach Freigabe der IT | offen | |
| Anpassungen bei jedem Update neu anwenden | offen | |

### Überwachung und Observability

| Feature | Status | Hängt ab von |
|---|---|---|
| Diagnose einer App: Pods, Logs, Ereignisse | Ereignisse laufen; Pods, Logs: API läuft | |
| Diagnose durch den Ops-Agenten | läuft | |
| Metriken: CPU, RAM, Platte je App und Zelle, Verlauf | offen | Metrik-Baustein |
| Verfügbarkeit und Antwortzeiten je App | offen | |
| Landkarte: Verdrahtung, Verkehr, Verbindungen nach außen, Agenten | offen | Metriken |
| Befunde mit Agenten-Aktion (z. B. ungenutzte oder doppelte Apps) | offen | Nutzungszahlen |
| Warnungen und Benachrichtigungen an die IT | offen | System-Mail |
| Kapazität: wann die Zelle wachsen muss | offen | Metriken |

### Sicherungen und Daten

| Feature | Status | Hängt ab von |
|---|---|---|
| Sicherungen je App, Sicherung jetzt anstoßen | läuft | |
| Wiederherstellen über den aktuellen Stand | läuft | |
| Wiederherstellen als Kopie | offen | |
| Backup-Ziel ändern | offen | |
| Wiederherstellungstest | offen | |
| Export aller Daten und Umzug auf eigene Infrastruktur | offen | |

### Prüfung und Sicherheit

| Feature | Status | Hängt ab von |
|---|---|---|
| Änderungsverlauf mit Urheber und Ergebnis | läuft | |
| Prüfprotokoll mit Export | Export des Änderungsverlaufs (CSV) läuft; Prüfprotokoll offen | |
| Sicherheitsübersicht: zweiter Faktor, verwaiste Konten, Admins | offen | |
| Was an Modellanbieter geht | offen | O-17 |
| Wer kann was öffnen, was verlässt die Zelle | offen | Datenzugriff je App |
| Eskalation an menschlichen Support (nur mit Zustimmung, befristet, protokolliert) | offen | |

### Identität

| Feature | Status | Hängt ab von |
|---|---|---|
| Konten und Gruppen, Mitglieder, App-Passwort | läuft | |
| Zugriff je App über Gruppen | läuft | |
| Abteilungen und Teams als Struktur der Identität | offen | |
| Personen-Änderungen mit Freigabe, Person entfernen | offen | |
| Konto-Hilfe: Passwort, zweiter Faktor, Sperre, Sitzungen | offen | |
| Anmelde-Richtlinie | offen | |

### Agenten

| Feature | Status | Hängt ab von |
|---|---|---|
| Aufgaben der Agenten mit allen Schritten | läuft | |
| Agenten als Konten mit Rechten je App | offen | |
| Autonomie-Stufen | offen | |
| Tool-Gateway für alle Apps | API läuft | O-18, O-19, O-20 |

### Modelle

| Feature | Status | Hängt ab von |
|---|---|---|
| Modelle in der Zelle betreiben | offen | |
| Modelle über den eigenen Schlüssel | offen | O-16 |
| Nutzung und Kosten, Budget und Grenzen je Agent oder Abteilung | offen | |

### Zelle

| Feature | Status | Hängt ab von |
|---|---|---|
| Zelle und Domain | offen | |
| System-Mail der Zelle (nur Versand: Einladungen, Passwort-Links, Warnungen) | offen | |

### Verteilte Zelle

Ein Cluster über alle Rechner, WireGuard dazwischen (E-42); Netz für Menschen über ein eigenes Mesh (E-43); Rechner kommen über die Konsole hinzu (E-44). Läuft (`stand.md`): Rechner mit Zustand, Beitrittsbefehl aus der Konsole, Hardware-Auslastung je Rechner mit seinen Apps, WireGuard zwischen den Rechnern. Der Rest ist offen.

- Rechner und Standorte einer Zelle mit Zustand
- Rechner hinzufügen: Beitrittsbefehl aus der Konsole (E-44)
- Rechner hinzufügen per SSH aus der Konsole, auf dem Beitrittsbefehl aufbauend
- Rechner entfernen (Apps und Volumes ziehen vorher um)
- Platzierung: welche App wo läuft, App an einen Standort binden
- Hardware-Auslastung je Rechner und App
- Kapazität und Umverteilung (Agent schlägt Verschieben oder mehr Hardware vor)
- Ausfall eines Rechners oder Standorts: was weiterläuft, was umzieht
- Latenz je Standort
- Verbindung zwischen den Rechnern (WireGuard, E-42)
- Zugang für Menschen und Anbindung ans Firmennetz (Mesh, E-43)

### Naht zur Office-Oberfläche

| Feature | Status | Hängt ab von |
|---|---|---|
| Anfragen-API: neue App, Feature-Wunsch, persönliche Testversion, mit Personenkreis und Datenzugriff; Status für die Anfragenden | offen | O-27 |
| Ereignisse für die Office-Oberfläche (Anfrage freigegeben, App bereit) | offen | O-27 |
| Gemeinsame Anmeldung | offen | O-25 |
| Office-Oberfläche als App der Zelle | offen | O-26 |

### Selbst betreiben

| Feature | Status | Hängt ab von |
|---|---|---|
| Zelle über beide Hetzner-VPS | offen | Rechner hinzufügen |
| Update der Plattform auf einen Release-Tag (Build auf dem Server aus Git) | offen | |
| Git-Hosting und Issues in der eigenen Zelle, Backlog als Issues | offen | Zelle auf dem VPS |

## Gehört zur Office-Oberfläche

Hier steht nur, was die Plattform dafür liefert. Planung und Umsetzung liegen beim Mitgründer.

| Feature | Was die Plattform liefert |
|---|---|
| Apps starten, die man nutzen darf | Liste der Apps je Person (API läuft) |
| Neue App anfragen, für bestimmte Personen, mit bestimmtem Datenzugriff | Anfragen-API, Freigabe in der Konsole |
| Feature für eine App wünschen | Anfragen-API, Build-Pipeline |
| Gewünschtes Feature zuerst nur für sich ausprobieren | Persönliche Testversion |
| Aufträge an Agenten in eigenen Worten | Aufgaben über die API |
| Geführte Abläufe: jemand kommt dazu, jemand geht, ein Team braucht ein Tool, ein Team anlegen, einer Abteilung eine App geben, etwas ist kaputt, etwas zurückholen | Anfragen-API; Ausführung und Freigabe in der Plattform |
| Eigenen Assistenten verbinden | Tool-Gateway, Tokens |
| Erscheinungsbild je Kunde | – |
| Mail, Postfächer, zentraler Starter für alle Tools | – |

## Bewusst draußen

- Migration bestehender IT-Landschaften
- Abrechnung, zentraler Dienst über mehrere Zellen
- Geräteverwaltung

## Design

- Brand-Look dunkel: https://claude.ai/artifact/URWKXNc7sU4avqh8JWoLWA
- Brand-Look hell: https://claude.ai/artifact/GpypdMvouCnaXyF9wC3Xst
- Figma „Platform Console 02": https://www.figma.com/design/J1s8hrkim2aNxKgzxEaSe6

Die beiden Brand-Looks und die Figma-Datei zeigen die neue Grenze (je 31 Frames in „Screens - Dark" und „Screens - Light"). Neue Teile tragen dort eine Lücken-Markierung, solange sie hier offen sind.
