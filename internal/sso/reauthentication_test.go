package sso_test

import (
	"context"
	stdcrypto "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"golang.org/x/oauth2"
)

func TestReauthenticationSignedEvidence(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	var codes sync.Map
	verifier := oauth2.GenerateVerifier()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
		case "/token":
			client, secret, ok := r.BasicAuth()
			if err := r.ParseForm(); err != nil || !ok || client != "client" || secret != "secret" || r.Form.Get("code_verifier") != verifier || r.Form.Get("redirect_uri") != "https://app.example/callback" || r.Form.Get("grant_type") != "authorization_code" {
				http.Error(w, "invalid exchange", 400)
				return
			}
			token, ok := codes.LoadAndDelete(r.Form.Get("code"))
			if !ok {
				http.Error(w, "invalid code", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "unused", "token_type": "Bearer", "id_token": token})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	issuer = server.URL
	client := sso.NewKyIdentityClient(config.SSOConfig{KyIdentityIssuer: issuer, KyIdentityClientID: "client", KyIdentitySecret: "secret"}, nil)
	now := time.Now().Truncate(time.Second)
	request := sso.ReauthenticationRequest{RedirectURI: "https://app.example/callback", State: "bound-state", Verifier: verifier, Nonce: "bound-nonce", Subject: "alice", StartedAt: now.Add(-time.Second)}
	authURL, err := client.BuildReauthenticationURL(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("max_age") != "0" || q.Get("prompt") != "login" || q.Get("nonce") != request.Nonce || q.Get("state") != request.State || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(verifier) {
		t.Fatalf("unexpected reauthentication URL: %s", authURL)
	}
	for _, tc := range []struct {
		name                           string
		override                       map[string]any
		remove                         string
		state                          string
		ordinary, badSignature, wantOK bool
	}{
		{name: "fresh same account", wantOK: true},
		{name: "exact start second", override: map[string]any{"auth_time": request.StartedAt.Unix()}, wantOK: true},
		{name: "missing auth time", remove: "auth_time"},
		{name: "null auth time", override: map[string]any{"auth_time": nil}},
		{name: "string auth time", override: map[string]any{"auth_time": "123"}},
		{name: "fractional auth time", override: map[string]any{"auth_time": float64(now.Unix()) + 0.5}},
		{name: "zero auth time", override: map[string]any{"auth_time": 0}},
		{name: "negative auth time", override: map[string]any{"auth_time": -1}},
		{name: "fresh token old authentication", override: map[string]any{"auth_time": request.StartedAt.Unix() - 1}},
		{name: "future authentication", override: map[string]any{"auth_time": now.Add(time.Hour).Unix()}},
		{name: "authentication after issuance", override: map[string]any{"iat": request.StartedAt.Unix() - 1}},
		{name: "missing issuance", remove: "iat"},
		{name: "future issuance", override: map[string]any{"iat": now.Add(time.Hour).Unix()}},
		{name: "different account", override: map[string]any{"sub": "bob"}},
		{name: "wrong nonce", override: map[string]any{"nonce": "another-flow"}},
		{name: "wrong state", state: "another-flow"},
		{name: "wrong issuer", override: map[string]any{"iss": "https://other.example"}},
		{name: "wrong audience", override: map[string]any{"aud": "other-client"}},
		{name: "expired token", override: map[string]any{"exp": now.Add(-time.Minute).Unix()}},
		{name: "invalid signature", badSignature: true},
		{name: "ordinary login permits absent auth time", remove: "auth_time", ordinary: true, wantOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"iss": issuer, "aud": "client", "sub": "alice", "nonce": request.Nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "auth_time": now.Unix()}
			for k, v := range tc.override {
				raw[k] = v
			}
			delete(raw, tc.remove)
			payload, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload)
			digest := sha256.Sum256([]byte(input))
			signature, err := rsa.SignPKCS1v15(rand.Reader, key, stdcrypto.SHA256, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			if tc.badSignature {
				signature[0] ^= 1
			}
			codes.Store(tc.name, input+"."+base64.RawURLEncoding.EncodeToString(signature))
			state := request.State
			if tc.state != "" {
				state = tc.state
			}
			var claims *sso.IdentityClaims
			if tc.ordinary {
				claims, err = client.ExchangeCode(context.Background(), tc.name, request.Verifier, request.RedirectURI, request.Nonce)
			} else {
				claims, err = client.ExchangeReauthenticationCode(context.Background(), tc.name, state, request)
			}
			if tc.wantOK {
				if err != nil || claims == nil || claims.Subject != "alice" || claims.Provider != "kyidentity" {
					t.Fatalf("claims=%+v err=%v", claims, err)
				}
				if _, err := client.ExchangeReauthenticationCode(context.Background(), tc.name, state, request); err == nil {
					t.Fatal("consumed code replay accepted")
				}
			} else if err == nil || claims != nil {
				t.Fatalf("untrusted evidence released claims: %+v, %v", claims, err)
			}
		})
	}
}

func TestReauthenticationRejectsInvalidRequestBeforeNetworking(t *testing.T) {
	client := sso.NewKyIdentityClient(config.SSOConfig{}, nil)
	valid := sso.ReauthenticationRequest{RedirectURI: "https://app.example/callback", State: "state", Verifier: "verifier", Nonce: "nonce", Subject: "alice", StartedAt: time.Now()}
	for _, mutate := range []func(*sso.ReauthenticationRequest){
		func(r *sso.ReauthenticationRequest) { r.RedirectURI = "" },
		func(r *sso.ReauthenticationRequest) { r.State = "" },
		func(r *sso.ReauthenticationRequest) { r.Verifier = "" },
		func(r *sso.ReauthenticationRequest) { r.Nonce = "" },
		func(r *sso.ReauthenticationRequest) { r.Subject = "" },
		func(r *sso.ReauthenticationRequest) { r.StartedAt = time.Time{} },
		func(r *sso.ReauthenticationRequest) { r.StartedAt = time.Now().Add(-5 * time.Minute) },
		func(r *sso.ReauthenticationRequest) { r.StartedAt = time.Now().Add(time.Minute) },
	} {
		r := valid
		mutate(&r)
		if _, err := client.BuildReauthenticationURL(context.Background(), r); err != sso.ErrReauthenticationRequired {
			t.Fatalf("URL: got %v", err)
		}
		if _, err := client.ExchangeReauthenticationCode(context.Background(), "code", r.State, r); err != sso.ErrReauthenticationRequired {
			t.Fatalf("exchange: got %v", err)
		}
	}
	if _, err := client.ExchangeReauthenticationCode(context.Background(), "", valid.State, valid); err != sso.ErrReauthenticationRequired {
		t.Fatalf("empty code: %v", err)
	}
}
