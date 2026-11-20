package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	dashboardPath            = "/dashboard/api"
	dashboardCookie          = "seavx_dashboard"
	dashboardDefaultPassword = "seavx12345"
	dashboardSessionTTL      = 12 * time.Hour
	dashboardSetupTTL        = 10 * time.Minute
)

type DashboardStore struct {
	db        *sql.DB
	loginMu   sync.Mutex
	nextLogin time.Time
}

func OpenDashboardStore(path string) (*DashboardStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initDashboard(db); err != nil {
		db.Close()
		return nil, err
	}
	return &DashboardStore{db: db}, nil
}

func initDashboard(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS dashboard_admin (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		password_hash BLOB NOT NULL,
		must_change INTEGER NOT NULL DEFAULT 1,
		session_hash BLOB,
		session_expires INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return err
	}
	var id int
	err = db.QueryRow("SELECT id FROM dashboard_admin WHERE id = 1").Scan(&id)
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(dashboardDefaultPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT OR IGNORE INTO dashboard_admin (id, password_hash) VALUES (1, ?)", hash)
	return err
}

func (d *DashboardStore) Close() error { return d.db.Close() }

func WithDashboard(store *DashboardStore) ServerOption {
	return func(s *Server) { s.dashboard = store }
}

func dashboardError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": message}})
}

func dashboardInternalError(w http.ResponseWriter, err error) {
	slog.Error("dashboard operation failed", "err", err)
	dashboardError(w, http.StatusInternalServerError, "Dashboard storage is unavailable.")
}

func dashboardSession(r *http.Request) [32]byte {
	if cookie, err := r.Cookie(dashboardCookie); err == nil {
		return sha256.Sum256([]byte(cookie.Value))
	}
	return [32]byte{}
}

func (d *DashboardStore) session(r *http.Request) (bool, error) {
	hash := dashboardSession(r)
	var mustChange bool
	err := d.db.QueryRowContext(r.Context(), `SELECT must_change FROM dashboard_admin
		WHERE session_hash = ? AND session_expires > ?`, hash[:], time.Now().Unix()).Scan(&mustChange)
	return mustChange, err
}

func (d *DashboardStore) requireSession(w http.ResponseWriter, r *http.Request) bool {
	mustChange, err := d.session(r)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		dashboardError(w, http.StatusUnauthorized, "Sign in to the dashboard.")
	case err != nil:
		dashboardInternalError(w, err)
	case mustChange:
		dashboardError(w, http.StatusForbidden, "Change the default password to continue.")
	default:
		return true
	}
	return false
}

func loopbackHost(host string) bool {
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func dashboardCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: dashboardCookie, Value: value, Path: dashboardPath, MaxAge: maxAge,
		HttpOnly: true, Secure: r.TLS != nil || !loopbackHost(r.Host), SameSite: http.SameSiteStrictMode,
	}
}

func dashboardToken() (string, [32]byte) {
	var value [32]byte
	rand.Read(value[:])
	token := base64.RawURLEncoding.EncodeToString(value[:])
	return token, sha256.Sum256([]byte(token))
}

func readDashboardPassword(w http.ResponseWriter, r *http.Request) (string, bool) {
	var form struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&form); err != nil || len(form.Password) == 0 || len(form.Password) > 72 {
		dashboardError(w, http.StatusBadRequest, "Enter a password of 1 to 72 bytes.")
		return "", false
	}
	return form.Password, true
}

func (d *DashboardStore) login(w http.ResponseWriter, r *http.Request) {
	password, ok := readDashboardPassword(w, r)
	if !ok {
		return
	}
	d.loginMu.Lock()
	if time.Now().Before(d.nextLogin) {
		d.loginMu.Unlock()
		w.Header().Set("Retry-After", "1")
		dashboardError(w, http.StatusTooManyRequests, "Wait a moment before trying again.")
		return
	}
	d.nextLogin = time.Now().Add(time.Second)
	d.loginMu.Unlock()

	var hash []byte
	var mustChange bool
	err := d.db.QueryRowContext(r.Context(), "SELECT password_hash, must_change FROM dashboard_admin WHERE id = 1").Scan(&hash, &mustChange)
	if err != nil {
		dashboardInternalError(w, err)
		return
	}
	// The well-known password may only be used through a loopback host and peer.
	if mustChange && (!loopbackHost(r.Host) || !loopbackHost(r.RemoteAddr)) {
		dashboardError(w, http.StatusForbidden, "Set up the dashboard through localhost first.")
		return
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
		dashboardError(w, http.StatusUnauthorized, "Incorrect password.")
		return
	}

	ttl := dashboardSessionTTL
	if mustChange {
		ttl = dashboardSetupTTL
	}
	token, digest := dashboardToken()
	res, err := d.db.ExecContext(r.Context(), `UPDATE dashboard_admin
		SET session_hash = ?, session_expires = ? WHERE id = 1 AND password_hash = ?`, digest[:], time.Now().Add(ttl).Unix(), hash)
	if !dashboardUpdated(w, res, err) {
		return
	}
	http.SetCookie(w, dashboardCookieFor(r, token, int(ttl.Seconds())))
	writeJSON(w, map[string]bool{"authenticated": true, "must_change": mustChange})
}

