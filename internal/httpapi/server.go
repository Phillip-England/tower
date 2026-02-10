package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"tower/internal/config"
	"tower/internal/db"
	"tower/internal/logic"
	"tower/internal/ui"
)

type Server struct {
	cfg        config.Config
	db         *db.DB
	limiter    *logic.Limiter
	adminToken string
	tmpl       *template.Template
}

func NewServer(cfg config.Config, d *db.DB, lim *logic.Limiter, adminToken string) (*Server, error) {
	tmpl, err := template.New("ui").Parse(ui.Template)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, db: d, limiter: lim, adminToken: adminToken, tmpl: tmpl}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/v1/log", s.authUser(s.handleLog))
	mux.HandleFunc("/api/v1/messages", s.authUser(s.handleMessages))
	mux.HandleFunc("/api/v1/messages/", s.authUser(s.handleMessageID))

	if s.cfg.UIEnabled {
		mux.HandleFunc("/ui", s.adminOnly(s.uiIndex))
		mux.HandleFunc("/ui/users", s.adminOnly(s.uiCreateUser))
		mux.HandleFunc("/ui/bans/unban", s.adminOnly(s.uiUnban))
	}
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

type authUserContext struct {
	User db.User
	IP   string
}

type ctxKey string

const userCtxKey ctxKey = "user"

func (s *Server) authUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-Tower-User")
		key := r.Header.Get("X-Tower-Key")
		if userID == "" || key == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing auth headers"})
			return
		}
		user, ok, err := s.db.GetUser(userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		if !ok || user.MessageKey != key {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
			return
		}
		ip := logic.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
		if banned, b := s.limiter.IsBanned(ip); banned {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "ip banned", "reason": b.Reason})
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), userCtxKey, authUserContext{User: user, IP: ip}))
		next(w, r)
	}
}

func userFrom(r *http.Request) authUserContext {
	v := r.Context().Value(userCtxKey)
	if v == nil {
		return authUserContext{}
	}
	return v.(authUserContext)
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	ctx := userFrom(r)
	var payload struct {
		IP     string `json:"ip"`
		Method string `json:"method"`
		Path   string `json:"path"`
		UserID string `json:"user_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	ip := payload.IP
	if ip == "" {
		ip = ctx.IP
	}
	method := payload.Method
	if method == "" {
		method = r.Method
	}
	p := payload.Path
	if p == "" {
		p = r.URL.Path
	}

	throttled, banned := s.limiter.LogRequest(logic.RequestLog{
		Time:   time.Now(),
		IP:     ip,
		Method: method,
		Path:   p,
		UserID: ctx.User.ID,
	})
	if banned {
		_, _ = s.limiter.RecordBan(ip, "auto-ban: repeated throttling")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "ip banned"})
		return
	}
	if throttled {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "throttled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	ctx := userFrom(r)
	if r.Method == http.MethodPost {
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.Body) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
			return
		}
		if !s.limiter.CanSendMessage(ctx.User.ID) {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "message throttled"})
			return
		}
		id, err := s.db.CreateMessage(db.Message{UserID: ctx.User.ID, Body: payload.Body, CreatedAt: time.Now()})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"id": id})
		return
	}
	if r.Method == http.MethodGet {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		msgs, err := s.db.ListMessages(ctx.User.ID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
			return
		}
		writeJSON(w, http.StatusOK, msgs)
		return
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
}

func (s *Server) handleMessageID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/v1/messages/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := s.db.DeleteMessage(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Admin-Token")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if tok == "" || tok != s.adminToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid admin token"})
			return
		}
		next(w, r)
	}
}

func (s *Server) uiIndex(w http.ResponseWriter, r *http.Request) {
	users, _ := s.db.ListUsers()
	bans, _ := s.db.ListBans()
	requests := s.limiter.RecentRequests()
	data := ui.ViewData{
		AdminToken: s.adminToken,
		Users:      users,
		Bans:       bans,
		Requests:   requests,
	}
	_ = s.tmpl.Execute(w, data)
}

func (s *Server) uiCreateUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if id == "" {
		id = fmt.Sprintf("user-%d", time.Now().Unix())
	}
	key, _ := config.NewToken(24)
	_ = s.db.CreateUser(db.User{ID: id, Name: name, MessageKey: key, CreatedAt: time.Now()})
	http.Redirect(w, r, addToken("/ui", s.adminToken), http.StatusSeeOther)
}

func (s *Server) uiUnban(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ip := strings.TrimSpace(r.FormValue("ip"))
	if ip == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = s.limiter.Unban(ip)
	http.Redirect(w, r, addToken("/ui", s.adminToken), http.StatusSeeOther)
}

func addToken(p, token string) string {
	return fmt.Sprintf("%s?token=%s", p, token)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
