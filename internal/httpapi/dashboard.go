package httpapi

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	setupPath string
	setupHash [32]byte
	loginMu   sync.Mutex
	attempts  map[string]dashboardLoginAttempt
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
	store := &DashboardStore{db: db, setupPath: path + ".setup-token", attempts: make(map[string]dashboardLoginAttempt)}
	var pending bool
	if err := db.QueryRow("SELECT must_change FROM dashboard_admin WHERE id = 1").Scan(&pending); err != nil {
		db.Close()
		return nil, err
	}
	if pending {
		if err := store.loadSetupToken(); err != nil {
			db.Close()
			return nil, err
		}
	} else if err := os.Remove(store.setupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("could not remove consumed dashboard setup token file", "err", err)
	}
	return store, nil
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

func (d *DashboardStore) loadSetupToken() error {
	token, _ := dashboardToken()
	file, err := os.OpenFile(d.setupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, writeErr := file.WriteString(token + "\n")
		if err := errors.Join(writeErr, file.Close()); err != nil {
			_ = os.Remove(d.setupPath)
			return err
		}
	} else if errors.Is(err, os.ErrExist) {
		info, err := os.Lstat(d.setupPath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 128 {
			return fmt.Errorf("dashboard setup token must be a private regular file: %s", d.setupPath)
		}
		data, err := os.ReadFile(d.setupPath)
		if err != nil {
			return err
		}
		token = strings.TrimSpace(string(data))
	} else {
		return err
	}
	if raw, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(raw) != 32 {
		return fmt.Errorf("invalid dashboard setup token file: %s", d.setupPath)
	}
	d.setupHash = sha256.Sum256([]byte(token))
	slog.Info("dashboard setup token file ready", "path", d.setupPath)
	return nil
}

func (d *DashboardStore) validSetupToken(token string) bool {
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return subtle.ConstantTimeCompare(digest[:], d.setupHash[:]) == 1
}

type dashboardLoginAttempt struct {
	failures int
	next     time.Time
	busy     bool
}

func (d *DashboardStore) startLogin(peer string, now time.Time) (time.Duration, bool) {
	d.loginMu.Lock()
	defer d.loginMu.Unlock()
	attempt := d.attempts[peer]
	if attempt.busy {
		return time.Second, false
	}
	if now.Before(attempt.next) {
		return attempt.next.Sub(now), false
	}
	if len(d.attempts) >= 1024 {
		for ip, entry := range d.attempts {
			if !entry.busy && now.Sub(entry.next) > 10*time.Minute {
				delete(d.attempts, ip)
			}
		}
		if _, exists := d.attempts[peer]; !exists && len(d.attempts) >= 1024 {
			return time.Second, false
		}
	}
	if now.Sub(attempt.next) > 10*time.Minute {
		attempt.failures = 0
	}
	attempt.busy = true
	d.attempts[peer] = attempt
	return 0, true
}

func (d *DashboardStore) finishLogin(peer string, failed bool) {
	d.loginMu.Lock()
	defer d.loginMu.Unlock()
	if !failed {
		delete(d.attempts, peer)
		return
	}
	attempt := d.attempts[peer]
	attempt.failures = min(attempt.failures+1, 6)
	attempt.next = time.Now().Add(time.Second << (attempt.failures - 1))
	attempt.busy = false
	d.attempts[peer] = attempt
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

type dashboardPasswordForm struct {
	Password        string `json:"password"`
	CurrentPassword string `json:"current_password"`
	SetupToken      string `json:"setup_token"`
}

func readDashboardPassword(w http.ResponseWriter, r *http.Request) (dashboardPasswordForm, bool) {
	var form dashboardPasswordForm
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&form); err != nil || len(form.Password) == 0 || len(form.Password) > 72 {
		dashboardError(w, http.StatusBadRequest, "Enter a password of 1 to 72 bytes.")
		return dashboardPasswordForm{}, false
	}
	return form, true
}

func (d *DashboardStore) login(w http.ResponseWriter, r *http.Request) {
	form, ok := readDashboardPassword(w, r)
	if !ok {
		return
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = "unknown"
	}
	wait, allowed := d.startLogin(peer, time.Now())
	if !allowed {
		w.Header().Set("Retry-After", strconv.FormatInt(int64((wait+time.Second-1)/time.Second), 10))
		dashboardError(w, http.StatusTooManyRequests, "Wait before trying again.")
		return
	}
	failed := false
	defer func() { d.finishLogin(peer, failed) }()

	var hash []byte
	var mustChange bool
	err = d.db.QueryRowContext(r.Context(), "SELECT password_hash, must_change FROM dashboard_admin WHERE id = 1").Scan(&hash, &mustChange)
	if err != nil {
		dashboardInternalError(w, err)
		return
	}
	// The well-known password may only be used through a loopback host and peer.
	if mustChange && (!loopbackHost(r.Host) || !loopbackHost(r.RemoteAddr)) {
		dashboardError(w, http.StatusForbidden, "Set up the dashboard through localhost first.")
		return
	}
	if mustChange && !d.validSetupToken(form.SetupToken) {
		failed = true
		dashboardError(w, http.StatusUnauthorized, "A valid setup token is required.")
		return
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(form.Password)) != nil {
		failed = true
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
	oldSession := dashboardSession(r)
	var currentHash []byte
	var mustChange bool
	err := d.db.QueryRowContext(r.Context(), `SELECT password_hash, must_change FROM dashboard_admin
		WHERE session_hash = ? AND session_expires > ?`, oldSession[:], time.Now().Unix()).Scan(&currentHash, &mustChange)
	if errors.Is(err, sql.ErrNoRows) {
		dashboardError(w, http.StatusUnauthorized, "Sign in to the dashboard.")
		return
	}
	if err != nil {
		dashboardInternalError(w, err)
		return
	}
	form, ok := readDashboardPassword(w, r)
	if !ok {
		return
	}
	if len([]rune(form.Password)) < 12 {
		dashboardError(w, http.StatusBadRequest, "Use a new password with at least 12 characters.")
		return
	}
	if mustChange && !d.validSetupToken(form.SetupToken) {
		dashboardError(w, http.StatusForbidden, "A valid setup token is required.")
		return
	}
	if !mustChange && (len(form.CurrentPassword) > 72 || bcrypt.CompareHashAndPassword(currentHash, []byte(form.CurrentPassword)) != nil) {
		dashboardError(w, http.StatusForbidden, "Incorrect current password.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(form.Password), bcrypt.DefaultCost)
	if err != nil {
		dashboardInternalError(w, err)
		return
	}
	token, digest := dashboardToken()
	res, err := d.db.ExecContext(r.Context(), `UPDATE dashboard_admin SET
		password_hash = ?, must_change = 0, session_hash = ?, session_expires = ?
		WHERE id = 1 AND must_change = ? AND session_hash = ? AND session_expires > ?`,
		hash, digest[:], time.Now().Add(dashboardSessionTTL).Unix(), mustChange, oldSession[:], time.Now().Unix())
	if !dashboardUpdated(w, res, err) {
		return
	}
	if mustChange {
		if err := os.Remove(d.setupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("could not remove consumed dashboard setup token file", "err", err)
		}
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
		authenticated := err == nil
		if !authenticated {
			if err := d.db.QueryRowContext(r.Context(), "SELECT must_change FROM dashboard_admin WHERE id = 1").Scan(&mustChange); err != nil {
				dashboardInternalError(w, err)
				return
			}
		}
		writeJSON(w, map[string]bool{"authenticated": authenticated, "must_change": authenticated && mustChange, "setup_required": mustChange})
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
