// Package server is the Vaultkeeper manager: web UI + API, agent coordination,
// scheduling and notifications.
package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/bcrypt"

	"vaultkeeper/internal/store"
	"vaultkeeper/web"
)

type Server struct {
	st  *store.Store
	mux *http.ServeMux

	cron    *cron.Cron
	cronMu  sync.Mutex
	entries map[string]cron.EntryID

	failMu sync.Mutex
	fails  map[string][]time.Time

	info Info

	wakeMu sync.Mutex
	wakes  map[string]chan struct{}
	dls    sync.Map // run id -> *download (streaming browser downloads)
}

func New(st *store.Store) (*Server, error) {
	s := &Server{st: st, mux: http.NewServeMux(), entries: map[string]cron.EntryID{}, fails: map[string][]time.Time{}, wakes: map[string]chan struct{}{}}
	if err := s.bootstrap(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

func rid(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) settings() store.Settings {
	var cfg store.Settings
	_ = s.st.Get(store.KindSettings, "main", &cfg)
	return cfg
}

func (s *Server) saveSettings(fn func(*store.Settings)) error {
	return store.Update(s.st, store.KindSettings, "main", func(c *store.Settings) error { fn(c); return nil })
}

// bootstrap initialises settings on first run (admin password, secrets).
func (s *Server) bootstrap() error {
	var cfg store.Settings
	if err := s.st.Get(store.KindSettings, "main", &cfg); err != nil && err != store.ErrNotFound {
		return err
	}
	changed := false
	if cfg.SessionSecret == "" {
		cfg.SessionSecret = rid(32)
		changed = true
	}
	// VK_ENROLL_TOKEN lets unattended deployments (compose, config management) preset the token.
	if t := os.Getenv("VK_ENROLL_TOKEN"); t != "" && t != cfg.EnrollToken {
		cfg.EnrollToken = t
		changed = true
	}
	if cfg.EnrollToken == "" {
		cfg.EnrollToken = rid(16)
		changed = true
	}
	if cfg.AdminHash == "" {
		pw := os.Getenv("VK_ADMIN_PASSWORD")
		generated := false
		if pw == "" {
			pw = rid(8)
			generated = true
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		cfg.AdminHash = string(h)
		changed = true
		if generated {
			log.Printf("=====================================================")
			log.Printf(" First run: admin password is  %s", pw)
			log.Printf(" (change it under Settings; set VK_ADMIN_PASSWORD to choose your own)")
			log.Printf("=====================================================")
		}
	}
	if cfg.SMTPPort == 0 {
		cfg.SMTPPort = 587
		cfg.SMTPSecurity = "starttls"
		cfg.EmailLevel = "warning"
		cfg.LogRetentionDays = 90
		changed = true
	}
	if cfg.SettingsRev < 2 { // defaults for settings added after the first release
		cfg.SettingsRev, cfg.DiskAlertPct, cfg.AutoBackupKeep = 2, 10, 14
		changed = true
	}
	if changed {
		return s.st.Put(store.KindSettings, "main", cfg)
	}
	return nil
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, format string, a ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, a...)})
}

func readJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 16<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

// ---------- auth ----------

const cookieName = "vk_session"

func (s *Server) sign(exp int64) string {
	secret := s.settings().SessionSecret
	payload := strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (s *Server) validSession(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	exp, err := strconv.ParseInt(string(pb), 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.sign(exp))) == 1
}

func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validSession(r) {
			httpErr(w, http.StatusUnauthorized, "not logged in")
			return
		}
		// Mutating requests must be JSON (blocks simple cross-site form posts).
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodDelete {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				httpErr(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
				return
			}
		}
		h(w, r)
	}
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func (s *Server) throttled(ip string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	cut := time.Now().Add(-5 * time.Minute)
	var keep []time.Time
	for _, t := range s.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	s.fails[ip] = keep
	return len(keep) >= 10
}

