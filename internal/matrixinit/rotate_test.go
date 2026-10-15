package matrixinit

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// Deleting a database password rotates it only if Postgres learns the new one: init.sql runs on
// an empty volume only, so matrix-init flags the rotation and renders role-passwords.sql.
func TestDBPasswordRotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	res, err := runWithSecret(t, goodInput(), dir, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if res.RotatedDBPassword {
		t.Fatal("a first run is not a rotation")
	}
	if res, err = Run(goodInput(), dir); err != nil || res.RotatedDBPassword {
		t.Fatalf("a re-run that keeps every secret is a rotation: %v %v", res.RotatedDBPassword, err)
	}
	old := readAll(t, filepath.Join(dir, "secrets"))
	os.Remove(filepath.Join(dir, "secrets", "mas_db_password"))
	if res, err = Run(goodInput(), dir); err != nil || !res.RotatedDBPassword {
		t.Fatalf("deleting mas_db_password was not flagged: %v %v", res.RotatedDBPassword, err)
	}
	pw, _ := os.ReadFile(filepath.Join(dir, "secrets", "mas_db_password"))
	if string(pw) == old["mas_db_password"] {
		t.Fatal("the password was not regenerated")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "postgres", "role-passwords.sql"))
	for _, want := range []string{"ALTER ROLE mas PASSWORD '" + string(pw) + "';", "ALTER ROLE synapse PASSWORD '" + old["synapse_db_password"] + "';"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("role-passwords.sql lacks %q:\n%s", want, b)
		}
	}
	// After a restore only the superuser password is new: a fresh volume, not a rotation.
	os.Remove(filepath.Join(dir, "secrets", "postgres_password"))
	if res, err = Run(goodInput(), dir); err != nil || res.RotatedDBPassword {
		t.Fatalf("a restore was flagged as a rotation: %v %v", res.RotatedDBPassword, err)
	}
}

// Against a real Postgres (CI's job sets KY_TEST_POSTGRES_DSN as a superuser): roles made with
// the old passwords, as init.sql left them, accept only the new ones after role-passwords.sql.
func TestRolePasswordsSQLRotatesOnPostgres(t *testing.T) {
	dsn := os.Getenv("KY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("KY_TEST_POSTGRES_DSN not set")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var taken bool
	if err := admin.QueryRowContext(ctx, `SELECT count(*) > 0 FROM pg_roles WHERE rolname IN ('synapse', 'mas')`).Scan(&taken); err != nil {
		t.Fatal(err)
	}
	if taken {
		t.Skip("roles synapse or mas already exist on this server; not touching them")
	}
	t.Cleanup(func() { admin.ExecContext(ctx, `DROP ROLE IF EXISTS synapse, mas`) })
	for _, q := range []string{`CREATE USER synapse PASSWORD 'old-synapse'`, `CREATE USER mas PASSWORD 'old-mas'`} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "postgres", "role-passwords.sql"))
	var n int
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "ALTER ROLE ") {
			if _, err := admin.ExecContext(ctx, line); err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d ALTER ROLE statements, want 2:\n%s", n, b)
	}
	login := func(role, pw string) error {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.User, cfg.Password = role, pw
		db := stdlib.OpenDB(*cfg)
		defer db.Close()
		return db.PingContext(ctx)
	}
	secrets := readAll(t, filepath.Join(dir, "secrets"))
	for _, role := range []string{"synapse", "mas"} {
		if err := login(role, secrets[role+"_db_password"]); err != nil {
			t.Errorf("%s with the rotated password: %v", role, err)
		}
		if err := login(role, "old-"+role); err == nil {
			t.Errorf("%s still accepts the old password", role)
		}
	}
}
