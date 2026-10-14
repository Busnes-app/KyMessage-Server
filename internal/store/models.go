package store

import (
	"time"
)

// User represents an identity within the system.
type User struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	Email              string     `json:"email"`
	DisplayName        string     `json:"display_name"`
	PasswordHash       string     `json:"-"`            // Never serialized to JSON
	Role               string     `json:"role"`         // "admin", "user", "manager"
	Status             string     `json:"status"`       // "active", "suspended", "inactive"
	SSOProvider        string     `json:"sso_provider"` // "local", "kyidentity", "oidc", "saml", "scim"
	SSOSubject         string     `json:"sso_subject,omitempty"`
	TOTPSecretEnc      string     `json:"-"` // AES-256-GCM encrypted
	TOTPEnabled        bool       `json:"totp_enabled"`
	TOTPLastCounter    int64      `json:"-"` // last RFC 6238 counter accepted; refuses replay inside the skew window
	RecoveryCodesHash  string     `json:"-"` // JSON array of sha256 hashes
	PushDeviceID       string     `json:"push_device_id,omitempty"`
	MustChangePassword bool       `json:"must_change_password"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	LastLoginAt        *time.Time `json:"last_login_at,omitempty"`
}

// Session represents an active authenticated user session.
type Session struct {
	TokenHash string `json:"token_hash"`
	UserID    string `json:"user_id"`
	UserAgent string `json:"user_agent"`
	IPAddress string `json:"ip_address"`
	// CreatedAt is when the credentials behind this session were verified. Step-up reads it,
	// so a session derived by pairing carries its parent's time, never a fresh one.
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type MFAChallenge struct {
	TokenHash string
	UserID    string
	ExpiresAt time.Time
}

// DevicePairing represents a 90-second ephemeral session to link mobile/PWA wrappers.
type DevicePairing struct {
	// Secret and PushToken never serialise: this record is reached by unauthenticated
	// pair/verify and pair/poll callers. A handler that must return one needs its own type.
	Secret     string    `json:"-"` // Ephemeral secret for exchange
	UserID     string    `json:"user_id,omitempty"`
	DeviceName string    `json:"device_name,omitempty"`
	Platform   string    `json:"platform,omitempty"` // "android", "ios", "pwa", "desktop"
	PushToken  string    `json:"-"`
	Status     string    `json:"status"` // "pending", "approved", "consumed", "expired"
	CreatedAt  time.Time `json:"created_at"`
	// AuthenticatedAt is the initiating session's credential time, inherited by the paired session.
	AuthenticatedAt time.Time `json:"-"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// Group represents a SCIM/RBAC user group.
type Group struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	ExternalID  string    `json:"external_id,omitempty"`
	Members     []string  `json:"members,omitempty"` // User IDs
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AuditRecord logs security and operational events with tamper-evident structure.
type AuditRecord struct {
	ID        int64     `json:"id"`
	UserID    string    `json:"user_id"`
	Action    string    `json:"action"` // e.g. "auth.login", "scim.user_created"
	Resource  string    `json:"resource"`
	Details   string    `json:"details,omitempty"`
	IPAddress string    `json:"ip_address"`
	CreatedAt time.Time `json:"created_at"`
}

// Setting represents a durable server-wide key-value configuration entry.
type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Agent token states: a current personal session, revoked by an admin, or expired unused.
const (
	AgentTokenActive  = "active"
	AgentTokenRevoked = "revoked"
	AgentTokenExpired = "expired"
)

// Agent is a chat agent account: a MAS user with no upstream link, owned by a directory
// member (OwnerSubject, a KyIdentity subject). Its token lives only in MAS; SessionID names the
// current personal session and is empty when TokenState is not active.
type Agent struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	MASUserID    string    `json:"mas_user_id"`
	DeviceID     string    `json:"device_id"`
	OwnerSubject string    `json:"owner_subject"`
	SessionID    string    `json:"-"`
	TokenState   string    `json:"token_state"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
}
