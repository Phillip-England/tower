package ui

import (
	"tower/internal/db"
	"tower/internal/logic"
)

type ViewData struct {
	AdminToken string
	Users      []db.User
	Bans       []db.Ban
	Requests   []logic.RequestLog
}

const Template = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>Tower Admin</title>
  <style>
    :root {
      --bg: #f5f1e8;
      --ink: #1b1b1b;
      --accent: #1f6feb;
      --muted: #666;
      --card: #fffaf0;
    }
    body { font-family: "Georgia", "Times New Roman", serif; background: var(--bg); color: var(--ink); margin: 0; }
    header { padding: 24px 32px; border-bottom: 2px solid #ddd; display:flex; justify-content: space-between; align-items: baseline; }
    h1 { margin: 0; font-size: 28px; }
    .container { padding: 24px 32px; display: grid; grid-template-columns: 1fr 1fr; gap: 24px; }
    .card { background: var(--card); padding: 16px; border: 1px solid #e0d8c8; box-shadow: 4px 4px 0 #ddd; }
    table { width: 100%; border-collapse: collapse; }
    th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid #e6e0d5; }
    form { margin: 0; }
    input, button { font-family: inherit; padding: 6px 8px; }
    button { background: var(--accent); color: white; border: none; cursor: pointer; }
    .muted { color: var(--muted); }
    .full { grid-column: 1 / -1; }
    @media (max-width: 900px) { .container { grid-template-columns: 1fr; } }
  </style>
</head>
<body>
  <header>
    <h1>Tower Admin</h1>
    <div class="muted">token: {{.AdminToken}}</div>
  </header>

  <div class="container">
    <section class="card">
      <h2>Users</h2>
      <form method="post" action="/ui/users?token={{.AdminToken}}">
        <input type="text" name="name" placeholder="New user name" />
        <button type="submit">Create</button>
      </form>
      <table>
        <tr><th>ID</th><th>Name</th><th>Key</th></tr>
        {{range .Users}}
          <tr><td>{{.ID}}</td><td>{{.Name}}</td><td class="muted">{{.MessageKey}}</td></tr>
        {{end}}
      </table>
    </section>

    <section class="card">
      <h2>Banned IPs</h2>
      <table>
        <tr><th>IP</th><th>Reason</th><th>Expires</th><th></th></tr>
        {{range .Bans}}
          <tr>
            <td>{{.IP}}</td>
            <td class="muted">{{.Reason}}</td>
            <td class="muted">{{if .ExpiresAt}}{{.ExpiresAt}}{{else}}never{{end}}</td>
            <td>
              <form method="post" action="/ui/bans/unban?token={{$.AdminToken}}">
                <input type="hidden" name="ip" value="{{.IP}}" />
                <button type="submit">Unban</button>
              </form>
            </td>
          </tr>
        {{end}}
      </table>
    </section>

    <section class="card full">
      <h2>Recent Requests</h2>
      <table>
        <tr><th>Time</th><th>IP</th><th>User</th><th>Method</th><th>Path</th></tr>
        {{range .Requests}}
          <tr>
            <td class="muted">{{.Time}}</td>
            <td>{{.IP}}</td>
            <td>{{.UserID}}</td>
            <td>{{.Method}}</td>
            <td class="muted">{{.Path}}</td>
          </tr>
        {{end}}
      </table>
    </section>
  </div>
</body>
</html>`
