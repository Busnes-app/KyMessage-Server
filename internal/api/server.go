package api

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/devices"
	"github.com/Busnes-app/ky_server_base/internal/scim"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/web"
)

// recoveryClient is the KyRecovery client as the handlers use it, narrowed so tests can stand
// in a fake without reaching the network.
type recoveryClient interface {
	ClaimPairing(ctx context.Context, serverURL, pairingCode, serviceName, appName string) (recoveryclient.PairingResult, error)
	recoveryclient.Depositor
}

type Server struct {
	config   *config.Config
	store    store.Store
	sessions *auth.SessionManager
	pairing  *devices.PairingService
	kysignon *sso.KySignOnClient
	oidc     *sso.GenericOIDCClient
	saml     *sso.SAMLServiceProvider
	scim     *scim.Server
	recovery recoveryClient
	mux      *http.ServeMux
	// clientAttempts throttles anonymous callers by address; accountAttempts throttles by
	// user ID. Separate maps, so anonymous traffic filling one cannot evict the other.
	clientAttempts  attemptLimiter
	accountAttempts attemptLimiter
	pow             auth.PoWSpender
	// detached counts the requests running on a context deliberately separated from their
	// connection. http.Server.Shutdown does not know about them, so runServer waits on this
	// before the store closes.
	detached detachedCounter
	live     messagingLiveRegistry
}

// detachedCounter is a WaitGroup that tolerates a registration arriving while the wait is
// already running. sync.WaitGroup panics on an Add from zero concurrent with Wait, and there is
// no barrier that rules that out here: Shutdown returns when its own timeout expires, with
// requests still in flight, so a second admin request can register just as the first finishes
// and drops the count to zero. A counter under a condition variable has no such rule.
type detachedCounter struct {
	once sync.Once
	mu   sync.Mutex
	cond *sync.Cond
	n    int
}

// signal builds the condition variable on first use, so the zero value of Server works.
func (d *detachedCounter) signal() *sync.Cond {
	d.once.Do(func() { d.cond = sync.NewCond(&d.mu) })
	return d.cond
}

func (d *detachedCounter) add() {
	c := d.signal()
	c.L.Lock()
	d.n++
	c.L.Unlock()
}

func (d *detachedCounter) done() {
	c := d.signal()
	c.L.Lock()
	d.n--
	c.L.Unlock()
	c.Broadcast()
}

// tracked counts a request as detached for as long as h runs. It wraps the auth middleware
// rather than the handler: requireAdmin authenticates against the store before the handler is
// reached, ReadTimeout (15s) outlasts cmd/server's shutdownTimeout (5s), and a SIGTERM landing
// during that lookup would otherwise leave the counter at zero, WaitDetached returning and the
// store closing under a request about to pin a key.
//
// The window before ServeHTTP is entered -- while net/http is still reading the request line
// and headers -- cannot be covered by any counter: there is no handler goroutine to register
// yet. Shutdown's own drain is all that covers it, which is why shutdownTimeout is spent first.
func (s *Server) tracked(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.detached.add()
		defer s.detached.done()
		h(w, r)
	}
}

// WaitDetached blocks until every request that detached from its connection has finished. It is
// called after http.Server.Shutdown and before the store is closed: pairing, the key pin and a
// deposit all keep writing after their connection is gone, and a closed store under them leaves
// a key pinned on disk with no row recording it.
func (s *Server) WaitDetached() {
	c := s.detached.signal()
	c.L.Lock()
	defer c.L.Unlock()
	for s.detached.n > 0 {
		c.Wait()
	}
}

type attemptWindow struct {
	count int
	reset time.Time
}

type attemptLimiter struct {
	mu sync.Mutex
	m  map[string]attemptWindow
	// cap bounds the map when callers influence the keys; zero means unbounded.
	cap int
}

