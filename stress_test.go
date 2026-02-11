package tower_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tower/internal/config"
	"tower/internal/db"
	"tower/internal/httpapi"
	"tower/internal/logic"
	tower "tower/sdk/go/tower"
)

const testAdminToken = "test-secret-token"

type testEnv struct {
	client  *tower.Client
	limiter *logic.Limiter
	db      *db.DB
	server  *httptest.Server
	dataDir string
}

func newTestServer(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()

	cfg := config.Config{
		DataDir:          dir,
		RequestWindow:    1 * time.Second,
		RequestLimit:     5,
		ThrottleWindow:   10 * time.Second,
		ThrottleLimit:    3,
		BanDuration:      2 * time.Second,
		InMemoryLogLimit: 1000,
		CleanupInterval:  1 * time.Hour,
	}

	d, err := db.Open(dir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}

	lim := logic.NewLimiter(cfg, d)
	srv, err := httpapi.NewServer(cfg, d, lim, testAdminToken)
	if err != nil {
		t.Fatalf("httpapi.NewServer: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())

	client := tower.New(ts.URL, testAdminToken)

	t.Cleanup(func() {
		ts.Close()
		d.Close()
	})

	return &testEnv{
		client:  client,
		limiter: lim,
		db:      d,
		server:  ts,
		dataDir: dir,
	}
}

// decision mirrors the server's JSON response for log/inspect endpoints.
type decision struct {
	Action     string `json:"action"`
	IP         string `json:"ip"`
	Reason     string `json:"reason,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

// logRequestRaw sends a log request and returns the full decision regardless of HTTP status.
func logRequestRaw(t *testing.T, baseURL, ip string) decision {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"ip": ip, "method": "GET", "path": "/test"})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/log", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tower-Key", testAdminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	var d decision
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return d
}

// inspectRaw sends an inspect request and returns the full decision.
func inspectRaw(t *testing.T, baseURL, ip string) decision {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"ip": ip})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/inspect", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tower-Key", testAdminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	var d decision
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return d
}

func TestStress_RateLimitEscalation(t *testing.T) {
	env := newTestServer(t)
	ip := "10.0.0.1"
	ctx := context.Background()

	t.Logf("[ESCALATION] starting escalation test for ip=%s (limit=%d, throttle_limit=%d)", ip, 5, 3)

	// Phase 1: requests within limit should ALLOW
	for i := 1; i <= 5; i++ {
		d, err := env.client.LogRequest(ctx, "GET", "/test", ip)
		if err != nil {
			t.Fatalf("[ESCALATION] request #%d unexpected error: %v", i, err)
		}
		t.Logf("[ESCALATION] request #%d from %s → ACTION=%s", i, ip, d.Action)
		if d.Action != "ALLOW" {
			t.Fatalf("[ESCALATION] expected ALLOW on request #%d, got %s", i, d.Action)
		}
	}

	// Phase 2: first request over limit should FLAG
	d := logRequestRaw(t, env.server.URL, ip)
	t.Logf("[ESCALATION] request #6 from %s → ACTION=%s reason=%q", ip, d.Action, d.Reason)
	if d.Action != "FLAG" {
		t.Fatalf("[ESCALATION] expected FLAG on request #6, got %s", d.Action)
	}

	// Phase 3: continued requests should THROTTLE
	for i := 7; i <= 8; i++ {
		d := logRequestRaw(t, env.server.URL, ip)
		t.Logf("[ESCALATION] request #%d from %s → ACTION=%s reason=%q", i, ip, d.Action, d.Reason)
		if d.Action != "THROTTLE" {
			t.Fatalf("[ESCALATION] expected THROTTLE on request #%d, got %s", i, d.Action)
		}
	}

	// Phase 4: after enough throttles, should BAN (throttle_limit=3, so 3rd throttle triggers ban)
	d = logRequestRaw(t, env.server.URL, ip)
	t.Logf("[ESCALATION] request #9 from %s → ACTION=%s reason=%q", ip, d.Action, d.Reason)
	if d.Action != "BAN" {
		t.Fatalf("[ESCALATION] expected BAN on request #9, got %s", d.Action)
	}

	// Verify inspect also shows BAN
	insp := inspectRaw(t, env.server.URL, ip)
	t.Logf("[ESCALATION] inspect %s → ACTION=%s", ip, insp.Action)
	if insp.Action != "BAN" {
		t.Fatalf("[ESCALATION] expected inspect to show BAN, got %s", insp.Action)
	}

	t.Logf("[ESCALATION] full lifecycle ALLOW→FLAG→THROTTLE→BAN verified")
}

func TestStress_ConcurrentIPs(t *testing.T) {
	env := newTestServer(t)
	numIPs := 50
	requestsPerIP := 10

	t.Logf("[CONCURRENT] launching %d goroutines, %d requests each", numIPs, requestsPerIP)

	var wg sync.WaitGroup
	wg.Add(numIPs)

	for g := 0; g < numIPs; g++ {
		go func(id int) {
			defer wg.Done()
			ip := fmt.Sprintf("10.0.0.%d", id)
			counts := map[string]int{}

			for r := 0; r < requestsPerIP; r++ {
				d := logRequestRaw(t, env.server.URL, ip)
				counts[d.Action]++
			}

			t.Logf("[CONCURRENT] goroutine #%d (ip=%s): %d ALLOW, %d FLAG, %d THROTTLE, %d BAN",
				id, ip, counts["ALLOW"], counts["FLAG"], counts["THROTTLE"], counts["BAN"])
		}(g)
	}

	wg.Wait()

	bans, flagged, tracked, reqs := env.limiter.Stats()
	t.Logf("[CONCURRENT] final stats: bans=%d flagged=%d tracked=%d recent_requests=%d", bans, flagged, tracked, reqs)
	t.Logf("[CONCURRENT] all %d goroutines completed without races", numIPs)
}

func TestStress_BanExpiry(t *testing.T) {
	env := newTestServer(t)
	ip := "10.0.0.99"

	t.Logf("[BAN-EXPIRY] driving %s to BAN status", ip)

	// Drive to BAN: 5 ALLOW + 1 FLAG + 2 THROTTLE + 1 BAN = 9 requests
	var lastAction string
	for i := 1; i <= 20; i++ {
		d := logRequestRaw(t, env.server.URL, ip)
		lastAction = d.Action
		t.Logf("[BAN-EXPIRY] request #%d → ACTION=%s", i, d.Action)
		if d.Action == "BAN" {
			break
		}
	}
	if lastAction != "BAN" {
		t.Fatalf("[BAN-EXPIRY] failed to reach BAN state, last action: %s", lastAction)
	}

	// Verify banned
	insp := inspectRaw(t, env.server.URL, ip)
	t.Logf("[BAN-EXPIRY] inspect after ban → ACTION=%s", insp.Action)
	if insp.Action != "BAN" {
		t.Fatalf("[BAN-EXPIRY] expected BAN, got %s", insp.Action)
	}

	// Wait for ban to expire (ban duration is 2s, wait a bit extra)
	t.Logf("[BAN-EXPIRY] waiting 2.5s for ban to expire...")
	time.Sleep(2500 * time.Millisecond)

	// After expiry, inspect should no longer show BAN
	insp = inspectRaw(t, env.server.URL, ip)
	t.Logf("[BAN-EXPIRY] inspect after expiry → ACTION=%s", insp.Action)
	if insp.Action == "BAN" {
		t.Fatalf("[BAN-EXPIRY] ban should have expired but still shows BAN")
	}
	t.Logf("[BAN-EXPIRY] ban expired successfully, ip is accessible again")
}

func TestStress_ManualBanUnban(t *testing.T) {
	env := newTestServer(t)
	ip := "192.168.1.100"

	t.Logf("[MANUAL-BAN] banning %s via limiter", ip)
	ban, err := env.limiter.RecordManualBan(ip, "manual test ban", 1*time.Hour)
	if err != nil {
		t.Fatalf("[MANUAL-BAN] RecordManualBan: %v", err)
	}
	t.Logf("[MANUAL-BAN] ban recorded: ip=%s reason=%q expires=%v", ban.IP, ban.Reason, ban.ExpiresAt)

	// Verify inspect shows BAN
	insp := inspectRaw(t, env.server.URL, ip)
	t.Logf("[MANUAL-BAN] inspect → ACTION=%s reason=%q", insp.Action, insp.Reason)
	if insp.Action != "BAN" {
		t.Fatalf("[MANUAL-BAN] expected BAN, got %s", insp.Action)
	}

	// Unban
	t.Logf("[MANUAL-BAN] unbanning %s", ip)
	if err := env.limiter.Unban(ip); err != nil {
		t.Fatalf("[MANUAL-BAN] Unban: %v", err)
	}

	// Verify inspect shows ALLOW
	insp = inspectRaw(t, env.server.URL, ip)
	t.Logf("[MANUAL-BAN] inspect after unban → ACTION=%s", insp.Action)
	if insp.Action != "ALLOW" {
		t.Fatalf("[MANUAL-BAN] expected ALLOW after unban, got %s", insp.Action)
	}

	t.Logf("[MANUAL-BAN] manual ban/unban cycle verified")
}

func TestStress_HighThroughput(t *testing.T) {
	env := newTestServer(t)
	ip := "10.0.0.200"
	total := 500

	t.Logf("[THROUGHPUT] sending %d requests from %s as fast as possible", total, ip)

	counts := map[string]int{}
	start := time.Now()

	for i := 1; i <= total; i++ {
		d := logRequestRaw(t, env.server.URL, ip)
		counts[d.Action]++
	}

	elapsed := time.Since(start)
	t.Logf("[THROUGHPUT] completed %d requests in %v (%.0f req/s)", total, elapsed, float64(total)/elapsed.Seconds())
	t.Logf("[THROUGHPUT] results: ALLOW=%d FLAG=%d THROTTLE=%d BAN=%d",
		counts["ALLOW"], counts["FLAG"], counts["THROTTLE"], counts["BAN"])

	// Sanity checks
	if counts["ALLOW"] == 0 {
		t.Fatal("[THROUGHPUT] expected at least some ALLOW responses")
	}
	if counts["ALLOW"] > 10 {
		t.Fatalf("[THROUGHPUT] too many ALLOWs (%d), rate limiting not working", counts["ALLOW"])
	}
	if counts["FLAG"] != 1 {
		t.Fatalf("[THROUGHPUT] expected exactly 1 FLAG, got %d", counts["FLAG"])
	}
	// After BAN, the handleLog endpoint will still be called but the authAPI middleware
	// checks IsBanned. However, since we're using the ip field in the body (not the
	// caller's IP), the auth middleware won't block us. The limiter itself will continue
	// returning BAN once the IP is banned.
	if counts["BAN"] == 0 {
		t.Fatal("[THROUGHPUT] expected some BAN responses")
	}

	t.Logf("[THROUGHPUT] distribution looks correct")
}

func TestStress_MultipleIPsEscalation(t *testing.T) {
	env := newTestServer(t)
	numIPs := 10
	// Each IP needs: 5 ALLOW + 1 FLAG + 2 THROTTLE + 1 BAN = 9 requests to reach BAN
	requestsPerIP := 15

	t.Logf("[MULTI-ESCALATION] escalating %d IPs independently", numIPs)

	for i := 0; i < numIPs; i++ {
		ip := fmt.Sprintf("172.16.0.%d", i)
		var lastAction string

		for r := 1; r <= requestsPerIP; r++ {
			d := logRequestRaw(t, env.server.URL, ip)
			lastAction = d.Action
			if d.Action == "BAN" {
				t.Logf("[MULTI-ESCALATION] ip=%s reached BAN at request #%d", ip, r)
				break
			}
		}

		if lastAction != "BAN" {
			t.Fatalf("[MULTI-ESCALATION] ip=%s did not reach BAN, last action: %s", ip, lastAction)
		}

		// Verify via inspect
		insp := inspectRaw(t, env.server.URL, ip)
		if insp.Action != "BAN" {
			t.Fatalf("[MULTI-ESCALATION] ip=%s inspect expected BAN, got %s", ip, insp.Action)
		}
	}

	bans, _, _, _ := env.limiter.Stats()
	t.Logf("[MULTI-ESCALATION] all %d IPs banned independently (active_bans=%d)", numIPs, bans)
	if bans != numIPs {
		t.Fatalf("[MULTI-ESCALATION] expected %d bans, got %d", numIPs, bans)
	}
}

func TestStress_CallbackNotification(t *testing.T) {
	env := newTestServer(t)
	ip := "10.0.0.50"

	var mu sync.Mutex
	var received []decision

	// Set up a callback receiver
	cbServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var d decision
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			t.Logf("[CALLBACK] failed to decode payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		event := r.Header.Get("X-Tower-Event")
		t.Logf("[CALLBACK] received notification: action=%s ip=%s reason=%q event_header=%s", d.Action, d.IP, d.Reason, event)
		mu.Lock()
		received = append(received, d)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cbServer.Close)

	t.Logf("[CALLBACK] callback server at %s", cbServer.URL)

	// Register callback via SDK
	ctx := context.Background()
	if err := env.client.RegisterCallback(ctx, cbServer.URL); err != nil {
		t.Fatalf("[CALLBACK] RegisterCallback: %v", err)
	}
	t.Logf("[CALLBACK] callback registered")

	// Drive IP to escalation
	t.Logf("[CALLBACK] driving %s through escalation...", ip)
	for i := 1; i <= 15; i++ {
		d := logRequestRaw(t, env.server.URL, ip)
		t.Logf("[CALLBACK] request #%d → ACTION=%s", i, d.Action)
		if d.Action == "BAN" {
			break
		}
	}

	// Give callbacks time to be delivered (they're async goroutines)
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	t.Logf("[CALLBACK] total callbacks received: %d", count)
	if count == 0 {
		t.Fatal("[CALLBACK] expected at least one callback notification")
	}

	// Verify we got FLAG, THROTTLE, and BAN callbacks
	mu.Lock()
	actions := map[string]int{}
	for _, d := range received {
		actions[d.Action]++
	}
	mu.Unlock()

	t.Logf("[CALLBACK] callback breakdown: FLAG=%d THROTTLE=%d BAN=%d", actions["FLAG"], actions["THROTTLE"], actions["BAN"])
	if actions["FLAG"] == 0 {
		t.Fatal("[CALLBACK] expected at least one FLAG callback")
	}
	if actions["THROTTLE"] == 0 {
		t.Fatal("[CALLBACK] expected at least one THROTTLE callback")
	}
	if actions["BAN"] == 0 {
		t.Fatal("[CALLBACK] expected at least one BAN callback")
	}
}

func TestStress_DatabasePersistence(t *testing.T) {
	dir := t.TempDir()

	cfg := config.Config{
		DataDir:          dir,
		RequestWindow:    1 * time.Second,
		RequestLimit:     5,
		ThrottleWindow:   10 * time.Second,
		ThrottleLimit:    3,
		BanDuration:      1 * time.Hour, // long ban so it doesn't expire during test
		InMemoryLogLimit: 1000,
		CleanupInterval:  1 * time.Hour,
	}

	ip := "10.0.0.77"

	// Phase 1: create DB, limiter, drive to BAN
	t.Logf("[DB-PERSIST] phase 1: creating initial limiter and driving %s to BAN", ip)
	d1, err := db.Open(dir)
	if err != nil {
		t.Fatalf("[DB-PERSIST] db.Open #1: %v", err)
	}

	lim1 := logic.NewLimiter(cfg, d1)
	srv1, _ := httpapi.NewServer(cfg, d1, lim1, testAdminToken)
	ts1 := httptest.NewServer(srv1.Handler())

	var lastAction string
	for i := 1; i <= 15; i++ {
		dec := logRequestRaw(t, ts1.URL, ip)
		lastAction = dec.Action
		t.Logf("[DB-PERSIST] phase 1: request #%d → ACTION=%s", i, dec.Action)
		if dec.Action == "BAN" {
			break
		}
	}
	if lastAction != "BAN" {
		t.Fatalf("[DB-PERSIST] failed to reach BAN, last action: %s", lastAction)
	}

	// Verify ban is in DB
	ban, found, err := d1.GetBan(ip)
	if err != nil {
		t.Fatalf("[DB-PERSIST] GetBan: %v", err)
	}
	if !found {
		t.Fatal("[DB-PERSIST] ban not found in database")
	}
	t.Logf("[DB-PERSIST] ban in DB: ip=%s reason=%q expires=%v", ban.IP, ban.Reason, ban.ExpiresAt)

	// Close phase 1
	ts1.Close()
	d1.Close()
	t.Logf("[DB-PERSIST] phase 1 closed")

	// Phase 2: open fresh DB + limiter, verify ban survives
	t.Logf("[DB-PERSIST] phase 2: opening fresh limiter on same DB")
	d2, err := db.Open(dir)
	if err != nil {
		t.Fatalf("[DB-PERSIST] db.Open #2: %v", err)
	}
	t.Cleanup(func() { d2.Close() })

	lim2 := logic.NewLimiter(cfg, d2)

	// Before LoadBans, the in-memory cache is empty
	banned, _ := lim2.IsBanned(ip)
	t.Logf("[DB-PERSIST] before LoadBans: IsBanned(%s)=%v", ip, banned)
	if banned {
		t.Fatal("[DB-PERSIST] expected not banned before LoadBans")
	}

	// Load bans from DB
	if err := lim2.LoadBans(); err != nil {
		t.Fatalf("[DB-PERSIST] LoadBans: %v", err)
	}

	banned, loadedBan := lim2.IsBanned(ip)
	t.Logf("[DB-PERSIST] after LoadBans: IsBanned(%s)=%v reason=%q", ip, banned, loadedBan.Reason)
	if !banned {
		t.Fatal("[DB-PERSIST] expected banned after LoadBans")
	}

	// Also verify through a fresh HTTP server
	srv2, _ := httpapi.NewServer(cfg, d2, lim2, testAdminToken)
	ts2 := httptest.NewServer(srv2.Handler())
	t.Cleanup(ts2.Close)

	insp := inspectRaw(t, ts2.URL, ip)
	t.Logf("[DB-PERSIST] phase 2 inspect → ACTION=%s", insp.Action)
	if insp.Action != "BAN" {
		t.Fatalf("[DB-PERSIST] expected BAN from fresh server, got %s", insp.Action)
	}

	t.Logf("[DB-PERSIST] database persistence verified across limiter restarts")
}

func TestStress_BurstRecovery(t *testing.T) {
	env := newTestServer(t)
	ip := "10.0.0.150"

	t.Logf("[BURST-RECOVERY] sending burst of requests, then waiting for window to reset")

	// Send exactly at the limit
	for i := 1; i <= 5; i++ {
		d := logRequestRaw(t, env.server.URL, ip)
		if d.Action != "ALLOW" {
			t.Fatalf("[BURST-RECOVERY] expected ALLOW on request #%d, got %s", i, d.Action)
		}
	}
	t.Logf("[BURST-RECOVERY] sent 5 requests (all ALLOW)")

	// Wait for request window to expire (1s + buffer)
	t.Logf("[BURST-RECOVERY] waiting 1.2s for request window to expire...")
	time.Sleep(1200 * time.Millisecond)

	// Should get ALLOW again since the window reset
	// Note: the IP is still flagged from being at the limit, but the request count is fresh
	// Actually, looking at LogRequest: it prunes reqByIP first, then checks count.
	// After window expires, count resets to 1, which is <= limit, so ALLOW.
	d := logRequestRaw(t, env.server.URL, ip)
	t.Logf("[BURST-RECOVERY] after window reset, request → ACTION=%s", d.Action)
	if d.Action != "ALLOW" {
		t.Fatalf("[BURST-RECOVERY] expected ALLOW after window reset, got %s", d.Action)
	}

	t.Logf("[BURST-RECOVERY] burst recovery verified")
}

func TestStress_ConcurrentSameIP(t *testing.T) {
	env := newTestServer(t)
	ip := "10.0.0.42"
	numGoroutines := 20
	requestsPerGoroutine := 5

	t.Logf("[CONCURRENT-SAME-IP] %d goroutines hitting same ip=%s with %d requests each",
		numGoroutines, ip, requestsPerGoroutine)

	var wg sync.WaitGroup
	var totalAllow, totalFlag, totalThrottle, totalBan atomic.Int64

	wg.Add(numGoroutines)
	for g := 0; g < numGoroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for r := 0; r < requestsPerGoroutine; r++ {
				d := logRequestRaw(t, env.server.URL, ip)
				switch d.Action {
				case "ALLOW":
					totalAllow.Add(1)
				case "FLAG":
					totalFlag.Add(1)
				case "THROTTLE":
					totalThrottle.Add(1)
				case "BAN":
					totalBan.Add(1)
				}
			}
		}(g)
	}

	wg.Wait()

	allow := totalAllow.Load()
	flag := totalFlag.Load()
	throttle := totalThrottle.Load()
	ban := totalBan.Load()
	total := allow + flag + throttle + ban

	t.Logf("[CONCURRENT-SAME-IP] total=%d ALLOW=%d FLAG=%d THROTTLE=%d BAN=%d",
		total, allow, flag, throttle, ban)

	if total != int64(numGoroutines*requestsPerGoroutine) {
		t.Fatalf("[CONCURRENT-SAME-IP] expected %d total responses, got %d",
			numGoroutines*requestsPerGoroutine, total)
	}
	if flag != 1 {
		t.Fatalf("[CONCURRENT-SAME-IP] expected exactly 1 FLAG, got %d", flag)
	}
	if ban == 0 {
		t.Fatal("[CONCURRENT-SAME-IP] expected at least one BAN")
	}
	t.Logf("[CONCURRENT-SAME-IP] concurrent access to same IP handled correctly")
}
