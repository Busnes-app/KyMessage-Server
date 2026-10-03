package main

import (
	"context"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// The bootstrap admin takes KY_ADMIN_USERNAME, so KyIdentity's unrenamable admin can sync.
func TestBootstrapAdminUsesConfiguredUsername(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := bootstrapAdmin(ctx, st, "kymessages-admin", "a-long-bootstrap-password"); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().GetLocalUserByUsername(ctx, "kymessages-admin")
	if err != nil || u.Role != "admin" || !u.MustChangePassword || u.SSOProvider != "local" {
		t.Fatalf("bootstrap admin: %v %+v", err, u)
	}
	if _, err := st.Users().GetLocalUserByUsername(ctx, "admin"); err == nil {
		t.Fatal("an account named admin was created as well")
	}
	// Only an empty database is bootstrapped.
	if err := bootstrapAdmin(ctx, st, "second", "a-long-bootstrap-password"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.Users().CountUsers(ctx); n != 1 {
		t.Fatalf("users after a second start: %d", n)
	}
}