// attemptsCap bounds the client limiter. Unauthenticated callers influence its keys, so the
// map is itself attack surface. At the cap we evict, never refuse: refusing every unknown key
// would let one caller fill the map and lock every new client out of login.
//
// The trade-off: memory is bounded, but an attacker who fills the map shortens other clients'
// windows, since an evicted counter starts again from zero. Per-account windows live in the
// separate, uncapped account limiter, whose keys are existing user IDs, so no flood of
// anonymous keys can reset them.
//
// Eviction is deliberately blind to how much of a window is left. Picking the entry nearest to
// expiry would always sacrifice the shortest windows first, so a caller minting keys with a
// long window could keep the one-minute login counter from ever reaching its limit. Every key
// is therefore equally likely to go. No key carries caller-supplied bytes, and IPv6 clients
// share one key per /64, so filling the map costs an attacker a distinct IPv4 address or /64.
const attemptsCap = 10000

func NewServer(cfg *config.Config, st store.Store) *Server {
	sessions := auth.NewSessionManager(st, cfg.Security)
	pairing := devices.NewPairingService(st, cfg.Server.AppName, cfg.Server.AppURL)
	kysignon := sso.NewKySignOnClient(cfg.SSO, st)
	oidc := sso.NewGenericOIDCClient(cfg.SSO, st)
	saml := sso.NewSAMLServiceProvider(cfg.SSO.SAMLEntityID, cfg.Server.AppURL+"/saml/acs")
	scimSrv := scim.NewServer(st, cfg.SCIM, cfg.Server.AppURL)
	recovery := recoveryclient.NewClient(recoveryclient.Options{AllowPrivate: cfg.Backup.AllowPrivateRecovery})

	s := &Server{
		config:          cfg,
		store:           st,
		sessions:        sessions,
		pairing:         pairing,
		kysignon:        kysignon,
		oidc:            oidc,
		saml:            saml,
		scim:            scimSrv,
		recovery:        recovery,
		mux:             http.NewServeMux(),
		clientAttempts:  attemptLimiter{m: make(map[string]attemptWindow), cap: attemptsCap},
		accountAttempts: attemptLimiter{m: make(map[string]attemptWindow)},
	}

	s.routes()
	return s
}

func (l *attemptLimiter) allow(key string, limit int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, known := l.m[key]; !known && l.cap > 0 && len(l.m) >= l.cap {
		l.makeRoom(now)
	}
	entry := bumpWindow(l.m[key], now, window)
	l.m[key] = entry
	return entry.count <= limit
}

// makeRoom frees a slot for a new key: it drops every expired window, and if the map is still
// full it drops one live entry chosen at random, never the one nearest expiry. Caller holds
// mu. The scan is O(cap) and only runs for a new key while the map is full; 10 000 entries is
// microseconds.
func (l *attemptLimiter) makeRoom(now time.Time) {
	for candidate, w := range l.m {
		if now.After(w.reset) {
			delete(l.m, candidate)
		}
	}
	if len(l.m) >= l.cap {
		// Go randomises map iteration, so the first entry is an unbiased victim.
		for candidate := range l.m {
			delete(l.m, candidate)
			break
		}
	}
}

// allowClientAttempt throttles an anonymous route per client. An IPv6 client is keyed by its
// /64: a single subscriber usually holds at least that much, and one key per address would let
// it mint a fresh window for every request.
func (s *Server) allowClientAttempt(scope string, r *http.Request, limit int, window time.Duration) bool {
	client := s.requestIP(r)
	if addr, err := netip.ParseAddr(client); err == nil && addr.Is6() {
		client = netip.PrefixFrom(addr, 64).Masked().String()
	}
	return s.clientAttempts.allow(scope+":"+client, limit, window)
}

// allowAccountAttempt throttles by an existing user's ID. Callers must never pass a
// caller-supplied string: this map is unbounded because its keys are not attacker-minted.
func (s *Server) allowAccountAttempt(key string, limit int, window time.Duration) bool {
	return s.accountAttempts.allow(key, limit, window)
}

func bumpWindow(entry attemptWindow, now time.Time, window time.Duration) attemptWindow {
	if now.After(entry.reset) {
		entry = attemptWindow{reset: now.Add(window)}
	}
	entry.count++
	return entry
}

// requestIP is the client address behind the limiter keys and audit rows. It resolves to the
// same address a session is bound to, and honours X-Forwarded-For only from a configured
// trusted proxy: keying on a caller-supplied header would make every limit here bypassable.
func (s *Server) requestIP(r *http.Request) string {
	return auth.ClientIP(r, s.config.Security.TrustedProxies)
}

