package api_test

import (
	"context"
	stdcrypto "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func TestMessagingRecoveryAuthenticationCallback(t *testing.T) {
	for _, reset := range []bool{false, true} {
		for _, scenario := range []string{"success", "wrong subject", "missing auth time", "old auth time", "session revoked during exchange", "registry changed during exchange", "other session", "other browser", "tampered sealed state", "disabled before callback"} {
			t.Run(fmt.Sprintf("reset=%t/%s", reset, scenario), func(t *testing.T) {
				_, st, cfg := setupTestServer(t)
				ctx := context.Background()
				session := messagingLogin(t, st, "alice")
				actor := store.MessagingActor{UserID: "alice", SessionHash: crypto.SHA256Hex([]byte(session))}
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
				var issuer string
				var codes sync.Map
				var first enrolledDevice
				idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/.well-known/openid-configuration":
						_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
					case "/keys":
						_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
					case "/token":
						if err := r.ParseForm(); err != nil {
							http.Error(w, "bad form", 400)
							return
						}
						value, ok := codes.LoadAndDelete(r.Form.Get("code"))
						if !ok {
							http.Error(w, "bad code", 400)
							return
						}
						q := value.(url.Values)
						challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
						if base64.RawURLEncoding.EncodeToString(challenge[:]) != q.Get("code_challenge") || r.Form.Get("redirect_uri") != q.Get("redirect_uri") {
							http.Error(w, "bad binding", 400)
							return
						}
						claims := map[string]any{"iss": issuer, "aud": "client", "sub": "alice", "nonce": q.Get("nonce"), "auth_time": time.Now().Unix(), "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
						switch scenario {
						case "wrong subject":
							claims["sub"] = "bob"
						case "missing auth time":
							delete(claims, "auth_time")
						case "old auth time":
							claims["auth_time"] = time.Now().Add(-time.Hour).Unix()
						case "session revoked during exchange":
							if err := st.Sessions().DeleteSession(ctx, actor.SessionHash); err != nil {
								t.Error(err)
							}
						case "registry changed during exchange":
							if err := st.Messaging().RevokeDevice(ctx, actor, first.ID); err != nil {
								t.Error(err)
							}
						}
						body, err := json.Marshal(claims)
						if err != nil {
							t.Error(err)
							return
						}
						input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test"}`)) + "." + base64.RawURLEncoding.EncodeToString(body)
						sum := sha256.Sum256([]byte(input))
						signature, err := rsa.SignPKCS1v15(rand.Reader, key, stdcrypto.SHA256, sum[:])
						if err != nil {
							t.Error(err)
							return
						}
						_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "unused", "token_type": "Bearer", "id_token": input + "." + base64.RawURLEncoding.EncodeToString(signature)})
					default:
						http.NotFound(w, r)
					}
				}))
				defer idp.Close()
				issuer = idp.URL
				cfg.SSO.KyIdentityIssuer = issuer
				cfg.SSO.KyIdentityClientID = "client"
				srv := api.NewServer(cfg, st)
				first = verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
				target := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
				path := "/api/messaging/devices/" + target.ID + "/recovery-auth"
				messagingCode(t, messagingRequest(t, srv, "POST", path, "", target.Token, struct{}{}), 401)
				messagingCode(t, messagingRequest(t, srv, "POST", path, session, first.Token, struct{}{}), 403)
				// Browser initiation remains CSRF protected.
				req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
				req.AddCookie(&http.Cookie{Name: "ky_session", Value: session})
				req.Header.Set("X-KyMessages-Device", target.Token)
				denied := httptest.NewRecorder()
				srv.ServeHTTP(denied, req)
				messagingCode(t, denied, 403)
				input := map[string]bool{"confirm_identity_reset": reset}
				if reset {
					messagingCode(t, messagingRequest(t, srv, "POST", path, session, target.Token, input), 403)
					cfg.Messaging.IdentityResetEnabled = true
					srv = api.NewServer(cfg, st)
				}
				started := messagingRequest(t, srv, "POST", path, session, target.Token, input)
				messagingCode(t, started, 201)
				var binder *http.Cookie
				for _, c := range started.Result().Cookies() {
					if strings.HasPrefix(c.Name, "ky_reauth_") {
						binder = c
					}
				}
				if binder == nil || !binder.HttpOnly || binder.Path != "/api/messaging/recovery-auth/callback" {
					t.Fatalf("no browser binder cookie: %v", started.Result().Cookies())
				}
				var reply struct {
					URL   string `json:"authorization_url"`
					Reset bool   `json:"identity_reset_available"`
				}
				if err := json.Unmarshal(started.Body.Bytes(), &reply); err != nil {
					t.Fatal(err)
				}
				authorization, err := url.Parse(reply.URL)
				if err != nil {
					t.Fatal(err)
				}
				q := authorization.Query()
				if q.Get("prompt") != "login" || q.Get("max_age") != "0" || reply.Reset != reset {
					t.Fatal("invalid recovery request")
				}
				record, err := st.Messaging().RecoveryAuthentication(ctx, actor, crypto.SHA256Hex([]byte(q.Get("state"))))
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(record.SealedRequest, q.Get("nonce")) || strings.Contains(record.SealedRequest, actor.SessionHash) {
					t.Fatal("unsealed request")
				}
				if scenario == "tampered sealed state" {
					cfg.Security.EncryptionKey = make([]byte, 32)
					srv = api.NewServer(cfg, st)
				}
				if reset && scenario == "disabled before callback" {
					cfg.Messaging.IdentityResetEnabled = false
					srv = api.NewServer(cfg, st)
				}
				codes.Store("test-code", q)
				if scenario == "success" {
					// No flow state may depend on the original server instance.
					srv = api.NewServer(cfg, st)
				}
				callback := "/api/messaging/recovery-auth/callback?" + url.Values{"state": {q.Get("state")}, "code": {"test-code"}}.Encode()
				callbackSession := session
				if scenario == "other session" {
					callbackSession = crypto.RandomHex(32)
					if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: crypto.SHA256Hex([]byte(callbackSession)), UserID: "alice", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, ""); err != nil {
						t.Fatal(err)
					}
				}
				// A normal browser callback has the original HttpOnly suite cookie and no device token.
				req = httptest.NewRequest("GET", callback, nil)
				req.AddCookie(&http.Cookie{Name: "ky_session", Value: callbackSession})
				// A stolen session can start the flow, but the victim's browser never holds its binder.
				if scenario != "other browser" {
					req.AddCookie(&http.Cookie{Name: binder.Name, Value: binder.Value})
				}
				response := httptest.NewRecorder()
				srv.ServeHTTP(response, req)
				expected := 401
				switch scenario {
				case "success":
					expected = 200
				case "disabled before callback":
					if reset {
						expected = 403
					} else {
						expected = 200
					}
				case "other session", "session revoked during exchange":
					expected = 403
				case "registry changed during exchange":
					expected = 409
				}
				messagingCode(t, response, expected)
				if scenario == "success" {
					for _, cookie := range response.Result().Cookies() {
						if cookie.Name == "ky_session" {
							t.Fatal("recovery callback replaced the original session")
						}
					}
					if reset {
						if !strings.Contains(response.Body.String(), `"identity_generation":2`) || !strings.Contains(response.Body.String(), `"identity_reset":true`) {
							t.Fatal(response.Body.String())
						}
					} else if !strings.Contains(response.Body.String(), `"reauthenticated":true`) || !strings.Contains(response.Body.String(), `"identity_reset_available":false`) {
						t.Fatal(response.Body.String())
					}
					replayStatus := 403
					if reset {
						replayStatus = 200
					}
					replay := messagingRequest(t, srv, "GET", callback, session, "", nil)
					messagingCode(t, replay, replayStatus)
					if reset && replay.Body.String() != response.Body.String() {
						t.Fatal("reset receipt changed on replay")
					}
					devices, err := st.Messaging().ListDevices(ctx, actor)
					if err != nil {
						t.Fatal(err)
					}
					for _, d := range devices {
						if reset {
							if d.ID == target.ID && (d.Status != "approved" || d.IdentityGeneration != 2) {
								t.Fatal("replacement not approved in generation 2")
							}
							if d.ID == first.ID && d.Status != "revoked" {
								t.Fatal("prior device not revoked")
							}
						} else if d.ID == target.ID && d.Status != "pending" {
							t.Fatal("callback approved device")
						}
					}
				}
			})
		}
	}

}
