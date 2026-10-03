package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// A write that loses a race to a concurrent insert of the same username meets the UNIQUE
// constraint after usernameTaken passed. Each engine's real error must become ErrUsernameTaken;
// other unique violations must not.
func TestUsernameUniqueViolationIsTyped(t *testing.T) {
	ctx := context.Background()
	opened, err := Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	s := opened.(*SQLStore)
	insert := func(id, username string) error {
		now := time.Now().UTC()
		_, err := s.db.ExecContext(ctx, s.rebind(`INSERT INTO users (id, username, created_at, updated_at) VALUES (?, ?, ?, ?)`), id, username, now, now)
		return err
	}
	if err := insert("u1", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := insert("u2", "other"); err != nil {
		t.Fatal(err)
	}

	dupName := insert("u3", "admin")
	if got := uniqueError(dupName); !errors.Is(got, ErrUsernameTaken) {
		t.Fatalf("username violation %v -> %v, want ErrUsernameTaken", dupName, got)
	}
	dupID := insert("u1", "fresh")
	if got := uniqueError(dupID); errors.Is(got, ErrUsernameTaken) || !errors.Is(got, ErrAlreadyExists) {
		t.Fatalf("id violation %v -> %v, want ErrAlreadyExists only", dupID, got)
	}
	_, rename := s.db.ExecContext(ctx, s.rebind(`UPDATE users SET username = ? WHERE id = ?`), "admin", "u2")
	if got := uniqueError(rename); !errors.Is(got, ErrUsernameTaken) {
		t.Fatalf("rename violation %v -> %v, want ErrUsernameTaken", rename, got)
	}
	if got := uniqueError(nil); got != nil {
		t.Fatalf("nil -> %v", got)
	}
}
