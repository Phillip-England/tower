package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"tower/internal/config"
	"tower/internal/db"
	"tower/internal/logic"
)

type Server struct {
	cfg        config.Config
	db         *db.DB
	limiter    *logic.Limiter
	adminToken string
}

func NewServer(cfg config.Config, d *db.DB, lim *logic.Limiter, adminToken string) (*Server, error) {
	return &Server{cfg: cfg, db: d, limiter: lim, adminToken: adminToken}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/v1/inspect", s.authAPI(s.handleInspect))
	mux.HandleFunc("/api/v1/log", s.authAPI(s.handleLog))
	mux.HandleFunc("/api/v1/callbacks", s.authAPI(s.handleCallbacks))
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// authAPI authenticates API requests using the X-Tower-Key header.
func (s *Server) authAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Tower-Key")
		if key == "" || key != s.adminToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
			return
		}
		ip := logic.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
		if banned, b := s.limiter.IsBanned(ip); banned {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "ip banned", "reason": b.Reason})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var ip string
	if r.Method == http.MethodPost {
		var payload struct {
			IP string `json:"ip"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		ip = payload.IP
	}
	if ip == "" {
		ip = r.URL.Query().Get("ip")
	}
	if ip == "" {
		ip = logic.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
	}
	decision := s.limiter.Inspect(ip)
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		IP     string `json:"ip"`
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	ip := payload.IP
	if ip == "" {
		ip = logic.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
	}
	method := payload.Method
	if method == "" {
		method = r.Method
	}
	p := payload.Path
	if p == "" {
		p = r.URL.Path
	}

	decision := s.limiter.LogRequest(logic.RequestLog{
		Time:   time.Now(),
		IP:     ip,
		Method: method,
		Path:   p,
	})

	if decision.Action == logic.ActionBan {
		_, _ = s.limiter.RecordBan(ip, decision.Reason)
		s.limiter.NotifyCallbacks(decision)
		writeJSON(w, http.StatusForbidden, decision)
		return
	}
	if decision.Action == logic.ActionThrottle {
		s.limiter.NotifyCallbacks(decision)
		writeJSON(w, http.StatusTooManyRequests, decision)
		return
	}
	if decision.Action == logic.ActionFlag {
		s.limiter.NotifyCallbacks(decision)
	}
	writeJSON(w, http.StatusOK, decision)
}

func (s *Server) handleCallbacks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{"callbacks": s.limiter.Callbacks()})
	case http.MethodPost:
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.URL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
			return
		}
		s.limiter.RegisterCallback(payload.URL)
		writeJSON(w, http.StatusOK, map[string]string{"status": "registered"})
	case http.MethodDelete:
		var payload struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.URL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
			return
		}
		s.limiter.UnregisterCallback(payload.URL)
		writeJSON(w, http.StatusOK, map[string]string{"status": "unregistered"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
