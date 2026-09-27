package store_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func recoverySetup(t *testing.T, st store.Store) (store.MessagingActor, store.MessagingEnrollment, store.MessagingRecoveryAuthentication) {
	t.Helper()
	ctx := context.Background()
	actor := messagingActor(t, st, "alice")
	first, sig := messagingEnrollment(t, actor)
	if err := st.Messaging().EnrollDevice(ctx, actor, first); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Messaging().VerifyDevice(ctx, actor, first.Device.ID, sig); err != nil {
		t.Fatal(err)
	}
	pending, sig := messagingEnrollment(t, actor)
	if err := st.Messaging().EnrollDevice(ctx, actor, pending); err != nil {
		t.Fatal(err)
	}
	if d, err := st.Messaging().VerifyDevice(ctx, actor, pending.Device.ID, sig); err != nil || d.Status != "pending" {
		t.Fatalf("pending: %+v %v", d, err)
	}
	actor.DeviceTokenHash = pending.TokenHash
	request := store.MessagingRecoveryAuthentication{StateHash: crypto.RandomHex(32), DeviceID: pending.Device.ID, SealedRequest: "opaque-test-ciphertext", CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() + 299}
	return actor, first, request
}

func TestMessagingRecoveryAuthenticationBindings(t *testing.T) {
	for _, scenario := range []string{"success", "wrong session", "wrong account", "wrong device token", "wrong subject", "registry changed", "target revoked", "session revoked", "subject changed", "expired", "capacity"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			cfg := testdb.Config(t)
			st, err := store.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			actor, first, request := recoverySetup(t, st)
			if scenario == "wrong device token" {
				actor.DeviceTokenHash = first.TokenHash
				if err := st.Messaging().BeginRecoveryAuthentication(ctx, actor, request); !errors.Is(err, store.ErrMessagingDenied) {
					t.Fatal(err)
				}
				return
			}
			if err := st.Messaging().BeginRecoveryAuthentication(ctx, actor, request); err != nil {
				t.Fatal(err)
			}
			if scenario == "capacity" {
				for i := 0; i < 3; i++ {
					request.StateHash = crypto.RandomHex(32)
					if err := st.Messaging().BeginRecoveryAuthentication(ctx, actor, request); err != nil {
						t.Fatal(err)
					}
				}
				request.StateHash = crypto.RandomHex(32)
				if err := st.Messaging().BeginRecoveryAuthentication(ctx, actor, request); !errors.Is(err, store.ErrMessagingLimit) {
					t.Fatal(err)
				}
				return
			}
			switch scenario {
			case "wrong session":
				actor.SessionHash = crypto.RandomHex(32)
				if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: actor.SessionHash, UserID: actor.UserID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, ""); err != nil {
					t.Fatal(err)
				}
			case "wrong account":
				actor = messagingActor(t, st, "bob")
			case "registry changed":
				if err := st.Messaging().RevokeDevice(ctx, actor, first.Device.ID); err != nil {
					t.Fatal(err)
				}
			case "target revoked":
				if err := st.Messaging().RevokeDevice(ctx, actor, request.DeviceID); err != nil {
					t.Fatal(err)
				}
			case "session revoked":
				if err := st.Sessions().DeleteSession(ctx, actor.SessionHash); err != nil {
					t.Fatal(err)
				}
			case "subject changed":
				u, err := st.Users().GetUserByID(ctx, actor.UserID)
				if err != nil {
					t.Fatal(err)
				}
				u.SSOSubject = "changed"
				if err := st.Users().UpdateUser(ctx, u); err != nil {
					t.Fatal(err)
				}
			case "expired":
				driver := cfg.Driver
				if driver == "postgres" {
					driver = "pgx"
				}
				db, err := sql.Open(driver, cfg.DSN)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.ExecContext(ctx, `UPDATE messaging_recovery_auth SET expires_at = 1`); err != nil {
					t.Fatal(err)
				}
			}
			actor.DeviceTokenHash = "" // Browser redirects carry only the original suite session.
			got, err := st.Messaging().RecoveryAuthentication(ctx, actor, request.StateHash)
			if scenario == "success" || scenario == "wrong subject" {
				if err != nil || got.SealedRequest != request.SealedRequest || got.Subject != "alice" || got.PublicKey == "" || got.RegistryHash == "" {
					t.Fatalf("%+v %v", got, err)
				}
			} else if err == nil || got.SealedRequest != "" {
				t.Fatalf("leaked request: %+v %v", got, err)
			}
			subject := "alice"
			if scenario == "wrong subject" {
				subject = "bob"
			}
			err = st.Messaging().CompleteRecoveryAuthentication(ctx, actor, request.StateHash, subject)
			if scenario != "success" {
				if err == nil {
					t.Fatal("invalid binding consumed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Messaging().CompleteRecoveryAuthentication(ctx, actor, request.StateHash, subject); !errors.Is(err, store.ErrMessagingDenied) {
				t.Fatalf("replay: %v", err)
			}
			devices, err := st.Messaging().ListDevices(ctx, actor)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range devices {
				if d.ID == request.DeviceID && d.Status != "pending" {
					t.Fatal("authentication approved device")
				}
			}
		})
	}
}

func TestMessagingRecoveryAuthenticationConcurrentConsumption(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	first, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	actor, _, request := recoverySetup(t, first)
	if err := first.Messaging().BeginRecoveryAuthentication(ctx, actor, request); err != nil {
		t.Fatal(err)
	}
	// The independent store connection sees durable state, not a process-local map.
	if _, err := second.Messaging().RecoveryAuthentication(ctx, actor, request.StateHash); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, st := range []store.Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- st.Messaging().CompleteRecoveryAuthentication(ctx, actor, request.StateHash, "alice")
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, store.ErrMessagingDenied) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("successful consumptions: %d", success)
	}
}
