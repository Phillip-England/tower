package logic

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tower/internal/config"
	"tower/internal/db"
)

// Action represents the escalation decision for an IP.
type Action string

const (
	ActionAllow    Action = "ALLOW"
	ActionFlag     Action = "FLAG"
	ActionThrottle Action = "THROTTLE"
	ActionBan      Action = "BAN"
)

// Decision is the result of inspecting or logging a request.
type Decision struct {
	Action     Action `json:"action"`
	IP         string `json:"ip"`
	Reason     string `json:"reason,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"` // seconds
}

type RequestLog struct {
	Time   time.Time
	IP     string
	Method string
	Path   string
}

type Limiter struct {
	cfg config.Config
	db  *db.DB

	mu             sync.Mutex
	reqByIP        map[string][]time.Time
	flaggedIPs     map[string]time.Time // first-time suspicious behavior
	throttleByIP   map[string][]time.Time
	bannedCache    map[string]db.Ban
	recentRequests []RequestLog
	callbacks      []string // callback URLs
}

func NewLimiter(cfg config.Config, d *db.DB) *Limiter {
	return &Limiter{
		cfg:            cfg,
		db:             d,
		reqByIP:        make(map[string][]time.Time),
		flaggedIPs:     make(map[string]time.Time),
		throttleByIP:   make(map[string][]time.Time),
		bannedCache:    make(map[string]db.Ban),
		recentRequests: make([]RequestLog, 0, cfg.InMemoryLogLimit),
	}
}

// StartCleanup launches a background goroutine that periodically removes
// expired bans and reclaims disk space. It stops when the context is cancelled.
func (l *Limiter) StartCleanup(ctx context.Context) {
	interval := l.cfg.CleanupInterval
	if interval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				l.runCleanup()
			}
		}
	}()
}

func (l *Limiter) runCleanup() {
	// 1. Delete expired bans from DB and evict from cache.
	deleted, _ := l.db.DeleteExpiredBans()
	if deleted > 0 {
		l.mu.Lock()
		for ip, b := range l.bannedCache {
			if b.ExpiresAt != nil && time.Now().After(*b.ExpiresAt) {
				delete(l.bannedCache, ip)
			}
		}
		l.mu.Unlock()
	}

	// 2. Reclaim freed disk space.
	l.db.IncrementalVacuum()
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

// Inspect checks an IP against the current state without recording a request.
func (l *Limiter) Inspect(ip string) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Check ban first
	if b, ok := l.bannedCache[ip]; ok {
		if b.ExpiresAt != nil && time.Now().After(*b.ExpiresAt) {
			delete(l.bannedCache, ip)
			_ = l.db.UnbanIP(ip)
		} else {
			return Decision{Action: ActionBan, IP: ip, Reason: b.Reason}
		}
	}

	// Check throttle state
	throttles := prune(l.throttleByIP[ip], l.cfg.ThrottleWindow)
	if len(throttles) > 0 {
		return Decision{Action: ActionThrottle, IP: ip, Reason: "rate limit exceeded", RetryAfter: int(l.cfg.RequestWindow.Seconds())}
	}

	// Check flagged state
	if _, flagged := l.flaggedIPs[ip]; flagged {
		return Decision{Action: ActionFlag, IP: ip, Reason: "suspicious activity detected"}
	}

	return Decision{Action: ActionAllow, IP: ip}
}

func (l *Limiter) LogRequest(r RequestLog) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()

	// append to recent log
	if len(l.recentRequests) >= l.cfg.InMemoryLogLimit {
		l.recentRequests = l.recentRequests[1:]
	}
	l.recentRequests = append(l.recentRequests, r)

	// rate limit check
	l.reqByIP[r.IP] = prune(l.reqByIP[r.IP], l.cfg.RequestWindow)
	l.reqByIP[r.IP] = append(l.reqByIP[r.IP], r.Time)
	count := len(l.reqByIP[r.IP])

	// Under limit: allow
	if count <= l.cfg.RequestLimit {
		return Decision{Action: ActionAllow, IP: r.IP}
	}

	// First time exceeding limit: flag
	if _, flagged := l.flaggedIPs[r.IP]; !flagged {
		l.flaggedIPs[r.IP] = r.Time
		return Decision{Action: ActionFlag, IP: r.IP, Reason: "suspicious activity detected"}
	}

	// Repeated violations: throttle
	l.throttleByIP[r.IP] = prune(l.throttleByIP[r.IP], l.cfg.ThrottleWindow)
	l.throttleByIP[r.IP] = append(l.throttleByIP[r.IP], r.Time)
	if len(l.throttleByIP[r.IP]) >= l.cfg.ThrottleLimit {
		return Decision{Action: ActionBan, IP: r.IP, Reason: "auto-ban: repeated throttling"}
	}
	return Decision{Action: ActionThrottle, IP: r.IP, Reason: "rate limit exceeded", RetryAfter: int(l.cfg.RequestWindow.Seconds())}
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

func (l *Limiter) RecentRequests() []RequestLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]RequestLog, len(l.recentRequests))
	copy(out, l.recentRequests)
	return out
}

// RegisterCallback adds a URL that will be notified on security events.
func (l *Limiter) RegisterCallback(url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, u := range l.callbacks {
		if u == url {
			return
		}
	}
	l.callbacks = append(l.callbacks, url)
}

// UnregisterCallback removes a callback URL.
func (l *Limiter) UnregisterCallback(url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, u := range l.callbacks {
		if u == url {
			l.callbacks = append(l.callbacks[:i], l.callbacks[i+1:]...)
			return
		}
	}
}

// Callbacks returns the registered callback URLs.
func (l *Limiter) Callbacks() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.callbacks))
	copy(out, l.callbacks)
	return out
}

// NotifyCallbacks sends a security event to all registered callback URLs.
func (l *Limiter) NotifyCallbacks(d Decision) {
	l.mu.Lock()
	urls := make([]string, len(l.callbacks))
	copy(urls, l.callbacks)
	l.mu.Unlock()

	if len(urls) == 0 || d.Action == ActionAllow {
		return
	}

	payload, _ := json.Marshal(d)
	for _, u := range urls {
		go func(target string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Tower-Event", string(d.Action))
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}(u)
	}
}

// Stats returns current limiter statistics.
func (l *Limiter) Stats() (activeBans, flaggedIPs, trackedIPs, recentReqs int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.bannedCache), len(l.flaggedIPs), len(l.reqByIP), len(l.recentRequests)
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