func (s *Server) routes() {
	s.messagingRoutes()
	// Auth
	s.mux.HandleFunc("/api/auth/pow-challenge", s.handlePoWChallenge)
	s.mux.HandleFunc("/api/auth/login", s.handleLogin)
	s.mux.HandleFunc("/api/auth/mfa/totp", s.handleMFATOTP)
	s.mux.HandleFunc("/api/auth/mfa/recovery-code", s.handleMFARecovery)
	s.mux.HandleFunc("/api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("/api/auth/me", s.handleMe)
	s.mux.HandleFunc("/api/auth/change-password", s.handleChangePassword)

	// SSO
	s.mux.HandleFunc("/api/sso/kysignon/login", s.handleKySignOnLogin)
	s.mux.HandleFunc("/api/sso/kysignon/callback", s.handleKySignOnCallback)
	s.mux.HandleFunc("/api/sso/kysignon/sync", s.handleKySignOnSyncWebhook)
	s.mux.HandleFunc("/saml/metadata", s.handleSAMLMetadata)

	// Devices & Ephemeral QR Pairing
	s.mux.HandleFunc("/api/devices/pair/init", s.requireAuthenticated(s.handlePairInit))
	s.mux.HandleFunc("/api/devices/pair/verify", s.handlePairVerify)
	s.mux.HandleFunc("/api/devices/pair/poll", s.handlePairPoll)

	// Feature 0 KyBackup & Restore Drills. Capsules carry site data and keys: admins only.
	// Method patterns: only the declared method reaches a handler. Export is a POST so the
	// CSRF check covers a download that carries the whole instance.
	// Routes that move, pin, disable or export the recovery trust root also need a recent
	// sign-in, so a stolen or long-lived session cannot redirect every future capsule.
	s.mux.HandleFunc("POST /api/backup/drill", s.requireAdmin(s.handleBackupDrill))
	s.mux.HandleFunc("POST /api/backup/export-capsule", s.requireFreshAdmin(s.handleExportCapsule))
	s.mux.HandleFunc("POST /api/backup/pair-remote", s.tracked(s.requireFreshAdmin(s.handlePairRemoteRecovery)))
	s.mux.HandleFunc("POST /api/backup/deposit", s.tracked(s.requireFreshAdmin(s.handleRunBackup)))
	s.mux.HandleFunc("DELETE /api/backup/pairing", s.tracked(s.requireFreshAdmin(s.handleUnpair)))
	s.mux.HandleFunc("POST /api/backup/pin-key", s.tracked(s.requireFreshAdmin(s.handlePinKey)))
	s.mux.HandleFunc("PUT /api/backup/schedule", s.requireFreshAdmin(s.handleSetSchedule))
	s.mux.HandleFunc("POST /api/backup/messages/drill", s.requireAdmin(s.handleMessagesDrill))
	s.mux.HandleFunc("POST /api/backup/messages/deposit", s.tracked(s.requireFreshAdmin(s.handleRunMessagesBackup)))
	s.mux.HandleFunc("PUT /api/backup/messages/schedule", s.requireFreshAdmin(s.handleSetMessagesSchedule))
	s.mux.HandleFunc("GET /api/backup/status", s.requireAdmin(s.handleBackupStatus))
	s.mux.HandleFunc("GET /api/admin/messaging/usage", s.requireAdmin(s.handleMessagingUsage))
	s.mux.HandleFunc("GET /api/admin/network-check", s.requireAdmin(s.handleNetworkCheck))
	s.mux.HandleFunc("GET /api/admin/messaging/devices", s.requireAdmin(s.handleSuspendedDevices))
	s.mux.HandleFunc("POST /api/admin/messaging/devices/{device}/revoke", s.tracked(s.requireFreshAdmin(s.handleRevokeSuspendedDevice)))

	// Settings & Theme. The read endpoint tiers its own payload by role.
	s.mux.HandleFunc("/api/settings", s.handleGetSettings)
	s.mux.HandleFunc("/api/settings/theme", s.requireAdmin(s.handleSetTheme))

	// SCIM 2.0 routes
	s.scim.RegisterRoutes(s.mux)

	// Embedded React PWA Frontend
	s.mux.Handle("/", web.Handler())
}

// stepUpWindow is how recent a sign-in must be for requireFreshAdmin. Signing in again is the
// step-up: it repeats the password and TOTP, or the suite login, the session was issued on.
const stepUpWindow = 10 * time.Minute

// reauthURL forces a KySignOn credential prompt (prompt=login, max_age=0) for step-up.
const reauthURL = "/api/sso/kysignon/login?fresh=1"

// requireAdmin rejects requests without a valid session, or with a non-admin one.
func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc { return s.admin(h, false) }

// requireFreshAdmin is requireAdmin with step-up: the session must be younger than stepUpWindow.
func (s *Server) requireFreshAdmin(h http.HandlerFunc) http.HandlerFunc { return s.admin(h, true) }

func (s *Server) admin(h http.HandlerFunc, fresh bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, sess, err := s.sessions.AuthenticateRequest(r)
		if err != nil {
			if errors.Is(err, auth.ErrPasswordChangeRequired) {
				s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "Change your password before continuing", "code": "password_change_required"})
			} else {
				s.writeError(w, http.StatusUnauthorized, "Authentication required")
			}
			return
		}
		if user.Role != "admin" {
			s.writeError(w, http.StatusForbidden, "Administrator role required")
			return
		}
		if fresh && time.Since(sess.CreatedAt) > stepUpWindow {
			body := map[string]string{"error": "Sign out and sign in again to confirm this change: backup changes need a sign-in from the last 10 minutes", "code": "reauthentication_required"}
			if user.SSOProvider == "kysignon" {
				// A plain SSO login may silently reuse the IdP session; this one forces credentials.
				body["error"] = "Sign in to KySignOn again to confirm this change: backup changes need a sign-in from the last 10 minutes"
				body["reauth_url"] = reauthURL
			}
			s.writeJSON(w, http.StatusForbidden, body)
			return
		}
		h(w, r)
	}
}

