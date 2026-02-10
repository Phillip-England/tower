package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

func Open(dataDir string) (*DB, error) {
	if dataDir == "" {
		return nil, errors.New("data dir required")
	}
	path := filepath.Join(dataDir, "tower.db")
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := migrate(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &DB{conn: conn}, nil
}

func (d *DB) Close() error { return d.conn.Close() }

func migrate(conn *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			message_key TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			body TEXT NOT NULL,
			created_at TEXT NOT NULL,
			read_at TEXT,
			FOREIGN KEY(user_id) REFERENCES users(id)
		);`,
		`CREATE TABLE IF NOT EXISTS banned_ips (
			ip TEXT PRIMARY KEY,
			reason TEXT NOT NULL,
			banned_at TEXT NOT NULL,
			expires_at TEXT
		);`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) GetSetting(key string) (string, bool, error) {
	var val string
	err := d.conn.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (d *DB) SetSetting(key, value string) error {
	_, err := d.conn.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

type User struct {
	ID         string
	Name       string
	MessageKey string
	CreatedAt  time.Time
}

func (d *DB) CreateUser(u User) error {
	_, err := d.conn.Exec(`INSERT INTO users(id,name,message_key,created_at) VALUES(?,?,?,?)`,
		u.ID, u.Name, u.MessageKey, u.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

func (d *DB) GetUser(id string) (User, bool, error) {
	var u User
	var created string
	err := d.conn.QueryRow(`SELECT id,name,message_key,created_at FROM users WHERE id=?`, id).
		Scan(&u.ID, &u.Name, &u.MessageKey, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return u, true, nil
}

func (d *DB) ListUsers() ([]User, error) {
	rows, err := d.conn.Query(`SELECT id,name,message_key,created_at FROM users ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var created string
		if err := rows.Scan(&u.ID, &u.Name, &u.MessageKey, &created); err != nil {
			return nil, err
		}
		u.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) UpdateUserKey(id, key string) error {
	res, err := d.conn.Exec(`UPDATE users SET message_key=? WHERE id=?`, key, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

type Message struct {
	ID        int64
	UserID    string
	Body      string
	CreatedAt time.Time
	ReadAt    *time.Time
}

func (d *DB) CreateMessage(m Message) (int64, error) {
	res, err := d.conn.Exec(`INSERT INTO messages(user_id,body,created_at,read_at) VALUES(?,?,?,?)`,
		m.UserID, m.Body, m.CreatedAt.UTC().Format(time.RFC3339), nullableTime(m.ReadAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ListMessages(userID string, limit int) ([]Message, error) {
	rows, err := d.conn.Query(`SELECT id,user_id,body,created_at,read_at FROM messages WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var created, read sql.NullString
		if err := rows.Scan(&m.ID, &m.UserID, &m.Body, &created, &read); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse(time.RFC3339, created.String)
		if read.Valid {
			t, _ := time.Parse(time.RFC3339, read.String)
			m.ReadAt = &t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (d *DB) DeleteMessage(id int64) error {
	_, err := d.conn.Exec(`DELETE FROM messages WHERE id=?`, id)
	return err
}

func (d *DB) MarkMessageRead(id int64) error {
	_, err := d.conn.Exec(`UPDATE messages SET read_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func nullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

type Ban struct {
	IP        string
	Reason    string
	BannedAt  time.Time
	ExpiresAt *time.Time
}

func (d *DB) BanIP(b Ban) error {
	_, err := d.conn.Exec(`INSERT INTO banned_ips(ip,reason,banned_at,expires_at) VALUES(?,?,?,?)
		ON CONFLICT(ip) DO UPDATE SET reason=excluded.reason,banned_at=excluded.banned_at,expires_at=excluded.expires_at`,
		b.IP, b.Reason, b.BannedAt.UTC().Format(time.RFC3339), nullableTime(b.ExpiresAt))
	return err
}

func (d *DB) UnbanIP(ip string) error {
	_, err := d.conn.Exec(`DELETE FROM banned_ips WHERE ip=?`, ip)
	return err
}

func (d *DB) ListBans() ([]Ban, error) {
	rows, err := d.conn.Query(`SELECT ip,reason,banned_at,expires_at FROM banned_ips ORDER BY banned_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ban
	for rows.Next() {
		var b Ban
		var banned, expires sql.NullString
		if err := rows.Scan(&b.IP, &b.Reason, &banned, &expires); err != nil {
			return nil, err
		}
		b.BannedAt, _ = time.Parse(time.RFC3339, banned.String)
		if expires.Valid {
			t, _ := time.Parse(time.RFC3339, expires.String)
			b.ExpiresAt = &t
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (d *DB) GetBan(ip string) (Ban, bool, error) {
	var b Ban
	var banned, expires sql.NullString
	err := d.conn.QueryRow(`SELECT ip,reason,banned_at,expires_at FROM banned_ips WHERE ip=?`, ip).
		Scan(&b.IP, &b.Reason, &banned, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Ban{}, false, nil
	}
	if err != nil {
		return Ban{}, false, err
	}
	b.BannedAt, _ = time.Parse(time.RFC3339, banned.String)
	if expires.Valid {
		t, _ := time.Parse(time.RFC3339, expires.String)
		b.ExpiresAt = &t
	}
	return b, true, nil
}
