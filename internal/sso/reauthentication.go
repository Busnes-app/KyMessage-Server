package sso

import (
	"context"
	"errors"
	"time"

	"golang.org/x/oauth2"
)

var ErrReauthenticationRequired = errors.New("fresh authentication for the same account is required")

// ReauthenticationRequest is server-owned authorization state, never callback input.
// The caller must bind it to the originating live session and intended action,
// then consume it once. Verifying claims alone does not authorize an identity reset.
type ReauthenticationRequest struct {
	RedirectURI string
	State       string
	Verifier    string
	Nonce       string
	Subject     string
	StartedAt   time.Time
}

func (r ReauthenticationRequest) valid(now time.Time) bool {
	return r.RedirectURI != "" && r.State != "" && r.Verifier != "" && r.Nonce != "" && r.Subject != "" &&
		!r.StartedAt.IsZero() && !r.StartedAt.After(now) && now.Sub(r.StartedAt) < 5*time.Minute
}

// BuildReauthenticationURL requests a new interaction and its signed auth_time.
func (k *KySignOnClient) BuildReauthenticationURL(ctx context.Context, request ReauthenticationRequest) (string, error) {
	if !request.valid(time.Now()) {
		return "", ErrReauthenticationRequired
	}
	return k.flow.authCodeURL(ctx, request.RedirectURI, request.State, request.Verifier, request.Nonce,
		oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
}

// ExchangeReauthenticationCode verifies signed evidence after the ordinary OIDC
// signature/issuer/audience/expiry/nonce checks. Timestamps use Unix-second precision;
// no clock-skew allowance or iat fallback weakens the fresh-authentication requirement.
func (k *KySignOnClient) ExchangeReauthenticationCode(ctx context.Context, code, state string, request ReauthenticationRequest) (*IdentityClaims, error) {
	if code == "" || state != request.State || !request.valid(time.Now()) {
		return nil, ErrReauthenticationRequired
	}
	token, err := k.flow.exchangeIDToken(ctx, code, request.Verifier, request.RedirectURI, request.Nonce)
	if err != nil {
		return nil, err
	}
	var evidence struct {
		AuthenticatedAt int64 `json:"auth_time"`
	}
	now := time.Now()
	if err := token.Claims(&evidence); err != nil || !request.valid(now) || token.Subject != request.Subject ||
		evidence.AuthenticatedAt <= 0 || evidence.AuthenticatedAt < request.StartedAt.Unix() ||
		evidence.AuthenticatedAt > now.Unix() || evidence.AuthenticatedAt > token.IssuedAt.Unix() || token.IssuedAt.Unix() > now.Unix() {
		return nil, ErrReauthenticationRequired
	}
	claims, err := claimsFromIDToken(token)
	if err != nil {
		return nil, err
	}
	claims.Provider = "kysignon"
	return claims, nil
}