func (s *Server) requireAuthenticated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := s.sessions.AuthenticateRequest(r); err != nil {
			if errors.Is(err, auth.ErrPasswordChangeRequired) {
				s.writeJSON(w, http.StatusForbidden, map[string]string{"error": "Change your password before continuing", "code": "password_change_required"})
			} else {
				s.writeError(w, http.StatusUnauthorized, "Authentication required")
			}
			return
		}
		h(w, r)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	if s.config.Security.CookieSecure {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}

	origin := r.Header.Get("Origin")
	if origin != "" && sameOrigin(origin, s.config.Server.AppURL) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-CSRF-Token, X-KySignOn-Signature, X-KyMessages-Device")

	if r.Method == http.MethodOptions {
		if origin != "" && !sameOrigin(origin, s.config.Server.AppURL) {
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// Routes that mint a session carry no CSRF token. Requiring a JSON body forces a CORS
	// preflight, which foreign origins fail, so a cross-site form cannot log a victim into
	// the attacker's account.
	if r.Method == http.MethodPost && mintsSession(r.URL.Path) && !jsonRequest(r) {
		s.writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if isUnsafeMethod(r.Method) && hasSessionCookie(r) && !csrfExempt(r.URL.Path) && !auth.ValidateCSRF(r) {
		s.writeError(w, http.StatusForbidden, "Invalid CSRF token")
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	}

	// SCIM middleware
	if strings.HasPrefix(r.URL.Path, "/scim/v2") {
		s.scim.AuthMiddleware(s.mux).ServeHTTP(w, r)
		return
	}

	s.mux.ServeHTTP(w, r)
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func hasSessionCookie(r *http.Request) bool {
	cookie, err := r.Cookie(auth.SessionCookieName)
	return err == nil && cookie.Value != ""
}

func csrfExempt(path string) bool {
	return path == "/api/auth/login" || strings.HasPrefix(path, "/api/auth/mfa/") || path == "/api/sso/kysignon/sync"
}

func mintsSession(path string) bool {
	return path == "/api/auth/login" || strings.HasPrefix(path, "/api/auth/mfa/") || path == "/api/devices/pair/verify"
}

func jsonRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func sameOrigin(origin, appURL string) bool {
	a, err := url.Parse(appURL)
	if err != nil || a.Scheme == "" || a.Host == "" {
		return false
	}
	o, err := url.Parse(origin)
	return err == nil && o.Scheme == a.Scheme && o.Host == a.Host
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, map[string]string{"error": message})
}
