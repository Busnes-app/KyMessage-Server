package main

import (
	"context"
	"errors"
	"strings"
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

type failingCount struct{ store.UserStore }

func (failingCount) CountUsers(context.Context) (int, error) {
	return 0, errors.New("database is locked")
}

type countFailStore struct{ store.Store }

func (s countFailStore) Users() store.UserStore { return failingCount{s.Store.Users()} }

// A transient count failure is not an empty database: it must not mint a second admin.
func TestBootstrapAdminRefusesOnCountError(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := bootstrapAdmin(ctx, countFailStore{st}, "admin", ""); err == nil {
		t.Fatal("bootstrap went ahead after a failed count")
	}
	if n, _ := st.Users().CountUsers(ctx); n != 0 {
		t.Fatalf("users after a failed count: %d", n)
	}
}

// init-admin takes the password from stdin; a piped line loses only its line ending.
func TestReadPasswordLine(t *testing.T) {
	for in, want := range map[string]string{"pass word 123\n": "pass word 123", "crlf-password\r\n": "crlf-password", "no-newline": "no-newline", "": ""} {
		got, err := readPasswordLine(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
}