func (s *Server) noteFail(ip string) {
	s.failMu.Lock()
	s.fails[ip] = append(s.fails[ip], time.Now())
	s.failMu.Unlock()
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.throttled(ip) {
		httpErr(w, http.StatusTooManyRequests, "too many failed attempts; try again in a few minutes")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if readJSON(r, &in) != nil {
		httpErr(w, 400, "bad request")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(s.settings().AdminHash), []byte(in.Password)) != nil {
		s.noteFail(ip)
		httpErr(w, http.StatusUnauthorized, "incorrect password")
		return
	}
	exp := time.Now().Add(7 * 24 * time.Hour)
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.sign(exp.Unix()), Path: "/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---------- routing ----------

func (s *Server) routes() {
	m := s.mux
	// Public.
	m.HandleFunc("POST /api/login", s.handleLogin)
	m.HandleFunc("POST /api/logout", s.handleLogout)
	m.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"authenticated": s.validSession(r)})
	})
	// Agents.
	m.HandleFunc("POST /api/agent/register", s.agentRegister)
	m.HandleFunc("POST /api/agent/poll", s.agentAuth(s.agentPoll))
	m.HandleFunc("POST /api/agent/tasks/{id}/log", s.agentAuth(s.agentLog))
	m.HandleFunc("POST /api/agent/tasks/{id}/result", s.agentAuth(s.agentResult))
	m.HandleFunc("POST /api/agent/tasks/{id}/data", s.agentAuth(s.agentData))
	// Operator API.
	a := s.requireAuth
	m.HandleFunc("GET /api/dashboard", a(s.apiDashboard))
	m.HandleFunc("GET /api/system", a(s.apiSystem))
	m.HandleFunc("POST /api/system/export", a(s.apiExport))
	m.HandleFunc("POST /api/system/import", a(s.apiImport))
	m.HandleFunc("POST /api/system/auto-backup", a(s.apiAutoBackupNow))
	m.HandleFunc("GET /api/agents", a(s.apiAgents))
	m.HandleFunc("PUT /api/agents/{id}", a(s.apiAgentRename))
	m.HandleFunc("DELETE /api/agents/{id}", a(s.apiAgentDelete))
	m.HandleFunc("PUT /api/agents/{id}/config", a(s.apiAgentConfig))
	m.HandleFunc("POST /api/agents/{id}/restart", a(s.apiAgentRestart))
	m.HandleFunc("GET /api/jobs", a(s.apiJobs))
	m.HandleFunc("POST /api/jobs", a(s.apiJobSave))
	m.HandleFunc("GET /api/jobs/{id}", a(s.apiJobGet))
	m.HandleFunc("PUT /api/jobs/{id}", a(s.apiJobSave))
	m.HandleFunc("DELETE /api/jobs/{id}", a(s.apiJobDelete))
	m.HandleFunc("POST /api/jobs/{id}/run", a(s.apiJobRun))
	m.HandleFunc("GET /api/jobs/{id}/snapshots", a(s.apiJobSnapshots))
	m.HandleFunc("GET /api/jobs/{id}/key", a(s.apiJobKey))
	m.HandleFunc("GET /api/jobs/{id}/browse", a(s.apiBrowse))
	m.HandleFunc("GET /api/jobs/{id}/search", a(s.apiSearch))
	m.HandleFunc("GET /api/jobs/{id}/download", s.requireAuth(s.apiDownload))
	m.HandleFunc("POST /api/jobs/{id}/restore", a(s.apiRestore))
	m.HandleFunc("DELETE /api/jobs/{id}/snapshots/{snap}", a(s.apiSnapshotDelete))
	m.HandleFunc("POST /api/agents/{id}/purge-repo", a(s.apiPurgeRepo))
	m.HandleFunc("GET /api/mirrors", a(s.apiMirrors))
	m.HandleFunc("POST /api/mirrors", a(s.apiMirrorSave))
	m.HandleFunc("PUT /api/mirrors/{id}", a(s.apiMirrorSave))
	m.HandleFunc("DELETE /api/mirrors/{id}", a(s.apiMirrorDelete))
	m.HandleFunc("POST /api/mirrors/{id}/run", a(s.apiMirrorRun))
	m.HandleFunc("GET /api/copyjobs", a(s.apiCopies))
	m.HandleFunc("POST /api/copyjobs", a(s.apiCopySave))
	m.HandleFunc("PUT /api/copyjobs/{id}", a(s.apiCopySave))
	m.HandleFunc("DELETE /api/copyjobs/{id}", a(s.apiCopyDelete))
	m.HandleFunc("POST /api/copyjobs/{id}/run", a(s.apiCopyRun))
	m.HandleFunc("POST /api/copyjobs/{id}/prune", a(s.apiCopyPrune))
	m.HandleFunc("GET /api/copyjobs/{id}/key", a(s.apiCopyKey))
	m.HandleFunc("GET /api/runs", a(s.apiRuns))
	m.HandleFunc("GET /api/runs/{id}", a(s.apiRunGet))
	m.HandleFunc("POST /api/runs/{id}/cancel", a(s.apiRunCancel))
	m.HandleFunc("GET /api/logs", a(s.apiLogs))
	m.HandleFunc("GET /api/settings", a(s.apiSettingsGet))
	m.HandleFunc("PUT /api/settings", a(s.apiSettingsPut))
	m.HandleFunc("POST /api/settings/test-email", a(s.apiTestEmail))
	m.HandleFunc("POST /api/settings/password", a(s.apiPassword))
	m.HandleFunc("POST /api/settings/enroll-token", a(s.apiRotateEnroll))

	sub, _ := fs.Sub(web.Static, "static")
	fsrv := http.FileServer(http.FS(sub))
	m.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		fsrv.ServeHTTP(w, r)
	}))
}
