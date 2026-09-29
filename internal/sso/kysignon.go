package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"golang.org/x/oauth2"
)

// KySignOnClient manages interactions with the central KySignOn identity provider.
type KySignOnClient struct {
	config config.SSOConfig
	store  store.Store
	flow   *oauthFlow

	// syncMu applies directory updates one at a time, so a read and its conditional write
	// see the same row. Ordering itself is persisted with the user (directory_synced_at).
	syncMu sync.Mutex
}

func NewKySignOnClient(cfg config.SSOConfig, st store.Store) *KySignOnClient {
	return &KySignOnClient{
		config: cfg,
		store:  st,
		flow:   newOAuthFlow(cfg.KySignOnIssuer, cfg.KySignOnClientID, cfg.KySignOnSecret),
	}
}

// BuildAuthURL generates the authorization code URL with PKCE for KySignOn.
// fresh asks the IdP for a new credential interaction instead of reusing its own session.
func (k *KySignOnClient) BuildAuthURL(ctx context.Context, redirectURI, state, verifier, nonce string, fresh bool) (string, error) {
	if fresh {
		return k.flow.authCodeURL(ctx, redirectURI, state, verifier, nonce,
			oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	return k.flow.authCodeURL(ctx, redirectURI, state, verifier, nonce)
}

// ExchangeCode exchanges the authorization code and verifier for identity claims.
func (k *KySignOnClient) ExchangeCode(ctx context.Context, code, verifier, redirectURI, expectedNonce string) (*IdentityClaims, error) {
	claims, err := k.flow.exchange(ctx, code, verifier, redirectURI, expectedNonce)
	if err != nil {
		return nil, err
	}
	claims.Provider = "kysignon"
	return claims, nil
}

// KySignOnSyncPayload defines the schema received during automatic directory replication webhooks.
type KySignOnSyncPayload struct {
	Event       string `json:"event"` // "user.created", "user.updated", "user.deactivated", "user.deleted"
	ID          string `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	Timestamp   int64  `json:"timestamp"`
}

// HandleSyncWebhook processes inbound HMAC-SHA256 signed user updates from KySignOn server.
func (k *KySignOnClient) HandleSyncWebhook(ctx context.Context, body []byte, signature string) error {
	if k.config.KySignOnHMACSecret == "" {
		return errors.New("webhook HMAC secret is not configured")
	}

	if !crypto.VerifyHMACSHA256(body, k.config.KySignOnHMACSecret, signature) {
		return errors.New("invalid webhook signature")
	}

	var payload KySignOnSyncPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("invalid json payload: %w", err)
	}
	if payload.Timestamp == 0 || time.Since(time.Unix(payload.Timestamp, 0)).Abs() > 5*time.Minute {
		return errors.New("webhook timestamp is missing or expired")
	}

	k.syncMu.Lock()
	defer k.syncMu.Unlock()
	return k.applySync(ctx, payload)
}

// raisesPrivilege reports whether a directory update would grant admin or reactivate. Updates
// carry second-resolution timestamps and no revision, so two in the same second cannot be
// ordered; a tie may only lower privilege, and a captured same-second promotion cannot
// undo a demotion.
func raisesPrivilege(existing *store.User, role, status string) bool {
	return (role == "admin" && existing.Role != "admin") || (status == "active" && existing.Status != "active")
}

func (k *KySignOnClient) applySync(ctx context.Context, payload KySignOnSyncPayload) error {

	switch payload.Event {
	case "user.created", "user.updated":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kysignon", payload.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}

		role := payload.Role
		if role == "" {
			role = "user"
		}
		status := payload.Status
		if status == "" {
			status = "active"
		}

		if existing != nil {
			privilegesChanged := existing.Role != role || existing.Status != status
			allowTie := !raisesPrivilege(existing, role, status)
			updated := *existing
			updated.Username = payload.Username
			updated.Email = payload.Email
			updated.DisplayName = payload.DisplayName
			updated.Role = role
			updated.Status = status
			// A replayed or superseded update applies nothing and still succeeds, so the
			// sender stops retrying.
			applied, err := k.store.Users().ApplyDirectoryProfile(ctx, &updated, payload.Timestamp, allowTie)
			if err != nil || !applied {
				return err
			}
			if privilegesChanged {
				return k.store.Sessions().DeleteUserSessions(ctx, existing.ID)
			}
			return nil
		}

		newUser := &store.User{
			ID:          fmt.Sprintf("usr_%s", crypto.RandomHex(12)),
			Username:    payload.Username,
			Email:       payload.Email,
			DisplayName: payload.DisplayName,
			Role:        role,
			Status:      status,
			SSOProvider: "kysignon",
			SSOSubject:  payload.ID,
		}
		if err := k.store.Users().CreateUser(ctx, newUser); err != nil {
			return err
		}
		_, err = k.store.Users().ApplyDirectoryProfile(ctx, newUser, payload.Timestamp, true)
		return err

	case "user.deactivated":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kysignon", payload.ID)
		if err != nil {
			return nil // User might not exist locally
		}
		existing.Status = "inactive"
		applied, err := k.store.Users().ApplyDirectoryProfile(ctx, existing, payload.Timestamp, true)
		if err != nil || !applied {
			return err
		}
		return k.store.Sessions().DeleteUserSessions(ctx, existing.ID)

	case "user.deleted":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kysignon", payload.ID)
		if err != nil {
			return nil
		}
		_, err = k.store.Users().DeleteDirectoryUser(ctx, existing.ID, payload.Timestamp)
		return err
	}

	return nil
}
