package sso

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// idpTimeout bounds each call to the IdP (discovery, keys, code exchange). Discovery runs
// under the flow's mutex, so an unbounded call to a hung IdP would queue every sign-in.
var idpTimeout = 10 * time.Second

// idpContext carries the bounded client unless the caller supplied one (oidc.ClientContext).
func idpContext(ctx context.Context) context.Context {
	if _, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok {
		return ctx
	}
	return oidc.ClientContext(ctx, &http.Client{Timeout: idpTimeout})
}

type oauthFlow struct {
	issuer       string
	clientID     string
	clientSecret string
	mu           sync.Mutex
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
}

func newOAuthFlow(issuer, clientID, clientSecret string) *oauthFlow {
	return &oauthFlow{issuer: strings.TrimRight(issuer, "/"), clientID: clientID, clientSecret: clientSecret}
}

func (f *oauthFlow) getProvider(ctx context.Context) (*oidc.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.provider != nil {
		return f.provider, nil
	}
	ctx, cancel := context.WithTimeout(ctx, idpTimeout)
	defer cancel()
	ctx = idpContext(ctx)
	provider, err := oidc.NewProvider(ctx, f.issuer)
	if err != nil {
		return nil, err
	}
	jwksClient := *ctx.Value(oauth2.HTTPClient).(*http.Client)
	base := jwksClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	jwksClient.Transport = strongJWKSTransport{base: base}
	f.verifier = provider.VerifierContext(oidc.ClientContext(ctx, &jwksClient), &oidc.Config{ClientID: f.clientID})
	f.provider = provider
	return provider, nil
}

func (f *oauthFlow) config(ctx context.Context, redirectURI string) (*oauth2.Config, *oidc.Provider, error) {
	if f.issuer == "" || f.clientID == "" {
		return nil, nil, ErrProviderDisabled
	}
	provider, err := f.getProvider(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &oauth2.Config{ClientID: f.clientID, ClientSecret: f.clientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURI, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}, provider, nil
}

func (f *oauthFlow) authCodeURL(ctx context.Context, redirectURI, state, verifier, nonce string, options ...oauth2.AuthCodeOption) (string, error) {
	config, _, err := f.config(ctx, redirectURI)
	if err != nil {
		return "", err
	}
	options = append(options, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce))
	return config.AuthCodeURL(state, options...), nil
}

func (f *oauthFlow) exchange(ctx context.Context, code, verifier, redirectURI, expectedNonce string) (*IdentityClaims, error) {
	idToken, err := f.exchangeIDToken(ctx, code, verifier, redirectURI, expectedNonce)
	if err != nil {
		return nil, err
	}
	return claimsFromIDToken(idToken)
}

func (f *oauthFlow) exchangeIDToken(ctx context.Context, code, verifier, redirectURI, expectedNonce string) (*oidc.IDToken, error) {
	ctx = idpContext(ctx)
	config, _, err := f.config(ctx, redirectURI)
	if err != nil {
		return nil, err
	}
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, err
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("missing id_token in token response")
	}
	idToken, err := f.verifier.Verify(ctx, rawIDToken)
	if err != nil || expectedNonce == "" || idToken.Nonce != expectedNonce {
		return nil, ErrInvalidIDToken
	}
	return idToken, nil
}

func claimsFromIDToken(idToken *oidc.IDToken) (*IdentityClaims, error) {
	var raw struct {
		Sub               string `json:"sub"`
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Role              string `json:"role"`
		AuthTime          int64  `json:"auth_time"`
	}
	if err := idToken.Claims(&raw); err != nil || raw.Sub == "" {
		return nil, ErrInvalidIDToken
	}
	username := raw.PreferredUsername
	if username == "" {
		username = raw.Email
	}
	if username == "" {
		username = raw.Sub
	}
	claims := &IdentityClaims{Issuer: idToken.Issuer, Subject: raw.Sub, Email: raw.Email, Name: raw.Name, PreferredUsername: username, Role: raw.Role}
	if raw.AuthTime > 0 && raw.AuthTime <= idToken.IssuedAt.Unix() && raw.AuthTime <= time.Now().Unix() {
		claims.AuthenticatedAt = time.Unix(raw.AuthTime, 0).UTC()
	}
	return claims, nil
}
