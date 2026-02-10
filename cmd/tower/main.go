package main

import (
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
	case "create-user":
		createUserCmd(os.Args[2:])
	case "list-users":
		listUsersCmd(os.Args[2:])
	case "rotate-key":
		rotateKeyCmd(os.Args[2:])
	case "ban-ip":
		banIPCmd(os.Args[2:])
	case "unban-ip":
		unbanIPCmd(os.Args[2:])
	case "list-bans":
		listBansCmd(os.Args[2:])
	case "admin-token":
		adminTokenCmd(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`tower - central hub server

Commands:
  serve         Start HTTP server
  create-user   Create a new user
  list-users    List users
  rotate-key    Rotate message key for a user
  ban-ip        Ban an IP manually
  unban-ip      Remove IP ban
  list-bans     List banned IPs
  admin-token   Print admin token
`)
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

func ensureAdmin(d *db.DB) (string, error) {
	// admin user
	if _, ok, err := d.GetUser("admin"); err != nil {
		return "", err
	} else if !ok {
		key, err := config.NewToken(24)
		if err != nil {
			return "", err
		}
		_ = d.CreateUser(db.User{ID: "admin", Name: "Admin", MessageKey: key, CreatedAt: time.Now()})
	}
	// admin token
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
	ui := fs.Bool("ui", true, "enable admin UI")
	fs.Parse(args)

	d := openDB(*dataDir)
	defer d.Close()
	adminToken, err := ensureAdmin(d)
	if err != nil {
		log.Fatalf("admin: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataDir = *dataDir
	cfg.Addr = *addr
	cfg.UIEnabled = *ui
	cfg.AdminToken = adminToken

	lim := logic.NewLimiter(cfg, d)
	if err := lim.LoadBans(); err != nil {
		log.Fatalf("load bans: %v", err)
	}

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

func createUserCmd(args []string) {
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	dataDir := commonFlags(fs)
	name := fs.String("name", "", "user name")
	id := fs.String("id", "", "user id (optional)")
	fs.Parse(args)

	if strings.TrimSpace(*name) == "" {
		log.Fatal("--name required")
	}
	if *id == "" {
		*id = strings.ToLower(strings.ReplaceAll(*name, " ", "-"))
	}
	key, _ := config.NewToken(24)

	d := openDB(*dataDir)
	defer d.Close()
	if err := d.CreateUser(db.User{ID: *id, Name: *name, MessageKey: key, CreatedAt: time.Now()}); err != nil {
		log.Fatalf("create user: %v", err)
	}
	fmt.Printf("user_id=%s\nmessage_key=%s\n", *id, key)
}

func listUsersCmd(args []string) {
	fs := flag.NewFlagSet("list-users", flag.ExitOnError)
	dataDir := commonFlags(fs)
	fs.Parse(args)

	d := openDB(*dataDir)
	defer d.Close()
	users, err := d.ListUsers()
	if err != nil {
		log.Fatalf("list users: %v", err)
	}
	for _, u := range users {
		fmt.Printf("%s\t%s\t%s\n", u.ID, u.Name, u.MessageKey)
	}
}

func rotateKeyCmd(args []string) {
	fs := flag.NewFlagSet("rotate-key", flag.ExitOnError)
	dataDir := commonFlags(fs)
	id := fs.String("id", "", "user id")
	fs.Parse(args)

	if *id == "" {
		log.Fatal("--id required")
	}
	key, _ := config.NewToken(24)

	d := openDB(*dataDir)
	defer d.Close()
	if err := d.UpdateUserKey(*id, key); err != nil {
		log.Fatalf("rotate key: %v", err)
	}
	fmt.Printf("user_id=%s\nmessage_key=%s\n", *id, key)
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

func adminTokenCmd(args []string) {
	fs := flag.NewFlagSet("admin-token", flag.ExitOnError)
	dataDir := commonFlags(fs)
	fs.Parse(args)

	d := openDB(*dataDir)
	defer d.Close()
	tok, err := ensureAdmin(d)
	if err != nil {
		log.Fatalf("admin token: %v", err)
	}
	fmt.Printf("%s\n", tok)
}
