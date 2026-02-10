package logic

import (
	"net"
	"strings"
	"sync"
	"time"

	"tower/internal/config"
	"tower/internal/db"
)

type RequestLog struct {
	Time   time.Time
	IP     string
	Method string
	Path   string
	UserID string
}

type Limiter struct {
	cfg config.Config
	db  *db.DB

	mu             sync.Mutex
	reqByIP        map[string][]time.Time
	throttleByIP   map[string][]time.Time
	msgByUser      map[string][]time.Time
	bannedCache    map[string]db.Ban
	recentRequests []RequestLog
}

func NewLimiter(cfg config.Config, d *db.DB) *Limiter {
	return &Limiter{
		cfg:            cfg,
		db:             d,
		reqByIP:        make(map[string][]time.Time),
		throttleByIP:   make(map[string][]time.Time),
		msgByUser:      make(map[string][]time.Time),
		bannedCache:    make(map[string]db.Ban),
		recentRequests: make([]RequestLog, 0, cfg.InMemoryLogLimit),
	}
}

func (l *Limiter) LoadBans() error {
	bans, err := l.db.ListBans()
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range bans {
		l.bannedCache[b.IP] = b
	}
	return nil
}

func (l *Limiter) IsBanned(ip string) (bool, db.Ban) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.bannedCache[ip]
	if !ok {
		return false, db.Ban{}
	}
	if b.ExpiresAt != nil && time.Now().After(*b.ExpiresAt) {
		delete(l.bannedCache, ip)
		_ = l.db.UnbanIP(ip)
		return false, db.Ban{}
	}
	return true, b
}

func (l *Limiter) LogRequest(r RequestLog) (throttled bool, banned bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// append to recent log
	if len(l.recentRequests) >= l.cfg.InMemoryLogLimit {
		l.recentRequests = l.recentRequests[1:]
	}
	l.recentRequests = append(l.recentRequests, r)

	// rate limit
	l.reqByIP[r.IP] = prune(l.reqByIP[r.IP], l.cfg.RequestWindow)
	l.reqByIP[r.IP] = append(l.reqByIP[r.IP], r.Time)
	if len(l.reqByIP[r.IP]) <= l.cfg.RequestLimit {
		return false, false
	}

	// throttled
	throttled = true
	l.throttleByIP[r.IP] = prune(l.throttleByIP[r.IP], l.cfg.ThrottleWindow)
	l.throttleByIP[r.IP] = append(l.throttleByIP[r.IP], r.Time)
	if len(l.throttleByIP[r.IP]) >= l.cfg.ThrottleLimit {
		banned = true
	}
	return throttled, banned
}

func (l *Limiter) RecordBan(ip, reason string) (db.Ban, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	exp := time.Now().Add(l.cfg.BanDuration)
	b := db.Ban{
		IP:        ip,
		Reason:    reason,
		BannedAt:  time.Now(),
		ExpiresAt: &exp,
	}
	if err := l.db.BanIP(b); err != nil {
		return db.Ban{}, err
	}
	l.bannedCache[ip] = b
	return b, nil
}

func (l *Limiter) RecordManualBan(ip, reason string, duration time.Duration) (db.Ban, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var exp *time.Time
	if duration > 0 {
		t := time.Now().Add(duration)
		exp = &t
	}
	b := db.Ban{
		IP:        ip,
		Reason:    reason,
		BannedAt:  time.Now(),
		ExpiresAt: exp,
	}
	if err := l.db.BanIP(b); err != nil {
		return db.Ban{}, err
	}
	l.bannedCache[ip] = b
	return b, nil
}

func (l *Limiter) Unban(ip string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.bannedCache, ip)
	return l.db.UnbanIP(ip)
}

func (l *Limiter) CanSendMessage(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgByUser[userID] = prune(l.msgByUser[userID], l.cfg.MessageWindow)
	if len(l.msgByUser[userID]) >= l.cfg.MessageLimit {
		return false
	}
	l.msgByUser[userID] = append(l.msgByUser[userID], time.Now())
	return true
}

func (l *Limiter) RecentRequests() []RequestLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]RequestLog, len(l.recentRequests))
	copy(out, l.recentRequests)
	return out
}

func prune(ts []time.Time, window time.Duration) []time.Time {
	cut := time.Now().Add(-window)
	idx := 0
	for idx < len(ts) && ts[idx].Before(cut) {
		idx++
	}
	return ts[idx:]
}

func ClientIP(remoteAddr, xff string) string {
	if xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}
