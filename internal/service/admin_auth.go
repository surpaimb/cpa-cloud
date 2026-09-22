package service

import (
	"crypto/subtle"
	"database/sql"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const adminCookieName = "cpa_admin_session"

type adminSession struct {
	SessionID string
	AdminID   string
	Username  string
	CSRFToken string
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !validOptionalOrigin(r, a.cfg.TLSCert != "") {
		writeAdminError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.")
		return
	}
	remote := clientAddress(r)
	if a.loginBlocked(remote) {
		w.Header().Set("Retry-After", "60")
		writeAdminError(w, http.StatusTooManyRequests, "login_limited", "Too many login attempts.")
		return
	}
	var input loginRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		a.recordLoginFailure(remote)
		return
	}
	if !validText(input.Username, 1, 120) {
		a.recordLoginFailure(remote)
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid login request.")
		return
	}
	if validateAdminPassword(input.Password) != nil {
		a.recordLoginFailure(remote)
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Administrator password must be 12 to 72 bytes.")
		return
	}
	var adminID, username string
	var hash []byte
	err := a.store.db.QueryRowContext(r.Context(), `SELECT id,username,password_hash FROM admins WHERE username=?`, input.Username).Scan(&adminID, &username, &hash)
	if err != nil || bcrypt.CompareHashAndPassword(hash, []byte(input.Password)) != nil {
		// Perform comparable work even when the username does not exist.
		if err == sql.ErrNoRows {
			_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$wJ5fLlDjsUYyrFKSGUN3LejJNhi2PMsS0KtqQffY2nzceZx9Nbm7a"), []byte(input.Password))
		}
		a.recordLoginFailure(remote)
		writeAdminError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.")
		return
	}
	a.clearLoginFailures(remote)
	token, err := randomToken(32)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	csrf, err := randomToken(32)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	sessionID, err := newID("ses")
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	expires := time.Now().UTC().Add(12 * time.Hour)
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`,
		sessionID, adminID, a.secrets.digest("admin-session/v1", token), csrf, expires.Format(time.RFC3339Nano), utcNow())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: adminCookieName, Value: token, Path: "/admin/", MaxAge: int((12 * time.Hour).Seconds()),
		HttpOnly: true, Secure: a.cfg.TLSCert != "", SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": csrf})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request, session adminSession) {
	_, _ = a.store.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE id=?`, session.SessionID)
	http.SetCookie(w, &http.Cookie{Name: adminCookieName, Value: "", Path: "/admin/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.TLSCert != "", SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) sessionInfo(w http.ResponseWriter, _ *http.Request, session adminSession) {
	writeJSON(w, http.StatusOK, map[string]string{"username": session.Username, "csrf_token": session.CSRFToken})
}

func (a *App) requireAdmin(next func(http.ResponseWriter, *http.Request, adminSession), write bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminCookieName)
		if err != nil || cookie.Value == "" {
			writeAdminError(w, http.StatusUnauthorized, "authentication_required", "Administrator authentication is required.")
			return
		}
		digest := a.secrets.digest("admin-session/v1", cookie.Value)
		var session adminSession
		var expires string
		err = a.store.db.QueryRowContext(r.Context(), `SELECT s.id,s.admin_id,a.username,s.csrf_token,s.expires_at FROM sessions s JOIN admins a ON a.id=s.admin_id WHERE s.token_digest=?`, digest).
			Scan(&session.SessionID, &session.AdminID, &session.Username, &session.CSRFToken, &expires)
		if err != nil {
			writeAdminError(w, http.StatusUnauthorized, "authentication_required", "Administrator authentication is required.")
			return
		}
		expiry, err := parseTime(expires)
		if err != nil || !time.Now().UTC().Before(expiry) {
			_, _ = a.store.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE id=?`, session.SessionID)
			writeAdminError(w, http.StatusUnauthorized, "session_expired", "Administrator session has expired.")
			return
		}
		if write {
			if !validRequiredOrigin(r, a.cfg.TLSCert != "") {
				writeAdminError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.")
				return
			}
			provided := r.Header.Get("X-CSRF-Token")
			if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(session.CSRFToken)) != 1 {
				writeAdminError(w, http.StatusForbidden, "csrf_rejected", "CSRF validation failed.")
				return
			}
		}
		next(w, r, session)
	}
}

func validOptionalOrigin(r *http.Request, tls bool) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || originMatches(r, origin, tls)
}

func validRequiredOrigin(r *http.Request, tls bool) bool {
	origin := r.Header.Get("Origin")
	return origin != "" && originMatches(r, origin, tls)
}

func originMatches(r *http.Request, origin string, tls bool) bool {
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return false
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	return strings.EqualFold(u.Scheme, scheme) && strings.EqualFold(u.Host, r.Host)
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (a *App) loginBlocked(address string) bool {
	now := time.Now()
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	entry := a.logins[address]
	if entry == nil {
		return false
	}
	return now.Before(entry.blockedTill)
}

func (a *App) recordLoginFailure(address string) {
	now := time.Now()
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	entry := a.logins[address]
	if entry == nil || now.Sub(entry.lastSeen) > 15*time.Minute {
		entry = &loginAttempt{}
		a.logins[address] = entry
	}
	entry.failures++
	entry.lastSeen = now
	if entry.failures >= 5 {
		delay := time.Duration(entry.failures-4) * time.Minute
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		entry.blockedTill = now.Add(delay)
	}
}

func (a *App) clearLoginFailures(address string) {
	a.loginMu.Lock()
	delete(a.logins, address)
	a.loginMu.Unlock()
}
