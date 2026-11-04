package sso

import (
	"errors"
	"slices"
	"time"
)

var (
	ErrInvalidState     = errors.New("invalid or expired SSO state")
	ErrInvalidIDToken   = errors.New("invalid ID token")
	ErrProviderDisabled = errors.New("SSO provider is not enabled")
)

// IdentityClaims holds the unified identity extracted from any SSO provider.
type IdentityClaims struct {
	Issuer            string `json:"-"`
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Role              string `json:"role,omitempty"`
	// Roles are KyIdentity's roles for this app; empty when the app defines none.
	Roles    []string `json:"roles,omitempty"`
	Provider string   `json:"provider"` // "kyidentity"
	// AuthenticatedAt is the signed auth_time, or zero when absent or implausible. The IdP
	// may reuse an old login, so only this, never the callback time, says how fresh it is.
	AuthenticatedAt time.Time `json:"-"`
}

// AssertedRole is the console role the token asserts, or "" when it asserts none (no app
// roles and no legacy global role), in which case the stored role stands.
func (c *IdentityClaims) AssertedRole() string {
	switch {
	case slices.Contains(c.Roles, "admin"):
		return "admin"
	case len(c.Roles) > 0:
		return "user"
	case c.Role == "admin":
		return "admin"
	case c.Role != "":
		return "user"
	}
	return ""
}

// SSOState represents ephemeral state held during OAuth/OIDC authorization flow.
type SSOState struct {
	State        string    `json:"state"`
	Verifier     string    `json:"verifier"`
	Provider     string    `json:"provider"`
	RedirectBack string    `json:"redirect_back"`
	CreatedAt    time.Time `json:"created_at"`
}
