# Tower

Tower is a small, self-hosted hub for:
- request logging
- rate limiting + IP banning
- user message inboxes

It runs as a CLI service backed by SQLite and exposes an HTTP API plus a minimal admin UI.

## Quickstart

Build and run:

```bash
go build ./cmd/tower
./tower serve --addr :8080
```

On first run, Tower creates a SQLite database in the OS-specific data directory and prints the admin token.

Admin UI:

```
http://localhost:8080/ui?token=YOUR_ADMIN_TOKEN
```

Create a user:

```bash
./tower create-user --name "Acme"
```

The output includes:

```
user_id=acme
message_key=...secret...
```

Use those credentials from your applications (via HTTP API or the Go SDK).

## CLI Commands

```bash
./tower serve --addr :8080 --data-dir /path/to/data
./tower create-user --name "Acme" --id acme
./tower list-users
./tower rotate-key --id acme
./tower ban-ip --ip 203.0.113.10 --reason "abuse" --duration 24h
./tower unban-ip --ip 203.0.113.10
./tower list-bans
./tower admin-token
```

## Data Directory

By default, Tower uses the OS config directory:
- macOS: `$HOME/Library/Application Support/tower`
- Linux: `$XDG_CONFIG_HOME/tower` or `$HOME/.config/tower`
- Windows: `%APPDATA%\tower`

Override with `--data-dir`.

## HTTP API

All API calls require:
- `X-Tower-User: <user_id>`
- `X-Tower-Key: <message_key>`

### Log a Request

`POST /api/v1/log`

```json
{ "method": "GET", "path": "/login", "ip": "198.51.100.7" }
```

Responses:
- `200` ok
- `429` throttled
- `403` banned

### Send a Message

`POST /api/v1/messages`

```json
{ "body": "Hello from your app" }
```

### List Messages

`GET /api/v1/messages?limit=50`

### Delete Message

`DELETE /api/v1/messages/{id}`

## Defaults (Sane + Time-Bound)

These defaults are designed to be hard for legitimate users to trigger:

- Request rate limit: `120 requests / 60s` per IP
- Throttle -> ban: `5 throttles / 24h` per IP
- Ban duration: `24h`
- Message throttle: `10 messages / 60s` per user
- Recent request log: `5000` entries in memory

Banned IPs are persisted in SQLite. Request logs and throttles remain in memory.

## Admin UI

The admin UI allows you to:
- view users
- create users
- view banned IPs
- unban IPs
- view recent requests (in memory)

Access with:

```
/ui?token=YOUR_ADMIN_TOKEN
```

## Go SDK

The Go SDK is in `sdk/go/tower`.

Example:

```go
package main

import (
  "context"
  "log"
  "tower/sdk/go/tower"
)

func main() {
  c := tower.New("http://localhost:8080", "acme", "YOUR_KEY")

  if err := c.LogRequest(context.Background(), "GET", "/login", "198.51.100.7"); err != nil {
    log.Fatal(err)
  }

  id, err := c.SendMessage(context.Background(), "Hello from the app")
  if err != nil {
    log.Fatal(err)
  }
  log.Printf("message id: %d", id)
}
```

## Notes

- Admin user is created automatically with ID `admin`.
- Admin token is generated on first run and stored in `settings`.
- If you rotate a user key, update any apps using that key.
