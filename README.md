# Sudux Backend Server

A scalable, high-performance Sudoku backend built with Go, PostgreSQL, and Redis. It provides REST APIs for authentication, player management, and statistics, as well as real-time WebSocket communication for multiplayer matches and background matchmaking.

---

## Requirements

- **Go**: 1.22 or higher (for building locally)
- **PostgreSQL**: 14 or higher
- **Redis**: 7 or higher
- **Target OS**: Ubuntu 24.04 (or any Linux distribution) / macOS / Windows

---

## System Setup (Ubuntu 24.04 VPS)

### 1. Install PostgreSQL & Redis

Run the following commands on your Ubuntu 24.04 VPS:

```bash
sudo apt update
sudo apt install -y postgresql redis-server
```

Start and enable Redis:

```bash
sudo systemctl enable --now redis-server
```

### 2. Configure PostgreSQL Database and User

Log in to the PostgreSQL prompt:

```bash
sudo -u postgres psql
```

Execute the following SQL statements to create the database user and database (replace `YOUR_STRONG_PASSWORD` with a secure password):

```sql
CREATE USER sudux WITH PASSWORD 'YOUR_STRONG_PASSWORD';
CREATE DATABASE sudux OWNER sudux;
GRANT ALL PRIVILEGES ON DATABASE sudux TO sudux;
\q
```

---

## Building the Binary

You can either build the single unified server binary directly on your VPS or cross-compile it from your local machine.

### Option A: Build on VPS
If Go is installed on your VPS:
```bash
go build -o sudux-server ./cmd/server
```

### Option B: Cross-Compile from Local Machine (Recommended for 2GB VPS)
To build a Linux binary from a local machine (e.g., Linux, macOS, or Windows):

```bash
# On Linux / macOS:
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o sudux-server ./cmd/server

# On Windows PowerShell:
$env:GOOS="linux"; $env:GOARCH="amd64"; go build -ldflags="-s -w" -o sudux-server ./cmd/server
```

Then upload `sudux-server` and the `migrations/` folder to your VPS (e.g. via `scp` or `rsync`):

```bash
scp sudux-server .env migrations/ user@your-vps-ip:/opt/sudux/
```

> **Note:** The `migrations/` directory must be present in the same working directory where `sudux-server` is run so the application can automatically run database schema migrations on startup.

---

## Configuration

Copy `.env.example` to `.env` in the application directory on your VPS:

```bash
cp .env.example .env
```

Edit `.env` to configure your production variables:

```ini
APP_ENV=production
APP_PORT=8080

DATABASE_URL=postgres://sudux:YOUR_STRONG_PASSWORD@localhost:5432/sudux?sslmode=disable
REDIS_URL=redis://localhost:6379/0

JWT_ACCESS_SECRET=your_generated_access_secret_here
JWT_REFRESH_SECRET=your_generated_refresh_secret_here

RESEND_API_KEY=re_your_real_resend_api_key
RESEND_FROM_EMAIL=login@yourdomain.com
```

> **Generate JWT Secrets:**
> ```bash
> openssl rand -base64 48
> ```

---

## Running with Systemd (Ubuntu 24.04)

To run `sudux-server` as a background service managed by `systemd`:

1. Create a service file `/etc/systemd/system/sudux.service`:

```bash
sudo nano /etc/systemd/system/sudux.service
```

2. Add the following configuration (adjust path and user if needed, e.g., `/opt/sudux`):

```ini
[Unit]
Description=Sudux Backend Unified Server
After=network.target postgresql.service redis-server.service
Wants=postgresql.service redis-server.service

[Service]
Type=simple
User=ubuntu
WorkingDirectory=/opt/sudux
ExecStart=/opt/sudux/sudux-server
Restart=always
RestartSec=5
EnvironmentFile=/opt/sudux/.env
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

3. Reload systemd, enable and start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable sudux
sudo systemctl start sudux
```

4. Check status and view logs:

```bash
# Check service status
sudo systemctl status sudux

# Stream logs
journalctl -u sudux -f
```

---

## API & WebSocket Endpoints

When running on port `8080`:

- **Health Check:** `GET http://<vps-ip>:8080/health`
- **REST API:** `http://<vps-ip>:8080/auth/*`, `/me`, `/friends/*`, `/matchmaking/*`, `/stats`
- **WebSocket:** `ws://<vps-ip>:8080/ws?token=<JWT_ACCESS_TOKEN>`

---

## Development

To run locally in development mode:

1. Ensure local Postgres & Redis are running.
2. Set `APP_ENV=development` in your `.env`.
3. Run:

```bash
go run ./cmd/server
```
