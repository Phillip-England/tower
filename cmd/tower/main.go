package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tower/internal/config"
	"tower/internal/db"
	"tower/internal/httpapi"
	"tower/internal/logic"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		serveCmd(os.Args[2:])
	case "status":
		statusCmd(os.Args[2:])
	case "ban-ip":
		banIPCmd(os.Args[2:])
	case "unban-ip":
		unbanIPCmd(os.Args[2:])
	case "list-bans":
		listBansCmd(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`tower - centralized rate limiting & IP ban management

Commands:
  serve         Start HTTP server
  status        Display system status and metrics
  ban-ip        Ban an IP manually
  unban-ip      Remove IP ban
  list-bans     List banned IPs`)
}

func commonFlags(fs *flag.FlagSet) *string {
	dataDir := fs.String("data-dir", config.DefaultDataDir(), "data directory")
	return dataDir
}

func openDB(dataDir string) *db.DB {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	d, err := db.Open(dataDir)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	return d
}

func ensureAdminToken(d *db.DB) (string, error) {
	if tok, ok, err := d.GetSetting("admin_token"); err != nil {
		return "", err
	} else if ok {
		return tok, nil
	}
	tok, err := config.NewToken(24)
	if err != nil {
		return "", err
	}
	if err := d.SetSetting("admin_token", tok); err != nil {
		return "", err
	}
	return tok, nil
}

func serveCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dataDir := commonFlags(fs)
	addr := fs.String("addr", ":8080", "listen address")
	fs.Parse(args)

	d := openDB(*dataDir)
	defer d.Close()
	adminToken, err := ensureAdminToken(d)
	if err != nil {
		log.Fatalf("admin: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataDir = *dataDir
	cfg.Addr = *addr
	cfg.AdminToken = adminToken

	lim := logic.NewLimiter(cfg, d)
	if err := lim.LoadBans(); err != nil {
		log.Fatalf("load bans: %v", err)
	}

	// Start background DB cleanup (expired bans, vacuum).
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	defer cleanupCancel()
	lim.StartCleanup(cleanupCtx)

	srv, err := httpapi.NewServer(cfg, d, lim, adminToken)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	log.Printf("tower listening on %s", cfg.Addr)
	log.Printf("admin token: %s", adminToken)
	log.Printf("data dir: %s", filepath.Clean(cfg.DataDir))
	if err := http.ListenAndServe(cfg.Addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}

func statusCmd(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	dataDir := commonFlags(fs)
	fs.Parse(args)

	d := openDB(*dataDir)
	defer d.Close()

	bans, _ := d.ListBans()

	activeBans := 0
	expiredBans := 0
	for _, b := range bans {
		if b.ExpiresAt != nil && time.Now().After(*b.ExpiresAt) {
			expiredBans++
		} else {
			activeBans++
		}
	}

	cfg := config.DefaultConfig()
	fmt.Println("Tower Status")
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Data directory:    %s\n", filepath.Clean(*dataDir))
	fmt.Printf("Active bans:       %d\n", activeBans)
	fmt.Printf("Expired bans:      %d\n", expiredBans)
	fmt.Println()
	fmt.Println("Rate Limit Config")
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Request limit:     %d / %s\n", cfg.RequestLimit, cfg.RequestWindow)
	fmt.Printf("Throttle limit:    %d violations / %s\n", cfg.ThrottleLimit, cfg.ThrottleWindow)
	fmt.Printf("Ban duration:      %s\n", cfg.BanDuration)
	fmt.Printf("In-memory log cap: %d\n", cfg.InMemoryLogLimit)
}

func banIPCmd(args []string) {
	fs := flag.NewFlagSet("ban-ip", flag.ExitOnError)
	dataDir := commonFlags(fs)
	ip := fs.String("ip", "", "ip to ban")
	reason := fs.String("reason", "manual ban", "reason")
	duration := fs.Duration("duration", 24*time.Hour, "ban duration (0 for permanent)")
	fs.Parse(args)

	if *ip == "" {
		log.Fatal("--ip required")
	}

	d := openDB(*dataDir)
	defer d.Close()
	cfg := config.DefaultConfig()
	lim := logic.NewLimiter(cfg, d)
	if err := lim.LoadBans(); err != nil {
		log.Fatalf("load bans: %v", err)
	}
	b, err := lim.RecordManualBan(*ip, *reason, *duration)
	if err != nil {
		log.Fatalf("ban ip: %v", err)
	}
	fmt.Printf("banned %s until %v\n", b.IP, b.ExpiresAt)
}

func unbanIPCmd(args []string) {
	fs := flag.NewFlagSet("unban-ip", flag.ExitOnError)
	dataDir := commonFlags(fs)
	ip := fs.String("ip", "", "ip to unban")
	fs.Parse(args)

	if *ip == "" {
		log.Fatal("--ip required")
	}

	d := openDB(*dataDir)
	defer d.Close()
	cfg := config.DefaultConfig()
	lim := logic.NewLimiter(cfg, d)
	if err := lim.Unban(*ip); err != nil {
		log.Fatalf("unban ip: %v", err)
	}
	fmt.Printf("unbanned %s\n", *ip)
}

func listBansCmd(args []string) {
	fs := flag.NewFlagSet("list-bans", flag.ExitOnError)
	dataDir := commonFlags(fs)
	fs.Parse(args)

	d := openDB(*dataDir)
	defer d.Close()
	bans, err := d.ListBans()
	if err != nil {
		log.Fatalf("list bans: %v", err)
	}
	for _, b := range bans {
		fmt.Printf("%s\t%s\t%v\n", b.IP, b.Reason, b.ExpiresAt)
	}
}

