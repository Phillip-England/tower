# Tower — Project Context Document

> This document describes the full Tower project so that an AI or developer can work with it without reading the source code.

## What is Tower?

Tower is a self-hosted Go service that acts as a centralized hub for static-site projects on a VPS. It provides:

- **Request logging** with rate limiting and automatic IP banning
- **Per-user message inboxes** via HTTP API
- **Admin CLI** for user/ban management
- **Admin web UI** for monitoring

It uses SQLite for persistence, has zero external runtime dependencies, and ships as a single binary.

---

## Project Structure

```
tower/
├── cmd/tower/main.go              # CLI entry point (serve, create-user, ban-ip, etc.)
├── internal/
│   ├── config/config.go            # Configuration defaults, token generation
│   ├── db/db.go                    # SQLite database layer (all queries)
│   ├── httpapi/server.go           # HTTP server, routes, handlers
│   ├── logic/limiter.go            # Rate limiting, IP ban logic
│   └── ui/templates.go             # Embedded HTML template for admin UI
├── sdk/go/tower/client.go          # Go SDK for consuming the API
├── go.mod                          # Module: tower, Go 1.24, dep: modernc.org/sqlite
├── Makefile                        # build, run, create-user, ban-ip, etc.
└── data/tower.db                   # SQLite database (created at runtime)
```

---

## Database Schema

Four tables, auto-migrated on startup:

```sql
settings    (key TEXT PK, value TEXT)
users       (id TEXT PK, name TEXT, message_key TEXT, created_at TEXT)
messages    (id INTEGER PK AUTOINCREMENT, user_id TEXT FK→users, body TEXT, created_at TEXT, read_at TEXT)
banned_ips  (ip TEXT PK, reason TEXT, banned_at TEXT, expires_at TEXT)
```

All timestamps are stored as RFC 3339 strings in UTC. Nullable timestamps (`read_at`, `expires_at`) are stored as NULL when unset.

---

## CLI Commands

The binary is `tower`. All commands accept `--data-dir` (defaults to OS config dir).

| Command | Purpose | Key Flags |
|---|---|---|
| `serve` | Start HTTP server | `--addr :8080`, `--ui true`, `--data-dir` |
| `create-user` | Create a user, print ID + key | `--name "Acme"`, `--id acme` (optional) |
| `list-users` | Print all users (TSV) | |
| `rotate-key` | Generate new message key | `--id acme` |
| `ban-ip` | Manually ban an IP | `--ip`, `--reason`, `--duration 24h` |
| `unban-ip` | Remove ban | `--ip` |
| `list-bans` | Print all bans (TSV) | |
| `admin-token` | Print the admin token | |

On first `serve`, an `admin` user and an `admin_token` setting are auto-created.

---

## Data Directory

Default location is OS-specific:
- **macOS:** `$HOME/Library/Application Support/tower`
- **Linux:** `$XDG_CONFIG_HOME/tower` or `$HOME/.config/tower`
- **Windows:** `%APPDATA%\tower`

Override with `--data-dir ./data` (the Makefile uses `./data`).

---

## Authentication

### API Authentication (all `/api/v1/*` routes)

Two required headers on every request:

```
X-Tower-User: <user_id>
X-Tower-Key: <message_key>
```

The server looks up the user in SQLite, compares the key, and rejects with `401` if invalid. The authenticated user is stored in request context and retrieved via `userFrom(r)`.

### Admin Authentication (`/ui*` routes)

Either:
- Header: `X-Admin-Token: <token>`
- Query param: `?token=<token>`

---

## HTTP API Reference

Base URL: `http://<host>:8080`

All responses are JSON with `Content-Type: application/json`. Errors return `{"error": "<message>"}`.

### Health Check

```
GET /healthz
→ 200  "ok"
```

No authentication required.

### Log a Request (Rate Limiting)

```
POST /api/v1/log
Body: {"method": "GET", "path": "/login", "ip": "198.51.100.7"}
→ 200  {"status": "ok"}
→ 429  {"error": "throttled"}
→ 403  {"error": "ip banned"}
```

All fields in the body are optional — defaults to the request's own method/path/IP. The server tracks requests per IP in a sliding window and escalates: throttle → repeated throttle → auto-ban.

### Send a Message

```
POST /api/v1/messages
Body: {"body": "Hello from your app"}
→ 200  {"id": 42}
→ 400  {"error": "invalid body"}
→ 429  {"error": "message throttled"}
```

Subject to per-user message rate limiting (10 msgs / 60s default).

### List Messages

```
GET /api/v1/messages?limit=50&offset=0
→ 200  [{"id":42, "user_id":"acme", "body":"Hello", "created_at":"...", "read_at":null}, ...]
```

- `limit`: 1–200, default 50
- `offset`: >= 0, default 0
- Ordered by `id DESC` (newest first)
- Scoped to the authenticated user

### Get Single Message

