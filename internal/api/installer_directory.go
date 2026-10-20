package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	protocol "github.com/elimity-com/scim"
)

func (s *Server) handleInstallerDirectory(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		s.writeError(w, 400, "Invalid directory body")
		return
	}
	if !s.config.SSO.Enabled || s.config.SSO.KyIdentityIssuer == "" {
		s.writeError(w, 503, "Directory unavailable")
		return
	}
	if s.config.InstallerSSO == nil {
		s.writeError(w, 503, "Managed directory unavailable")
		return
	}
	secret, err := applysetup.Secret(s.config.InstallerSSO.DirectorySecretFile)
	if err != nil {
		s.writeError(w, 503, "Directory credential unavailable")
		return
	}
	event, err := syncauth.Verify([]byte(secret), syncauth.FromRequest(r), raw, syncauth.Options{})
	if err != nil {
		s.writeError(w, 401, "Invalid directory signature")
		return
	}
	var attrs protocol.ResourceAttributes
	if json.Unmarshal(raw, &attrs) != nil {
		s.writeError(w, 400, "Invalid directory resource")
		return
	}
	subject, _ := attrs["id"].(string)
	active, hasActive := attrs["active"].(bool)
	meta, _ := attrs["meta"].(map[string]any)
	version, _ := meta["version"].(string)
	revision := strings.TrimSuffix(strings.TrimPrefix(version, `W/"`), `"`)
	if version != `W/"`+revision+`"` || subject == "" || len(subject) > 256 || ((event.Type == "user.updated" || event.Type == "user.created" || event.Type == "user.mfa_reset") && !hasActive) {
		s.writeError(w, 400, "Invalid directory identity or revision")
		return
	}
	admin := false
	if roles, ok := attrs["roles"].([]any); ok {
		for _, item := range roles {
			role, _ := item.(map[string]any)
			if role["value"] == "kymessage.admin" {
				admin = true
			}
		}
	}
	sum := sha256.Sum256(append([]byte(event.Type+"\x00"), raw...))
	digest := hex.EncodeToString(sum[:])
	err = s.store.ApplyInstallerDirectory(r.Context(), s.config.SSO.KyIdentityIssuer, subject, event.Type, revision, digest, active, admin)
	if errors.Is(err, store.ErrNotInstallerOwned) {
		// Someone KyIdentity assigned after setup: an ordinary chat member, no console access.
		err = s.kyidentity.ApplyMemberEvent(r.Context(), event, raw)
		switch {
		case errors.Is(err, sso.ErrSyncMalformed):
			s.writeError(w, 400, "Invalid directory resource")
			return
		case errors.Is(err, sso.ErrSyncUsernameConflict):
			s.writeError(w, 409, err.Error())
			return
		case err != nil:
			s.writeError(w, 500, "Directory update failed")
			return
		}
	} else if err != nil {
		s.writeError(w, 422, "Directory revision or assigned identity refused")
		return
	}
	if s.directoryChanged != nil {
		s.directoryChanged()
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleInstallerNetwork(w http.ResponseWriter, r *http.Request) {
	trusted := auth.TrustedPeer(r, s.config.Security.TrustedProxies)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if trusted {
		switch r.Header.Get("X-Forwarded-Proto") {
		case "https", "http":
			scheme = r.Header.Get("X-Forwarded-Proto")
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, 200, map[string]any{"clientIp": s.requestIP(r), "proxyHeadersTrusted": trusted, "scheme": scheme})
}