func dashboardUpdated(w http.ResponseWriter, res sql.Result, err error) bool {
	if err == nil {
		var count int64
		count, err = res.RowsAffected()
		if err == nil {
			if count == 1 {
				return true
			}
			dashboardError(w, http.StatusConflict, "Session changed. Sign in again.")
			return false
		}
	}
	dashboardInternalError(w, err)
	return false
}

func (d *DashboardStore) changePassword(w http.ResponseWriter, r *http.Request) {
	mustChange, err := d.session(r)
	if errors.Is(err, sql.ErrNoRows) {
		dashboardError(w, http.StatusUnauthorized, "Sign in to the dashboard.")
		return
	}
	if err != nil {
		dashboardInternalError(w, err)
		return
	}
	if !mustChange {
		dashboardError(w, http.StatusConflict, "Initial setup has already finished.")
		return
	}
	password, ok := readDashboardPassword(w, r)
	if !ok {
		return
	}
	if len([]rune(password)) < 12 {
		dashboardError(w, http.StatusBadRequest, "Use a new password with at least 12 characters.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		dashboardInternalError(w, err)
		return
	}
	oldSession := dashboardSession(r)
	token, digest := dashboardToken()
	res, err := d.db.ExecContext(r.Context(), `UPDATE dashboard_admin SET
		password_hash = ?, must_change = 0, session_hash = ?, session_expires = ?
		WHERE id = 1 AND must_change = 1 AND session_hash = ? AND session_expires > ?`,
		hash, digest[:], time.Now().Add(dashboardSessionTTL).Unix(), oldSession[:], time.Now().Unix())
	if !dashboardUpdated(w, res, err) {
		return
	}
	http.SetCookie(w, dashboardCookieFor(r, token, int(dashboardSessionTTL.Seconds())))
	writeJSON(w, map[string]bool{"authenticated": true, "must_change": false})
}

func (d *DashboardStore) logout(w http.ResponseWriter, r *http.Request) {
	hash := dashboardSession(r)
	_, err := d.db.ExecContext(r.Context(), `UPDATE dashboard_admin SET session_hash = NULL,
		session_expires = 0 WHERE session_hash = ?`, hash[:])
	if err != nil {
		dashboardInternalError(w, err)
		return
	}
	http.SetCookie(w, dashboardCookieFor(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dashboardHandler() http.Handler {
	d := s.dashboard
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+dashboardPath+"/login", d.login)
	mux.HandleFunc("POST "+dashboardPath+"/password", d.changePassword)
	mux.HandleFunc("POST "+dashboardPath+"/logout", d.logout)
	mux.HandleFunc("GET "+dashboardPath+"/session", func(w http.ResponseWriter, r *http.Request) {
		mustChange, err := d.session(r)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			dashboardInternalError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"authenticated": err == nil, "must_change": mustChange})
	})
	admin := func(pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if d.requireSession(w, r) {
				handler(w, r)
			}
		})
	}
	admin("GET "+dashboardPath+"/models", s.wire.HandleModels)
	admin("GET "+dashboardPath+"/quota", s.handleQuota)
	admin("GET "+dashboardPath+"/usage", s.handleUsage)
	admin("GET "+dashboardPath+"/logging", s.handleLogging)
	admin("GET "+dashboardPath+"/logging/{id}", s.handleLog)
	admin("GET "+dashboardPath+"/logging/stream", func(w http.ResponseWriter, r *http.Request) {
		s.streamLogs(w, r, func() bool {
			mustChange, err := d.session(r)
			return err == nil && !mustChange
		})
	})
	if s.keys != nil {
		admin("GET "+dashboardPath+"/keys", s.requireKeysAdmin(s.handleListKeys))
		admin("POST "+dashboardPath+"/keys", s.requireKeysAdmin(s.handleCreateKey))
		admin("DELETE "+dashboardPath+"/keys/{id}", s.requireKeysAdmin(s.handleDeleteKey))
	}
	protected := http.NewCrossOriginProtection().Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		protected.ServeHTTP(w, r)
	})
}
