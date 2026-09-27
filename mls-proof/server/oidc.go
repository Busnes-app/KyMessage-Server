//go:build mlsproof

package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync"
	"time"

	kycrypto "github.com/Busnes-app/ky_server_base/internal/crypto"
)

const oidcClient = "mls-proof-client"
const oidcSecret = "disposable-local-issuer-secret"
const oidcRedirect = "http://127.0.0.1:4178/api/sso/kysignon/callback"
const recoveryRedirect = "http://127.0.0.1:4178/api/messaging/recovery-auth/callback"

// A test issuer, not an identity service: names are selected without passwords.
// The product still performs its real discovery, PKCE exchange and ID-token checks.
func newProofIssuer() (*httptest.Server, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	type authorization struct {
		subject, challenge, nonce, redirect string
		authenticatedAt                     int64
		expires                             time.Time
	}
	// Each code owns one immutable record. LoadAndDelete consumes it once.
	var codes sync.Map
	var issuer string
	name := regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	page := template.Must(template.New("authorize").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Disposable identity provider</title></head><body><h1>Test suite identity</h1><p>Local fixture only. No real accounts or passwords.</p><form method="post"><label>Test identity <input name="subject" required maxlength="32"></label><button>Continue to KyMessages</button></form></body></html>`))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "proof", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != oidcClient || (q.Get("redirect_uri") != oidcRedirect && q.Get("redirect_uri") != recoveryRedirect) || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("state") == "" || q.Get("nonce") == "" {
			http.Error(w, "invalid proof authorization request", 400)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = page.Execute(w, nil)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil || !name.MatchString(r.PostForm.Get("subject")) {
			http.Error(w, "invalid disposable identity", 400)
			return
		}
		code := kycrypto.RandomHex(32)
		codes.Store(code, authorization{subject: r.PostForm.Get("subject"), challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri"), authenticatedAt: time.Now().Unix(), expires: time.Now().Add(2 * time.Minute)})
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusSeeOther)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		client, secret, ok := r.BasicAuth()
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if !ok {
			client, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		if client != oidcClient || secret != oidcSecret || r.Form.Get("grant_type") != "authorization_code" {
			http.Error(w, "invalid client or redirect", 401)
			return
		}
		value, exists := codes.LoadAndDelete(r.Form.Get("code"))
		if !exists {
			http.Error(w, "invalid code", 400)
			return
		}
		a := value.(authorization)
		challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("redirect_uri") != a.redirect || time.Now().After(a.expires) || base64.RawURLEncoding.EncodeToString(challenge[:]) != a.challenge {
			http.Error(w, "expired code or invalid PKCE", 400)
			return
		}
		claims, err := json.Marshal(map[string]any{"iss": issuer, "aud": oidcClient, "sub": a.subject, "nonce": a.nonce, "iat": time.Now().Unix(), "auth_time": a.authenticatedAt, "exp": time.Now().Add(5 * time.Minute).Unix(), "preferred_username": a.subject, "name": "Test " + a.subject})
		if err != nil {
			http.Error(w, "claims failed", 500)
			return
		}
		input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"proof"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
		digest := sha256.Sum256([]byte(input))
		signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			http.Error(w, "signing failed", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "disposable-not-used", "token_type": "Bearer", "id_token": input + "." + base64.RawURLEncoding.EncodeToString(signature)})
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self' http://127.0.0.1:4178; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		mux.ServeHTTP(w, r)
	}))
	issuer = server.URL
	return server, nil
}
