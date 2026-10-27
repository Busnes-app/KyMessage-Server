package sso

import (
	"context"
	stdcrypto "crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/fips140"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// An ID token signed with an RSA key below 2048 bits is refused even though its signature,
// issuer, audience, expiry and nonce are valid; the 2048-bit control signs in.
func TestIDTokenRSAKeyFloor(t *testing.T) {
	for _, bits := range []int{1024, 2047, 2048} {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, bits)
			if err != nil {
				t.Fatal(err)
			}
			var issuer string
			idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
				case "/keys":
					_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k", "alg": "RS256", "use": "sig",
						"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
				case "/token":
					claims, _ := json.Marshal(map[string]any{"iss": issuer, "aud": "client", "sub": "s", "nonce": "n", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
					input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
					digest := sha256.Sum256([]byte(input))
					sig, err := rsa.SignPKCS1v15(rand.Reader, key, stdcrypto.SHA256, digest[:])
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": input + "." + base64.RawURLEncoding.EncodeToString(sig)})
				default:
					http.NotFound(w, r)
				}
			}))
			defer idp.Close()
			issuer = idp.URL
			claims, err := newOAuthFlow(issuer, "client", "").exchange(context.Background(), "code", "verifier", "https://app.example/cb", "n")
			if bits < 2048 {
				if err == nil || claims != nil {
					t.Fatalf("RSA-%d ID token accepted", bits)
				}
				return
			}
			if err != nil || claims.Subject != "s" {
				t.Fatalf("RSA-%d control: %v", bits, err)
			}
		})
	}
}

// The floor reads JWK members as go-jose does: case-sensitive, so a decoy "N" cannot hide
// the "n" the verifier uses; non-RSA keys pass through; redirects are refused.
func TestStrongJWKSTransportInput(t *testing.T) {
	strong, _ := rsa.GenerateKey(rand.Reader, 2048)
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	n := func(k *rsa.PrivateKey) string { return base64.RawURLEncoding.EncodeToString(k.N.Bytes()) }
	for name, tc := range map[string]struct {
		body   string
		status int
		ok     bool
	}{
		"strong":          {`{"keys":[{"kty":"RSA","n":"` + n(strong) + `","e":"AQAB"}]}`, 200, true},
		"weak":            {`{"keys":[{"kty":"RSA","n":"` + n(weak) + `","e":"AQAB"}]}`, 200, false},
		"decoy member":    {`{"keys":[{"kty":"RSA","N":"` + n(strong) + `","n":"` + n(weak) + `","e":"AQAB"}]}`, 200, false},
		"one weak of two": {`{"keys":[{"kty":"RSA","n":"` + n(strong) + `","e":"AQAB"},{"kty":"RSA","n":"` + n(weak) + `","e":"AQAB"}]}`, 200, false},
		"ec":              {`{"keys":[{"kty":"EC","crv":"P-256","x":"AA","y":"AA"}]}`, 200, true},
		"redirect":        {``, http.StatusFound, false},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status == http.StatusFound {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			client := &http.Client{Transport: strongJWKSTransport{base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := client.Get(srv.URL)
			if resp != nil {
				resp.Body.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
			if err != nil && tc.status == 200 && !strings.Contains(err.Error(), "2048") {
				t.Fatalf("refusal does not name the floor: %v", err)
			}
		})
	}
}

// Under GODEBUG=fips140=on, Go's TLS client refuses a chain whose explicitly trusted root is
// below RSA-2048, before any HTTP request reaches the IdP; a strong root and the ordinary
// profile connect. The leaf is P-256 in every case, so the root alone decides.
func TestOIDCTrustedRootKeyFloorUnderProfile(t *testing.T) {
	for _, bits := range []int{1024, 2047, 2048} {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			rootKey, err := rsa.GenerateKey(rand.Reader, bits)
			if err != nil {
				t.Fatal(err)
			}
			leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
			rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
			if err != nil {
				t.Fatal(err)
			}
			root, _ = x509.ParseCertificate(rootDER)
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
			leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, rootKey)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var issuer string
			idp := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys"})
			}))
			idp.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: leafKey}}}
			idp.StartTLS()
			defer idp.Close()
			issuer = idp.URL
			pool := x509.NewCertPool()
			pool.AddCert(root)
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
			defer client.CloseIdleConnections()
			ctx := oidc.ClientContext(context.Background(), client)
			_, err = newOAuthFlow(issuer, "client", "").authCodeURL(ctx, "https://app.example/cb", "s", "v", "n")
			refuse := bits < 2048 && fips140.Enabled()
			if (err != nil) != refuse {
				t.Fatalf("RSA-%d root, profile=%v: %v", bits, fips140.Enabled(), err)
			}
			if refuse && calls.Load() != 0 {
				t.Fatal("an HTTP request crossed a weak-root TLS connection")
			}
		})
	}
}
