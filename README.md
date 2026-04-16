# Mnemosyne

A self-hosted web application that pulls emails from IMAP servers and makes them searchable. Back up your email to your own hardware, search it with Gmail-style operators, and export it in standard formats.

## Features

- **IMAP backup** — Connect any number of IMAP accounts, select folders to back up, and sync on a schedule or manually. Messages are stored exactly once via content-addressed dedup (SHA-256). Optional per-account SOCKS5 proxy for accounts that aren't reachable directly.
- **Full-text search** — Gmail-style query syntax: `from:alice subject:"quarterly report" budget has:attachment before:2026-01-01`. Powered by SQLite FTS5.
- **Attachment text extraction** — PDF (via pdftotext), DOCX, HTML, and plain text attachments are indexed so you can search their contents.
- **Retention policies** — Keep all messages on the server, keep the N newest, or keep messages younger than N days. Messages are only deleted from the upstream server after the local backup is confirmed durable.
- **Export** — Download messages as mbox, Maildir (tar), or upload directly to another IMAP server.
- **Multi-user** — Each user sees only their own accounts, messages, and search results. Isolation is enforced at every database query.
- **Single binary** — One Go binary, one SQLite database, one data directory. No external services required.

## Quickstart

### Using Docker (recommended)

```bash
docker run -d \
  --name mnemosyne \
  -p 8080:8080 \
  -v /path/to/data:/var/lib/mnemosyne \
  ghcr.io/hjiang/mnemosyne:latest

# Create your first user
docker exec -it mnemosyne mnemosyne adduser you@example.com
```

Open `http://localhost:8080` and log in.

### From source (requires Nix)

```bash
git clone https://github.com/hjiang/mnemosyne.git
cd mnemosyne
nix develop                        # enter dev shell
go run ./cmd/mnemosyne adduser you@example.com
go run ./cmd/mnemosyne serve       # http://localhost:8080
```

## Deploying to Unraid

### 1. Install the container

Open the Unraid web UI and go to **Docker > Add Container** (or click the terminal icon to use the CLI).

**Using the Unraid Docker UI:**

| Field | Value |
|---|---|
| Name | `mnemosyne` |
| Repository | `ghcr.io/hjiang/mnemosyne:latest` |
| Network Type | `bridge` |
| Port Mapping | Host: `8080` → Container: `8080` (or pick any free host port) |
| Path Mapping | Host: `/mnt/user/appdata/mnemosyne` → Container: `/var/lib/mnemosyne` |

Click **Apply** to create the container.

**Using the CLI:**

```bash
docker run -d \
  --name mnemosyne \
  --restart unless-stopped \
  -p 8080:8080 \
  -v /mnt/user/appdata/mnemosyne:/var/lib/mnemosyne \
  ghcr.io/hjiang/mnemosyne:latest
```

### 2. Create a user

```bash
docker exec -it mnemosyne mnemosyne adduser you@example.com
```

Enter a password when prompted. You can create multiple users.

### 3. Log in and add your email accounts

1. Open `http://<your-unraid-ip>:8080` in a browser.
2. Log in with the email and password you just created.
3. Go to **Accounts** and add your IMAP server details:
   - **Host**: your mail server (e.g., `imap.gmail.com`)
   - **Port**: `993` for TLS, `143` for plain
   - **Username**: your email address
   - **Password**: your email password (or app-specific password for Gmail)
   - **Use TLS**: check for port 993
   - **SOCKS5 Proxy** (optional): host, port, and optional credentials for accounts that need to be reached through a proxy (e.g., region-locked providers, jump hosts). Leave blank for a direct connection.
4. Click **Manage folders** to select which folders to back up.
5. Click **Backup now** to run the first sync.

You can later use **Edit** on the Accounts page to update any of these fields (password and proxy password are kept unchanged when their inputs are left blank).

### 4. Optional: reverse proxy with HTTPS

If you use Nginx Proxy Manager or Traefik on Unraid, point a subdomain at `mnemosyne:8080`. Set the `MNEMOSYNE_BASE_URL` environment variable to your public URL:

