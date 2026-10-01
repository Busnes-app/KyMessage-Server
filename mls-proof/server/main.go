//go:build mlsproof

// This disposable loopback fixture is excluded from production builds.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func main() {
	if err := serve(); err != nil {
		log.Fatal(err)
	}
}
func syntheticIdentity(user string) bool {
	return utf8.ValidString(user) && len(user) > 0 && len(user) <= 64 && !strings.ContainsFunc(user, unicode.IsControl)
}

func serve() error {
	dir, err := os.MkdirTemp("", "kymessages-mls-http-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.Setenv("KY_DATA_DIR", dir); err != nil {
		return err
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return err
	}
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "fixture.db")
	cfg.Server.AppURL = "http://127.0.0.1:4178"
	cfg.Captcha.Provider = "none"
	cfg.Security.CookieSecure = false // Loopback fixture only.
	cfg.Security.CookieDomain = ""
	cfg.SSO.KyIdentityIssuer = "" // Never inherit a live issuer from the operator's environment.
	oidcMode := os.Getenv("MLS_PROOF_OIDC") == "1"
	cfg.Messaging.IdentityResetEnabled = oidcMode
	if oidcMode {
		issuer, err := newProofIssuer()
		if err != nil {
			return err
		}
		defer issuer.Close()
		cfg.SSO.KyIdentityIssuer = issuer.URL
		cfg.SSO.KyIdentityClientID = oidcClient
		cfg.SSO.KyIdentitySecret = oidcSecret
		cfg.SSO.AutoProvision = true
	}
	st, err := store.Open(context.Background(), cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	mux := http.NewServeMux()
	// Test-only writes against this process's disposable database.
	fixtureDB, err := sql.Open("sqlite", cfg.Database.DSN+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer fixtureDB.Close()
	// Mirror restore-messages: approved devices lose their delivery token.
	mux.HandleFunc("POST /proof-fixture/suspend-devices/{user}", func(w http.ResponseWriter, r *http.Request) {
		user := r.PathValue("user")
		if !syntheticIdentity(user) {
			http.Error(w, "invalid synthetic identity", 400)
			return
		}
		if _, err := fixtureDB.ExecContext(r.Context(), "UPDATE messaging_devices SET token_hash = NULL, suspended_at = ? WHERE user_id = ? AND status = 'approved'", time.Now().Unix(), user); err != nil {
			http.Error(w, "fixture suspension failed", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	if !oidcMode {
		mux.HandleFunc("POST /proof-fixture/expire-room/{room}", func(w http.ResponseWriter, r *http.Request) {
			room := r.PathValue("room")
			id, err := uuid.Parse(room)
			if err != nil || id.String() != room {
				http.Error(w, "invalid room", 400)
				return
			}
			if _, err := fixtureDB.ExecContext(r.Context(), "UPDATE messaging_events SET created_at = created_at - 400*86400 WHERE room_id = ?", room); err != nil {
				http.Error(w, "fixture aging failed", 500)
				return
			}
			if err := st.Messaging().ExpireMessages(r.Context()); err != nil {
				http.Error(w, "fixture expiry failed", 500)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("POST /proof-fixture/session/{user}", func(w http.ResponseWriter, r *http.Request) {
			user := r.PathValue("user")
			if !syntheticIdentity(user) {
				http.Error(w, "invalid synthetic identity", 400)
				return
			}
			// Disposable demo identities have no passwords. Reissue a session after a
			// reload; this route must never exist on the production server.
			_, err := st.Users().GetUserByID(r.Context(), user)
			if errors.Is(err, store.ErrNotFound) {
				err = st.Users().CreateUser(r.Context(), &store.User{ID: user, Username: user, Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: user})
			}
			if err != nil {
				http.Error(w, "fixture account failed", 500)
				return
			}
			token := crypto.RandomHex(32)
			if err := st.Sessions().CreateSession(r.Context(), &store.Session{TokenHash: crypto.SHA256Hex([]byte(token)), UserID: user, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}, ""); err != nil {
				http.Error(w, "fixture session failed", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_ = json.NewEncoder(w).Encode(map[string]string{"session": token})
		})
	} else {
		mux.HandleFunc("/proof-fixture/session/", http.NotFound)
	}
	mux.HandleFunc("GET /proof-fixture/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	apiServer := api.NewServer(cfg, st)
	defer apiServer.WaitDetached()
	defer apiServer.StopMessaging()
	mux.Handle("/", apiServer)
	server := &http.Server{Addr: "127.0.0.1:4179", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		apiServer.StopMessaging()
		return server.Shutdown(shutdown)
	}
}