```
GET /api/v1/messages/{id}
→ 200  {"id":42, "user_id":"acme", "body":"Hello", "created_at":"...", "read_at":null}
→ 404  {"error": "message not found"}
```

Returns 404 if the message doesn't exist **or** doesn't belong to the authenticated user.

### Mark Message as Read

```
PATCH /api/v1/messages/{id}
→ 200  {"status": "read"}
→ 404  {"error": "message not found"}
```

Sets `read_at` to the current UTC time. Returns 404 if not owned by the authenticated user.

### Delete a Message

```
DELETE /api/v1/messages/{id}
→ 200  {"status": "deleted"}
```

Scoped to authenticated user — the DELETE query includes `AND user_id=?`, so deleting another user's message silently does nothing (returns 200 but affects 0 rows).

### Unread Message Count

```
GET /api/v1/messages/unread-count
→ 200  {"unread_count": 5}
```

Counts messages where `read_at IS NULL` for the authenticated user.

---

## JSON Response Format

All message objects use **snake_case** keys:

```json
{
  "id": 42,
  "user_id": "acme",
  "body": "Hello from your app",
  "created_at": "2025-01-15T10:30:00Z",
  "read_at": null
}
```

---

## Rate Limiting Defaults

| Setting | Default | Scope |
|---|---|---|
| Request rate limit | 120 requests / 60s | Per IP |
| Throttle-to-ban threshold | 5 throttles / 24h | Per IP |
| Auto-ban duration | 24h | Per IP |
| Message rate limit | 10 messages / 60s | Per user |
| In-memory request log | 5000 entries | Global |

Rate limits and throttle counters are in-memory (lost on restart). Bans are persisted in SQLite and loaded into an in-memory cache on startup. Expired bans are lazily cleaned up on next access.

---

## Go SDK (`sdk/go/tower`)

Import path: `tower/sdk/go/tower`

### Initialization

```go
c := tower.New("http://localhost:8080", "acme", "YOUR_MESSAGE_KEY")
```

Fields on `Client`: `BaseURL`, `UserID`, `Key`, `HTTP` (customizable `*http.Client`, 10s timeout default).

### Methods

```go
// Rate limiting
err := c.LogRequest(ctx, "GET", "/page", "198.51.100.7")

// Messaging
id, err := c.SendMessage(ctx, "Hello")
msgs, err := c.ListMessages(ctx, 50, 0)         // limit, offset
msg, err := c.GetMessage(ctx, 42)
err = c.MarkMessageRead(ctx, 42)
count, err := c.UnreadCount(ctx)
err = c.DeleteMessage(ctx, 42)
```

### Error Handling

All methods return errors. API errors (status >= 300) are returned as `fmt.Errorf("tower error: %s", ...)` containing either the JSON `error` field or the HTTP status text.

### Message Struct

```go
type Message struct {
    ID        int64      `json:"id"`
    UserID    string     `json:"user_id"`
    Body      string     `json:"body"`
    CreatedAt time.Time  `json:"created_at"`
    ReadAt    *time.Time `json:"read_at"`
}
```

---

## Admin UI

Accessible at `/ui?token=<admin_token>`. Features:

- View all users (ID, name, message key)
- Create new users (name → auto-generated ID and key)
- View banned IPs (IP, reason, expiration)
- Unban IPs
- View recent requests (time, IP, user, method, path)

The UI is a single embedded HTML template with inline CSS, no JavaScript dependencies.

---

## Build & Run

```bash
make build          # → ./tower binary
make run            # build + serve on :8080 with ./data
make create-user NAME=Acme
make admin-token
make ban-ip IP=1.2.3.4 REASON="abuse" DURATION=24h
make unban-ip IP=1.2.3.4
make check          # fmt + vet + test
```

---

## Key Design Decisions

1. **Single binary** — no config files, no external databases. SQLite + in-memory caches.
2. **Header-based auth** — designed for server-to-server calls where projects on the same VPS talk to Tower.
3. **Ownership scoping** — all message operations (get, list, delete, mark-read) are scoped to the authenticated user. No user can access another user's messages.
4. **Sliding window rate limiting** — in-memory with time-based pruning. Resets on restart (bans persist).
5. **Escalation model** — throttle → repeated throttle → auto-ban. Manual bans also supported via CLI.
6. **snake_case JSON** — all API responses and SDK structs use `snake_case` field names.

---

## Typical Integration Pattern

A static-site project on the same VPS integrates with Tower like this:

```go
// In your project's middleware or handler
c := tower.New("http://localhost:8080", "my-project", os.Getenv("TOWER_KEY"))

// Log every incoming request for rate limiting
err := c.LogRequest(ctx, r.Method, r.URL.Path, clientIP)
if err != nil {
    // "tower error: throttled" or "tower error: ip banned"
}

// Send a message (e.g., contact form submission)
id, _ := c.SendMessage(ctx, "New contact from user@example.com: ...")

// Poll for unread messages
count, _ := c.UnreadCount(ctx)
```
