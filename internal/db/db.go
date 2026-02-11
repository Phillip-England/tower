package db

import (
	"database/sql"
	"errors"
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
	// Enable incremental auto-vacuum so deleted rows reclaim disk space.
	if _, err := conn.Exec(`PRAGMA auto_vacuum = INCREMENTAL`); err != nil {
		_ = conn.Close()
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

// DeleteExpiredBans removes all bans whose expires_at is in the past.
func (d *DB) DeleteExpiredBans() (int64, error) {
	res, err := d.conn.Exec(`DELETE FROM banned_ips WHERE expires_at IS NOT NULL AND expires_at < ?`,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IncrementalVacuum reclaims free pages from the database file.
func (d *DB) IncrementalVacuum() error {
	_, err := d.conn.Exec(`PRAGMA incremental_vacuum`)
	return err
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
