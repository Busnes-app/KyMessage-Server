package sso

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
)

// WebhookRecordKey is the setting written after each acknowledged directory delivery, by this
// package alone; the console reads it.
const WebhookRecordKey = "kyidentity_webhook_last"

// WebhookRecord is the last acknowledged delivery: when, and its signed event type.
type WebhookRecord struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

// Why a delivery was refused, as the console shows it.
const (
	RejectNotConfigured = "not_configured" // no secret, or one shorter than syncauth.MinKeyBytes
	RejectBadSignature  = "bad_signature"  // no signature, or one that does not match
	RejectStale         = "stale"          // timestamp outside the ±5-minute window
	RejectBadHeaders    = "bad_headers"    // event type, ID or timestamp missing or unparseable
	RejectMalformed     = "malformed"      // signed, but not a usable SCIM user
	// signed, but another KyMessages account holds the username
	RejectUsernameConflict = "username_conflict"
)

// Rejections counts refused deliveries since this process started. Memory only; an
// unauthenticated sender never reaches the database or the audit log.
type Rejections struct {
	Count      int64      `json:"count"`
	LastAt     *time.Time `json:"last_at"`
	LastReason string     `json:"last_reason"`
}

type rejectCounter struct {
	mu   sync.Mutex
	last Rejections
}

// Rejections returns the refusal counters.
func (k *KyIdentityClient) Rejections() Rejections {
	k.rejects.mu.Lock()
	defer k.rejects.mu.Unlock()
	return k.rejects.last
}

func (k *KyIdentityClient) reject(err error) {
	now := time.Now().UTC()
	k.rejects.mu.Lock()
	defer k.rejects.mu.Unlock()
	k.rejects.last = Rejections{Count: k.rejects.last.Count + 1, LastAt: &now, LastReason: rejectReason(err)}
}

func rejectReason(err error) string {
	switch {
	case errors.Is(err, ErrSyncMalformed):
		return RejectMalformed
	case errors.Is(err, ErrSyncUsernameConflict):
		return RejectUsernameConflict
	case errors.Is(err, errNoSecret), errors.Is(err, syncauth.ErrShortKey):
		return RejectNotConfigured
	case errors.Is(err, syncauth.ErrStale):
		return RejectStale
	case errors.Is(err, syncauth.ErrNoSignature), errors.Is(err, syncauth.ErrBadSignature):
		return RejectBadSignature
	default: // syncauth.ErrMissingFields, syncauth.ErrBadTimestamp
		return RejectBadHeaders
	}
}

// recordDelivery notes an acknowledged delivery. A failed write is logged, never a failed
// delivery: the event is already applied.
func (k *KyIdentityClient) recordDelivery(ctx context.Context, kind string) {
	if len(kind) > 64 { // signed by KyIdentity; bounded anyway
		kind = strings.ToValidUTF8(kind[:64], "")
	}
	b, err := json.Marshal(WebhookRecord{At: time.Now().UTC(), Kind: kind})
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := k.store.Settings().SetSetting(wctx, WebhookRecordKey, string(b)); err != nil {
		log.Printf("[SSO] directory webhook applied; its time was not recorded: %v", err)
	}
}
