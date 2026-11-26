# Install a cell

This guide sets up a cell on your own server. Afterwards you can reach the console at `https://console.<your domain>` and install apps.

Duration: about 30 minutes, of which about 20 minutes are spent waiting for the setup.

## Overview

1. [Check the requirements](#requirements)
2. [Prepare the server](#1-prepare-the-server)
3. [Install the cell](#2-install-the-cell)
4. [Sign in](#3-sign-in)
5. [Set up backups](#4-set-up-backups)

Afterwards, as needed: [more machines](#add-more-machines), [devices in the mesh](#connect-devices).

## Requirements

| | Required |
|---|---|
| Server | Freshly installed, with root access over SSH |
| Operating system | Ubuntu 24.04 LTS (recommended) |
| Processor | x86_64, 4 cores (at least 2) |
| Memory | 8 GB (at least 4) |
| Disk | 80 GB (at least 40) |
| Domain | A domain or subdomain for which you can set DNS records, for example `cell.example.com` |
| Email address | For the Let's Encrypt certificates |
| Backup target | An S3 bucket outside the server, for example Hetzner Object Storage. You set it up in the console after the installation. |

> **Other systems:** Ubuntu 22.04 LTS and Debian 12 or 13 should also work, but are not tested yet. Older versions no longer receive security updates and are not supported.

## 1. Prepare the server

The examples show the Hetzner Cloud Console; with other providers the settings have similar names.

1. **Create a server** with Ubuntu 24.04.

   Do you plan to use more than one machine? Create a private network under *Networks* and attach the server to it **before** you install. The installation detects the network by itself.

2. **Create a firewall** (*Firewalls*, *Create Firewall*) and assign it to the server:

   | Direction | Protocol | Port | Source |
   |---|---|---|---|
   | Inbound | TCP | 80 | Anywhere |
   | Inbound | TCP | 443 | Anywhere |
   | Inbound | UDP | 3478 | Anywhere |
   | Inbound | TCP | 22 | Only your own IP address |

   Do not add outbound rules: then outbound traffic is allowed, which the cell needs.

3. **Set a DNS record** at your domain provider:

   | Name | Type | Value |
   |---|---|---|
   | `*.cell.example.com` | A | Public IPv4 address of the server |

   One record is enough: all services of the cell live under this domain, for example `console.cell.example.com`.

## 2. Install the cell

Log in to the server over SSH and run:

```bash
curl -fsSL https://get.eigen-platform.com | sudo bash
```

The command downloads the latest release and asks for the domain and the email address. Before it sets anything up, it checks the DNS record and the ports and stops with an explanation if something is missing.

At the end, this appears:

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

Write down both values, for example in a password manager. They appear only this once.

> **Important:** Without the backup key, no backup can be restored into a new cell.

## 3. Sign in

1. Open `https://console.cell.example.com`.
2. Sign in as `akadmin` with the password from step 2.
3. Change the password at `https://auth.cell.example.com`, user settings.

## 4. Set up backups

Without a backup target, the cell backs up nothing. Set it up before you put data into apps.

1. Create an S3 bucket, for example in Hetzner Object Storage, and generate access keys for it.
2. In the console, open *Backups* and enter provider, bucket and keys.
3. As a test, the cell creates a file in the bucket and deletes it again. If that works, the target applies to all apps from now on. If it does not, everything stays as it was, and the console shows the reason.

You can also set the target without the console, for example from a script or by an AI agent with an admin token: see [Backup target through the API](#backup-target-through-the-api).

## Add more machines

Prerequisite: the new machine meets the [requirements](#requirements) (x86_64 or arm64). In the same private network as the first one, it needs no open ports. Without a private network, the machines talk over their public addresses; then open TCP 6443, 10250, 5001 and UDP 51820 on every machine, **only for the addresses of the other machines**.

1. In the console: *Machines*, *Add machine*.
2. Run the command shown on the new machine.

The machine appears in the console after a few minutes.

## Connect devices

Through the mesh, staff members reach the cell from their devices over an encrypted connection.

1. Install Tailscale: [tailscale.com/download](https://tailscale.com/download).
2. Run on the device:

   ```bash
   tailscale up --login-server=https://mesh.cell.example.com
   ```

3. Sign in in the browser with the cell account.

Administrators and members of the group `cell-mesh` are allowed. You manage groups at `https://auth.cell.example.com/if/admin/`.

## Updates

The same command as for the installation fetches the latest release:

```bash
curl -fsSL https://get.eigen-platform.com | sudo bash
```

All settings and data are kept. You install a specific release with `CELL_VERSION`:

```bash
curl -fsSL https://get.eigen-platform.com | sudo CELL_VERSION=v0.2.0 bash
```

## If something goes wrong

If an error occurs, the installation shows the failed step, the reason and the last lines of the log. The full log is in `/var/log/cell-install.log`.

| Message or behavior | Cause and solution |
|---|---|
| `*.cell.example.com has no DNS record` | The DNS record is missing or not active yet. Check the record, wait a few minutes, run again. |
| `ports 80/443 (tcp) or 3478 (udp) ... are taken` | Another program is using the ports, for example a web server. Stop it or use a dedicated server. |
| `Let's Encrypt could not check auth...` (step *Login page*) | Port 80 does not reach the server from the internet, so Let's Encrypt cannot verify the domain. Check the provider's firewall and the server's own (ufw, nftables rules that drop forwarded traffic), then run the installation again: it requests the certificate at once. |
| New machine does not appear | It is not in the private network of the first machine, or, over public addresses, the firewall blocks the ports between the machines. |
| Backup target: test fails | The console gives the reason, usually a wrong key, a wrong bucket name or a read-only key. |
| Initial password not written down | On the server: `sudo k3s kubectl -n cell-system get secret identity -o jsonpath='{.data.AUTHENTIK_BOOTSTRAP_PASSWORD}' \| base64 -d` (valid until you change it) |
| Backup key not written down | On the server: `sudo k3s kubectl -n cell-system get secret backup-target -o jsonpath='{.data.password}' \| base64 -d` |

You can run the installation again at any time; it changes only what is missing.

## Reference

### Ports

| Port | From | Purpose |
|---|---|---|
| 80/TCP | Internet | Let's Encrypt verifies the domain; redirect to HTTPS |
| 443/TCP | Internet | Console, sign-in, API, all apps, mesh |
| 3478/UDP | Internet | Mesh: direct connections between devices and cell |
| 6443, 10250, 5001/TCP, 51820/UDP | Other machines of the cell only | Traffic between the machines (private network or public addresses). Never open to the internet. |

### Settings

For an installation without prompts, for example from a script, set settings as environment variables before `bash`:

```bash
curl -fsSL https://get.eigen-platform.com |
  sudo CELL_DOMAIN=cell.example.com CELL_ACME_EMAIL=ops@example.com bash
```

The installation saves them in `/etc/cell/install.env`; later runs do not need them again.

| Variable | Meaning | Default |
|---|---|---|
| `CELL_VERSION` | Release that is installed | the latest |
| `CELL_DOMAIN` | Domain of the cell | asked for |
| `CELL_ACME_EMAIL` | Email for Let's Encrypt | asked for; empty: self-signed certificates |
| `CELL_INGRESS_IP` | Public address of the cell | address of the default route |
| `CELL_NODE_IP` | Address for traffic between the machines | address in the private network, otherwise `CELL_INGRESS_IP` |
| `CELL_BACKUP_S3_*` | Backup target already at installation (`ENDPOINT`, `REGION`, `BUCKET`, `ACCESS_KEY`, `SECRET_KEY`); on a later run another target goes through the same check and switch as one set in the console, and the run waits for it | none, set it up in the console |
| `CELL_BACKUP_S3_PRIVATE_NETWORK` | `true` allows an `http://` endpoint, only with a private IP address as its host (no host name); otherwise the endpoint must be `https://` | `false` |
| `CELL_BACKUP_PASSWORD` | Backup key; a later run with another one stops, the key encrypts the backups | generated |
| `CELL_LLM_API_KEY` | Anthropic key for the ops agent | without it: agent tasks wait |

### Backup target through the API

The console uses the same interface as scripts and AI agents. An admin creates a token for their account in the console under *Organization* and gives it to an agent, for example. The token is exchanged for an access token, which is used to set the target:

```bash
curl -fsS https://auth.cell.example.com/application/o/token/ \
  -d grant_type=client_credentials -d client_id=cell-api -d 'scope=openid profile' \
  -d username=akadmin -d password=<token>
# access_token from the response:
curl -fsS -X PUT https://api.cell.example.com/v0/backup-target \
  -H 'Authorization: Bearer <access_token>' -H 'Content-Type: application/json' \
  -d '{"endpoint": "https://fsn1.your-objectstorage.com", "region": "fsn1",
       "bucket": "<bucket>", "accessKey": "<access key>", "secretKey": "<secret key>"}'
```

`GET /v0/backup-target` shows the target and the result of the test. The full description of the API is at `https://api.cell.example.com/openapi.yaml`.