```bash
docker run -d \
  --name mnemosyne \
  --restart unless-stopped \
  -p 8080:8080 \
  -v /mnt/user/appdata/mnemosyne:/var/lib/mnemosyne \
  -e MNEMOSYNE_BASE_URL=https://mail.example.com \
  ghcr.io/hjiang/mnemosyne:latest
```

### 5. Gmail-specific notes

Gmail requires an **App Password** instead of your regular password:

1. Go to [Google Account > Security > 2-Step Verification > App passwords](https://myaccount.google.com/apppasswords).
2. Generate an app password for "Mail".
3. Use `imap.gmail.com`, port `993`, TLS enabled, and the app password.

Gmail's IMAP maps labels to folders. Common ones to back up: `INBOX`, `[Gmail]/Sent Mail`, `[Gmail]/All Mail`.

### Updating

**Using the Unraid Docker UI:** Click the container icon and select **Update**. Unraid pulls the new image and recreates the container automatically.

**Using the CLI:**

```bash
docker pull ghcr.io/hjiang/mnemosyne:latest
docker stop mnemosyne
docker rm mnemosyne
docker run -d \
  --name mnemosyne \
  --restart unless-stopped \
  -p 8080:8080 \
  -v /mnt/user/appdata/mnemosyne:/var/lib/mnemosyne \
  ghcr.io/hjiang/mnemosyne:latest
```

Migrations run automatically on startup. Your data in `/mnt/user/appdata/mnemosyne` is preserved across updates.

### Backup your Mnemosyne data

The entire application state lives in the volume mount. To back up Mnemosyne itself:

```bash
# Stop the container first for a clean snapshot
docker stop mnemosyne
cp -a /mnt/user/appdata/mnemosyne /mnt/user/backups/mnemosyne-$(date +%Y%m%d)
docker start mnemosyne
```

This copies the SQLite database, blob store, and encryption key.

## Configuration

Mnemosyne works with zero configuration. All settings have sensible defaults and can be overridden via environment variables or a YAML config file.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `MNEMOSYNE_LISTEN` | `:8080` | Address and port to listen on |
| `MNEMOSYNE_DATA_DIR` | `/var/lib/mnemosyne` | Directory for database, blobs, and encryption key |
| `MNEMOSYNE_BASE_URL` | `http://localhost:8080` | Public URL (for redirects) |
| `MNEMOSYNE_CONFIG` | `/etc/mnemosyne/config.yaml` | Path to optional YAML config file |

### YAML config file

```yaml
listen: ":8080"
data_dir: "/var/lib/mnemosyne"
base_url: "https://mail.example.com"
session_ttl: 720h    # 30 days

backup:
  default_schedule: "0 3 * * *"   # daily at 3 AM
  max_concurrent: 2
```

## Search syntax

| Operator | Example | Description |
|---|---|---|
| Free text | `budget report` | Matches subject, from, to, cc, and body text |
| `from:` | `from:alice@example.com` | Sender address contains value |
| `to:` | `to:bob` | Recipient address contains value |
| `cc:` | `cc:carol` | CC address contains value |
| `subject:` | `subject:"quarterly report"` | Subject contains value (quote for phrases) |
| `has:attachment` | `has:attachment` | Messages with attachments |
| `before:` | `before:2026-01-01` | Messages before date |
| `after:` | `after:2025-06-15` | Messages after date |
| `filename:` | `filename:*.pdf` | Attachment filename matches |

Operators can be combined: `from:alice has:attachment before:2026-01-01 budget`

## Export formats

- **mbox** — Standard mbox format, compatible with Thunderbird, mutt, and most email clients.
- **Maildir** — Streamed as a `.tar` file containing the Maildir directory structure.
- **IMAP upload** — Directly upload messages to a folder on another IMAP server.

## Building the Docker image with Nix

No Docker daemon is needed for the build itself:

```bash
nix build .#docker       # produces result -> .tar.gz
docker load < result     # load into local Docker
```

## Development

```bash
nix develop                          # enter dev shell
go test -race ./...                  # run tests
golangci-lint run ./...              # lint
go run ./cmd/mnemosyne serve         # run locally
```

## License

TBD
