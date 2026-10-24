package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Base cookie names. A deployment with secure cookies and no KY_COOKIE_DOMAIN sends them
// with the __Host- prefix (CookieName), so a sibling subdomain cannot plant or shadow them.
const (
	SessionCookieName = "ky_session"
	CSRFCookieName    = "ky_csrf"
	HeaderCSRF        = "X-CSRF-Token"
)

// CookieName is base with the __Host- prefix when the cookie can carry it: browsers accept
// the prefix only on a Secure, host-only cookie with Path=/.
func CookieName(base string, cfg config.SecurityConfig) string {
	if cfg.CookieSecure && cfg.CookieDomain == "" {
		return "__Host-" + base
	}
	return base
}

var ErrPasswordChangeRequired = errors.New("password change required")

type SessionManager struct {
	store  store.Store
	config config.SecurityConfig
}

func NewSessionManager(st store.Store, cfg config.SecurityConfig) *SessionManager {
	return &SessionManager{
		store:  st,
		config: cfg,
	}
}

// IssueSession creates an active session for credentials verified just now and writes the
// HttpOnly cookie + CSRF token.
func (sm *SessionManager) IssueSession(ctx context.Context, w http.ResponseWriter, r *http.Request, user *store.User) (*store.Session, string, error) {
	return sm.IssueDerivedSession(ctx, w, r, user, time.Now().UTC())
}

// IssueDerivedSession issues a session whose credentials were verified at authenticatedAt,
// for a session minted from another one (device pairing) without a fresh sign-in.
func (sm *SessionManager) IssueDerivedSession(ctx context.Context, w http.ResponseWriter, r *http.Request, user *store.User, authenticatedAt time.Time) (*store.Session, string, error) {
	rawToken := crypto.RandomHex(32)
	tokenHash := crypto.SHA256Hex([]byte(rawToken))

	ttl := sm.config.SessionTTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}

	sess := &store.Session{
		TokenHash: tokenHash,
		UserID:    user.ID,
		UserAgent: r.UserAgent(),
		IPAddress: ClientIP(r, sm.config.TrustedProxies),
		CreatedAt: authenticatedAt,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}

	if err := sm.store.Sessions().CreateSession(ctx, sess, user.PasswordHash); err != nil {
		return nil, "", err
	}

	// Set HttpOnly session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(SessionCookieName, sm.config),
		Value:    rawToken,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		Secure:   sm.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Domain:   sm.config.CookieDomain,
	})

	// The CSRF token is bound to this session (CSRFToken), never a free-standing random value.
	sm.writeCSRFCookie(w, rawToken, sess.ExpiresAt)

	return sess, rawToken, nil
}

// AuthenticateRequest extracts the session token from cookie or Authorization header and returns the user.
func (sm *SessionManager) AuthenticateRequest(r *http.Request) (*store.User, *store.Session, error) {
	var rawToken string

	// 1. Check Bearer token header
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		rawToken = strings.TrimPrefix(authHeader, "Bearer ")
	}

	// 2. Fallback to HttpOnly cookie
	if rawToken == "" {
		if cookie, err := r.Cookie(CookieName(SessionCookieName, sm.config)); err == nil && cookie.Value != "" {
			rawToken = cookie.Value
		}
	}

	if rawToken == "" {
		return nil, nil, store.ErrNotFound
	}

	tokenHash := crypto.SHA256Hex([]byte(rawToken))
	sess, err := sm.store.Sessions().GetSession(r.Context(), tokenHash)
	if err != nil {
		return nil, nil, err
	}

	user, err := sm.store.Users().GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		return nil, nil, err
	}
	allowed, ownershipErr := sm.store.InstallerUserAllowed(r.Context(), user)
	if ownershipErr != nil || !allowed || user.Status != "active" {
		_ = sm.store.Sessions().DeleteSession(r.Context(), tokenHash)
		return nil, nil, store.ErrNotFound
	}

	// The password transaction may revoke the session between the two reads.
	// Recheck after loading the user so an old session cannot inherit a cleared flag.
	if _, err := sm.store.Sessions().GetSession(r.Context(), tokenHash); err != nil {
		return nil, nil, err
	}

	// A restricted session never authorizes product operations or private settings.
	if user.MustChangePassword && !((r.Method == http.MethodGet && r.URL.Path == "/api/auth/me") || (r.Method == http.MethodPost && (r.URL.Path == "/api/auth/change-password" || r.URL.Path == "/api/auth/logout"))) {
		return nil, nil, ErrPasswordChangeRequired
	}
	return user, sess, nil
}

// HasSessionCookie reports whether the request carries a session cookie.
func (sm *SessionManager) HasSessionCookie(r *http.Request) bool {
	cookie, err := r.Cookie(CookieName(SessionCookieName, sm.config))
	return err == nil && cookie.Value != ""
}

// SessionToken is the raw session token the request's cookie presents, or "".
func (sm *SessionManager) SessionToken(r *http.Request) string {
	if cookie, err := r.Cookie(CookieName(SessionCookieName, sm.config)); err == nil {
		return cookie.Value
	}
	return ""
}

// CSRFToken is derived from the session token under the server secret, so only the
// session's own page can know it. The "ky-csrf:" label keeps it apart from the PoW
// challenge signatures, whose payload always starts with a hex salt.
func (sm *SessionManager) CSRFToken(rawSession string) string {
	return crypto.ComputeHMACSHA256([]byte("ky-csrf:"+rawSession), sm.config.SessionSecret)
}

// ValidateCSRF compares the header, in constant time, with the token recomputed from the
// session cookie. The CSRF cookie only carries the value to the SPA: a pair planted from a
// sibling subdomain, or copied from another session, proves nothing.
func (sm *SessionManager) ValidateCSRF(r *http.Request) bool {
	raw, header := sm.SessionToken(r), r.Header.Get(HeaderCSRF)
	return raw != "" && header != "" && crypto.VerifyHMACSHA256([]byte("ky-csrf:"+raw), sm.config.SessionSecret, header)
}

// RefreshCSRF rewrites the CSRF cookie for the presented session cookie: a lost cookie, or
// one from before tokens were bound to the session. A request authenticated by a Bearer
// token for another session gets nothing.
func (sm *SessionManager) RefreshCSRF(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if raw := sm.SessionToken(r); raw != "" && crypto.SHA256Hex([]byte(raw)) == sess.TokenHash {
		sm.writeCSRFCookie(w, raw, sess.ExpiresAt)
	}
}

func (sm *SessionManager) writeCSRFCookie(w http.ResponseWriter, rawSession string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(CSRFCookieName, sm.config),
		Value:    sm.CSRFToken(rawSession),
		Path:     "/",
		Expires:  expires,
		HttpOnly: false, // Read by JS to put into X-CSRF-Token header
		Secure:   sm.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Domain:   sm.config.CookieDomain,
	})
}

// RevokeSession deletes the active session and clears the browser cookies.
func (sm *SessionManager) RevokeSession(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	rawToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		rawToken = ""
	}
	if rawToken == "" {
		if cookie, err := r.Cookie(CookieName(SessionCookieName, sm.config)); err == nil {
			rawToken = cookie.Value
		}
	}

	if rawToken != "" {
		tokenHash := crypto.SHA256Hex([]byte(rawToken))
		_ = sm.store.Sessions().DeleteSession(ctx, tokenHash)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(SessionCookieName, sm.config),
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   sm.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Domain:   sm.config.CookieDomain,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName(CSRFCookieName, sm.config),
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		HttpOnly: false,
		Secure:   sm.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		Domain:   sm.config.CookieDomain,
	})

	return nil
}
