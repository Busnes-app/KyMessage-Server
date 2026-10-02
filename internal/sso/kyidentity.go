package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"golang.org/x/oauth2"
)

// KyIdentityClient manages interactions with the suite KyIdentity identity provider.
type KyIdentityClient struct {
	config config.SSOConfig
	store  store.Store
	flow   *oauthFlow

	// syncMu applies directory updates one at a time, so a read and its conditional write
	// see the same row. Ordering itself is persisted per subject (directory_sync_state).
	syncMu sync.Mutex
	// rejects counts refused deliveries since start, in memory only.
	rejects rejectCounter
}

func NewKyIdentityClient(cfg config.SSOConfig, st store.Store) *KyIdentityClient {
	return &KyIdentityClient{
		config: cfg,
		store:  st,
		flow:   newOAuthFlow(cfg.KyIdentityIssuer, cfg.KyIdentityClientID, cfg.KyIdentitySecret),
	}
}

// BuildAuthURL generates the authorization code URL with PKCE for KyIdentity.
// fresh asks the IdP for a new credential interaction instead of reusing its own session.
func (k *KyIdentityClient) BuildAuthURL(ctx context.Context, redirectURI, state, verifier, nonce string, fresh bool) (string, error) {
	if fresh {
		return k.flow.authCodeURL(ctx, redirectURI, state, verifier, nonce,
			oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	return k.flow.authCodeURL(ctx, redirectURI, state, verifier, nonce)
}

// ExchangeCode exchanges the authorization code and verifier for identity claims.
func (k *KyIdentityClient) ExchangeCode(ctx context.Context, code, verifier, redirectURI, expectedNonce string) (*IdentityClaims, error) {
	claims, err := k.flow.exchange(ctx, code, verifier, redirectURI, expectedNonce)
	if err != nil {
		return nil, err
	}
	claims.Provider = "kyidentity"
	return claims, nil
}

// DirectoryUser is the SCIM 2.0 User resource KyIdentity sends to a suite webhook. The event
// type and ID travel in the signed headers, not the body.
type DirectoryUser struct {
	ID          string `json:"id"`
	UserName    string `json:"userName"`
	DisplayName string `json:"displayName"`
	Name        *struct {
		Formatted string `json:"formatted"`
	} `json:"name"`
	Emails []scimValue `json:"emails"`
	Roles  []scimValue `json:"roles"`
	Active bool        `json:"active"`
	Meta   *struct {
		Version string `json:"version"`
	} `json:"meta"`
}

type scimValue struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary"`
}

// primaryValue is the value marked primary, else the first, else "".
func primaryValue(values []scimValue) string {
	for _, v := range values {
		if v.Primary {
			return v.Value
		}
	}
	if len(values) > 0 {
		return values[0].Value
	}
	return ""
}

var (
	// ErrSyncUnauthorized covers a missing secret or a failed signature, timestamp or event check.
	ErrSyncUnauthorized = errors.New("directory webhook not authenticated")
	// ErrSyncMalformed is an authenticated body that is not a usable SCIM User.
	ErrSyncMalformed = errors.New("directory webhook body is not a usable SCIM user")
	// errNoSecret: the receiver has no key, so nothing can be verified.
	errNoSecret = errors.New("KY_KYIDENTITY_HMAC_SECRET is not set")
)

// revisionPattern matches KyIdentity's meta.version, W/"<n>": a per-user counter that rises on
// every change. -1 marks state resent after a KyIdentity restore.
var revisionPattern = regexp.MustCompile(`^W/"(-1|0|[1-9][0-9]{0,17})"$`)

// HandleSyncWebhook applies one signed directory event from KyIdentity. Superseded and
// duplicate events succeed without effect, so the sender's outbox stops retrying them. An
// acknowledged delivery is recorded under WebhookRecordKey; a refused one is only counted, in
// memory (Rejections).
func (k *KyIdentityClient) HandleSyncWebhook(ctx context.Context, headers syncauth.Headers, body []byte) error {
	kind, err := k.applySyncWebhook(ctx, headers, body)
	switch {
	case err == nil:
		k.recordDelivery(ctx, kind)
	case errors.Is(err, ErrSyncUnauthorized), errors.Is(err, ErrSyncMalformed):
		k.reject(err)
	}
	return err
}

// applySyncWebhook verifies and applies one event and returns its signed type. The cause of
// ErrSyncUnauthorized stays reachable with errors.Is, so refusals can be told apart.
func (k *KyIdentityClient) applySyncWebhook(ctx context.Context, headers syncauth.Headers, body []byte) (string, error) {
	if k.config.KyIdentityHMACSecret == "" {
		return "", fmt.Errorf("%w: %w", ErrSyncUnauthorized, errNoSecret)
	}
	event, err := syncauth.Verify([]byte(k.config.KyIdentityHMACSecret), headers, body, syncauth.Options{})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSyncUnauthorized, err)
	}
	var user DirectoryUser
	if err := json.Unmarshal(body, &user); err != nil || user.ID == "" || user.Meta == nil {
		return "", ErrSyncMalformed
	}
	match := revisionPattern.FindStringSubmatch(user.Meta.Version)
	if match == nil {
		return "", ErrSyncMalformed
	}
	revision, _ := strconv.ParseInt(match[1], 10, 64)
	ev := store.DirectoryEvent{ID: event.ID, Revision: revision}

	k.syncMu.Lock()
	defer k.syncMu.Unlock()
	switch event.Type {
	case "user.created", "user.updated", "user.mfa_reset":
		return event.Type, k.upsertDirectoryUser(ctx, user, ev)
	case "user.deleted":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kyidentity", user.ID)
		if errors.Is(err, store.ErrNotFound) {
			// Record the deletion anyway, so an older creation delivered later cannot
			// bring the account into existence. The delete matches no row.
			existing, err = &store.User{SSOProvider: "kyidentity", SSOSubject: user.ID}, nil
		}
		if err != nil {
			return event.Type, err
		}
		_, err = k.store.Users().DeleteDirectoryUser(ctx, existing, ev)
		return event.Type, err
	}
	return event.Type, nil // other event types (groups) are not for this product
}

func (k *KyIdentityClient) upsertDirectoryUser(ctx context.Context, in DirectoryUser, ev store.DirectoryEvent) error {
	// KyIdentity sends this app's roles when it defines any, else the user's global role.
	role := "user"
	if primaryValue(in.Roles) == "admin" {
		role = "admin"
	}
	status := "inactive"
	if in.Active {
		status = "active"
	}
	email := primaryValue(in.Emails)
	displayName := in.DisplayName
	if displayName == "" && in.Name != nil {
		displayName = in.Name.Formatted
	}

	existing, err := k.store.Users().GetUserBySSO(ctx, "kyidentity", in.ID)
	if errors.Is(err, store.ErrNotFound) {
		_, err = k.store.Users().CreateDirectoryUser(ctx, &store.User{
			ID: fmt.Sprintf("usr_%s", crypto.RandomHex(12)), Username: in.UserName, Email: email,
			DisplayName: displayName, Role: role, Status: status, SSOProvider: "kyidentity", SSOSubject: in.ID,
		}, ev)
		return err
	}
	if err != nil {
		return err
	}
	updated := *existing
	updated.Username, updated.Email, updated.DisplayName, updated.Role, updated.Status = in.UserName, email, displayName, role, status
	applied, err := k.store.Users().ApplyDirectoryProfile(ctx, &updated, ev)
	if err != nil || !applied {
		return err
	}
	if existing.Role != role || existing.Status != status {
		return k.store.Sessions().DeleteUserSessions(ctx, existing.ID)
	}
	return nil
}
