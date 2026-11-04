package api

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"golang.org/x/oauth2"
)

// ssoCookie sets (maxAge > 0) or deletes (maxAge < 0) one of the short-lived flow cookies.
// Path=/ because the __Host- prefix requires it.
func (s *Server) ssoCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName(name, s.config.Security),
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.config.Security.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) handleKyIdentityLogin(w http.ResponseWriter, r *http.Request) {
	if !s.config.SSO.Enabled {
		s.writeError(w, http.StatusNotFound, "Unknown API endpoint")
		return
	}
	state := crypto.RandomHex(16)
	nonce := crypto.RandomHex(16)
	verifier := oauth2.GenerateVerifier()

	redirectURI := fmt.Sprintf("%s/api/sso/kyidentity/callback", s.config.Server.AppURL)
	authURL, err := s.kyidentity.BuildAuthURL(r.Context(), redirectURI, state, verifier, nonce, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		log.Printf("[SSO] KyIdentity sign-in unavailable: %v", err)
		s.writeError(w, http.StatusBadGateway, "Suite sign-in is unavailable")
		return
	}

	s.ssoCookie(w, "ky_pkce_"+state, verifier, 300)
	s.ssoCookie(w, "ky_nonce_"+state, nonce, 300)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleKyIdentityCallback(w http.ResponseWriter, r *http.Request) {
	if !s.config.SSO.Enabled {
		s.writeError(w, http.StatusNotFound, "Unknown API endpoint")
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	cookie, err := r.Cookie(auth.CookieName("ky_pkce_"+state, s.config.Security))
	if err != nil || cookie.Value == "" {
		s.ssoRefused(w, r, "failed")
		return
	}
	verifier := cookie.Value
	nonceCookie, err := r.Cookie(auth.CookieName("ky_nonce_"+state, s.config.Security))
	if err != nil || nonceCookie.Value == "" {
		s.ssoRefused(w, r, "failed")
		return
	}
	// One flow, one callback: the cookies go whatever the outcome.
	s.ssoCookie(w, "ky_pkce_"+state, "", -1)
	s.ssoCookie(w, "ky_nonce_"+state, "", -1)

	redirectURI := fmt.Sprintf("%s/api/sso/kyidentity/callback", s.config.Server.AppURL)
	claims, err := s.kyidentity.ExchangeCode(r.Context(), code, verifier, redirectURI, nonceCookie.Value)
	if err != nil {
		log.Printf("[SSO] KyIdentity code exchange failed: %v", err)
		s.ssoRefused(w, r, "failed")
		return
	}

	managed, allowed, err := s.store.InstallerIdentity(r.Context(), claims.Issuer, claims.Subject)
	if err != nil {
		log.Printf("[SSO] installer identity check for %q failed: %v", claims.Subject, err)
		s.ssoRefused(w, r, "failed")
		return
	}
	if managed && !allowed {
		s.ssoRefused(w, r, "not_assigned")
		return
	}
	// Accounts are matched by subject only; a username never links to an existing account.
	user, err := s.store.Users().GetUserBySSO(r.Context(), "kyidentity", claims.Subject)
	if errors.Is(err, store.ErrNotFound) && s.config.SCIM.Enabled && claims.Subject != "" {
		// A SCIM row whose externalId is this subject is this person: sign in as it, and SCIM
		// keeps provisioning and offboarding it (ky-server-base Q1 A). externalId is not
		// unique, so two such rows are refused rather than one picked at random.
		rows, n, lerr := s.store.Users().ListUsers(r.Context(), 0, 2, store.UserFilter{Field: store.UserFieldSubject, Value: claims.Subject, Provider: "scim"})
		switch {
		case lerr != nil:
			err = lerr
		case n > 1:
			s.audit(r.Context(), "", r, "sso.login_conflict", claims.Subject, fmt.Sprintf("subject=%q scim_rows=%d", claims.Subject, n))
			s.ssoRefused(w, r, "adoption_conflict")
			return
		case len(rows) == 1 && rows[0].Role == "admin" && !s.config.SCIM.AllowAdmin:
			// A SCIM admin row from before KY_SCIM_ALLOW_ADMIN existed must not become a live
			// console administrator by sign-in (ky-server-base Q3 A).
			s.audit(r.Context(), rows[0].ID, r, "sso.login_refused", "session", "scim admin row without KY_SCIM_ALLOW_ADMIN")
			s.ssoRefused(w, r, "not_assigned")
			return
		case len(rows) == 1:
			user, err = rows[0], nil
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		if !s.config.SSO.AutoProvision {
			s.ssoRefused(w, r, "not_provisioned")
			return
		}
		user = &store.User{
			ID:          fmt.Sprintf("usr_%s", crypto.RandomHex(12)),
			Username:    claims.PreferredUsername,
			Email:       claims.Email,
			DisplayName: claims.Name,
			Role:        "user",
			Status:      "active",
			SSOProvider: "kyidentity",
			SSOSubject:  claims.Subject,
		}
		err = s.store.Users().CreateUser(r.Context(), user)
		if errors.Is(err, store.ErrUsernameTaken) {
			log.Printf("[SSO] sign-in by %q refused: username %q is held by another KyMessages account", claims.Subject, user.Username)
			s.audit(r.Context(), "", r, "sso.login_conflict", user.Username, fmt.Sprintf("subject=%q", claims.Subject))
			s.ssoRefused(w, r, "username_conflict")
			return
		}
		if err != nil {
			log.Printf("[SSO] provisioning %q failed: %v", claims.Subject, err)
			s.ssoRefused(w, r, "failed")
			return
		}
	} else if err != nil {
		log.Printf("[SSO] looking up %q failed: %v", claims.Subject, err)
		s.ssoRefused(w, r, "failed")
		return
	}
	if user.Status != "active" {
		s.audit(r.Context(), user.ID, r, "sso.login_refused", "session", fmt.Sprintf("status=%q", user.Status))
		s.ssoRefused(w, r, "disabled")
		return
	}

	// KyIdentity's explicit app role applies at each sign-in; empty roles mean user. Installer-
	// and SCIM-owned rows keep theirs. A change ends the user's other sessions and pairings.
	if role := claims.AssertedRole(); role != user.Role && user.SSOProvider == "kyidentity" && !managed {
		from := user.Role
		if err := s.store.Users().ApplySignInRole(r.Context(), user, role); err != nil {
			log.Printf("[SSO] applying KyIdentity role %q to %s failed: %v", role, user.ID, err)
			s.ssoRefused(w, r, "failed")
			return
		}
		s.audit(r.Context(), user.ID, r, "sso.role_changed", "user", fmt.Sprintf("from=%q to=%q", from, role))
	}

	// The session is as fresh as the IdP's signed auth_time; a missing one never passes step-up.
	_, _, err = s.sessions.IssueDerivedSession(r.Context(), w, r, user, claims.AuthenticatedAt)
	if err != nil {
		log.Printf("[SSO] session for %s failed: %v", user.ID, err)
		s.ssoRefused(w, r, "failed")
		return
	}
	s.audit(r.Context(), user.ID, r, "sso.login", "session", "")

	http.Redirect(w, r, "/", http.StatusFound)
}

// ssoRefused ends a refused or failed callback on the sign-in page, which explains code. The
// callback is a browser navigation, so a JSON error would strand the user on a raw page. Only
// the fixed codes the Login page knows go in the URL, never server text.
func (s *Server) ssoRefused(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/#/login?sso_error="+code, http.StatusFound)
}

func (s *Server) handleKyIdentitySyncWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	if _, err := s.store.Settings().GetSetting(r.Context(), store.InstallerConfigKey); err == nil {
		s.handleInstallerDirectory(w, r)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.writeError(w, 503, "Directory unavailable")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	switch err := s.kyidentity.HandleSyncWebhook(r.Context(), syncauth.FromRequest(r), body); {
	case errors.Is(err, sso.ErrSyncUnauthorized):
		// The reason is derived from request headers; keep it on one log line.
		log.Printf("[SSO] directory webhook refused: %s", strings.NewReplacer("\n", "", "\r", "").Replace(err.Error()))
		s.writeError(w, http.StatusUnauthorized, "Directory webhook not authenticated")
		return
	case errors.Is(err, sso.ErrSyncMalformed):
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, sso.ErrSyncUsernameConflict):
		// Received and explained: KyIdentity fences a 500 as an uncertain write.
		s.writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.writeError(w, http.StatusInternalServerError, "Directory update failed")
		return
	}

	if s.directoryChanged != nil {
		s.directoryChanged()
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"synced": true})
}
