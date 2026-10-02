package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/backup/media"
)

// restoreFixture is a restored tree with dumps for both databases, a media backup of one file,
// an empty media store, and a fake pg_restore that answers --list, reads a whole dump for
// --file=/dev/null (a complete one ends in PGEND) and logs every restore.
func restoreFixture(t *testing.T, toc string) (backup.MatrixRestore, string) {
	t.Helper()
	mdir, data, backups, mediaDir := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	for rel, body := range map[string]string{
		"secrets/mas_db_password": "maspw\n", "secrets/synapse_db_password": "synpw\n",
		"dumps/mas.dump.000": "PGDMP mas\n" + toc + "PGEND\n", "dumps/synapse.dump.000": "PGDMP syn part 0\n",
		"dumps/synapse.dump.001": "; TABLE public events synapse\n", "dumps/synapse.dump.002": "PGEND\n",
	} {
		p := filepath.Join(mdir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	key, err := keyfile.LoadOrCreate(backup.MediaKeyPath(data), 32)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "local_content", "ab", "cd"), 0o700)
	_ = os.WriteFile(filepath.Join(src, "local_content", "ab", "cd", "img"), []byte("image bytes"), 0o600)
	if _, err := media.Run(context.Background(), src, filepath.Join(backups, "media"), key, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log")
	fakeTool(t, "pg_restore", fmt.Sprintf(`if [ "$1" = --list ]; then
  IFS= read -r first || [ -n "$first" ] || exit 1
  case $first in PGDMP*) ;; *) echo 'pg_restore: error: not an archive' >&2; exit 1 ;; esac
  while IFS= read -r line; do echo "$line"; done
  exit 0
fi
if [ "$1" = --file=/dev/null ]; then
  last=
  while IFS= read -r line; do last=$line; done
  [ "$last" = PGEND ] && exit 0
  echo 'pg_restore: error: could not read from input file: end of file' >&2
  exit 1
fi
echo "$* env=$PGPASSWORD" >> %s
`, log))
	r := backup.MatrixRestore{MatrixDir: mdir, MediaDir: mediaDir, BackupDir: backups, DataDir: data, DBHost: "postgres",
		Relations: func(context.Context, string, string, string, string) (int, error) { return 0, nil }}
	return r, log
}

func TestMatrixRestoreRestoresDumpsThenMedia(t *testing.T) {
	r, log := restoreFixture(t, "; TABLE public users mas\n")
	var out bytes.Buffer
	if err := r.Run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "--dbname=mas") || !strings.Contains(lines[1], "--dbname=synapse") {
		t.Fatalf("pg_restore calls %q, want mas then synapse", lines)
	}
	for _, want := range []string{"--host=postgres ", "--username=mas", "--no-owner", "--no-privileges", "--single-transaction", "--exit-on-error"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("pg_restore lacks %s: %q", want, lines[0])
		}
	}
	if args, env, _ := strings.Cut(lines[0], " env="); env != "maspw" || strings.Contains(args, "maspw") || !strings.HasSuffix(lines[1], " env=synpw") {
		t.Errorf("owner passwords must reach pg_restore by PGPASSWORD only: %q", lines)
	}
	if b, err := os.ReadFile(filepath.Join(r.MediaDir, "local_content", "ab", "cd", "img")); err != nil || string(b) != "image bytes" {
		t.Fatalf("media not restored: %q %v", b, err)
	}
	if !strings.Contains(out.String(), "Restored database synapse") || !strings.Contains(out.String(), "Restored 1 media files") {
		t.Errorf("output %q", out.String())
	}
}

// A host:port reaches pg_restore as --host and --port, and the relation count gets it whole.
func TestMatrixRestoreTakesAPort(t *testing.T) {
	r, log := restoreFixture(t, "")
	r.DBHost = "db.internal:5433"
	var hosts []string
	r.Relations = func(_ context.Context, host, _, _, _ string) (int, error) {
		hosts = append(hosts, host)
		return 0, nil
	}
	if err := r.Run(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "--host=db.internal --port=5433 ") || strings.Join(hosts, ",") != "db.internal:5433,db.internal:5433" {
		t.Fatalf("calls %q, relations hosts %q", calls, hosts)
	}
}

func TestMatrixRestoreRefusesANonEmptyStack(t *testing.T) {
	r, log := restoreFixture(t, "")
	r.Relations = func(_ context.Context, _, db, _, _ string) (int, error) {
		if db == "synapse" {
			return 7, nil
		}
		return 0, nil
	}
	err := r.Run(context.Background(), io.Discard)
	// The refusal fires on a live stack too: it must name what down -v destroys.
	if err == nil || !strings.Contains(err.Error(), "already holds 7 relations") || !strings.Contains(err.Error(), "deletes its Matrix database and media") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, r, log, 0)
}

// assertUntouched: no pg_restore ran and the media store still holds only its seeded entries.
func assertUntouched(t *testing.T, r backup.MatrixRestore, log string, seeded int) {
	t.Helper()
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Error("pg_restore ran after a failed check")
	}
	if entries, _ := os.ReadDir(r.MediaDir); len(entries) != seeded {
		t.Errorf("media store written after a failed check: %v", entries)
	}
}

func TestMatrixRestoreRefusesBeforeWriting(t *testing.T) {
	for name, spoil := range map[string]func(r backup.MatrixRestore) int{
		"stray part": func(r backup.MatrixRestore) int {
			_ = os.WriteFile(filepath.Join(r.MatrixDir, "dumps", "mas.dump.002"), []byte("x"), 0o600)
			return 0
		},
		"missing dump": func(r backup.MatrixRestore) int {
			_ = os.Remove(filepath.Join(r.MatrixDir, "dumps", "mas.dump.000"))
			return 0
		},
		// --list reads only the TOC at the front of .000; the data blocks need a full read.
		"missing final part": func(r backup.MatrixRestore) int {
			_ = os.Remove(filepath.Join(r.MatrixDir, "dumps", "synapse.dump.002"))
			return 0
		},
		"truncated final part": func(r backup.MatrixRestore) int {
			_ = os.WriteFile(filepath.Join(r.MatrixDir, "dumps", "synapse.dump.002"), []byte("PGE"), 0o600)
			return 0
		},
		"corrupt dump": func(r backup.MatrixRestore) int {
			_ = os.WriteFile(filepath.Join(r.MatrixDir, "dumps", "synapse.dump.000"), []byte("garbage\n"), 0o600)
			return 0
		},
		"missing password": func(r backup.MatrixRestore) int {
			_ = os.Remove(filepath.Join(r.MatrixDir, "secrets", "synapse_db_password"))
			return 0
		},
		"media store not empty": func(r backup.MatrixRestore) int {
			_ = os.WriteFile(filepath.Join(r.MediaDir, "already-here"), nil, 0o600)
			return 1
		},
		"swapped media": func(r backup.MatrixRestore) int {
			m := filepath.Join(r.BackupDir, "media")
			full, _ := filepath.Glob(filepath.Join(m, "full-*.tar"))
			for _, f := range full {
				_ = os.Remove(f)
			}
			_ = os.WriteFile(filepath.Join(m, "mirror", "local_content", "ab", "cd", "img"), []byte("0123456789012345678901234567890123"), 0o600)
			return 0
		},
		"no media backup": func(r backup.MatrixRestore) int {
			_ = os.RemoveAll(filepath.Join(r.BackupDir, "media"))
			return 0
		},
		"no media key": func(r backup.MatrixRestore) int {
			_ = os.Remove(backup.MediaKeyPath(r.DataDir))
			return 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, log := restoreFixture(t, "")
			seeded := spoil(r)
			if err := r.Run(context.Background(), io.Discard); err == nil || !strings.HasPrefix(err.Error(), "refused: ") {
				t.Fatalf("err = %v, want a refusal", err)
			}
			assertUntouched(t, r, log, seeded)
		})
	}
}

func TestMatrixRestoreRefusesAnExtension(t *testing.T) {
	r, log := restoreFixture(t, "; 3; 3079 16385 EXTENSION - pg_trgm\n")
	if err := r.Run(context.Background(), io.Discard); err == nil || !strings.Contains(err.Error(), "extension") {
		t.Fatalf("err = %v", err)
	}
	assertUntouched(t, r, log, 0)
}

func TestMatrixRestoreSkipMedia(t *testing.T) {
	r, log := restoreFixture(t, "")
	r.SkipMedia = true
	_ = os.Remove(backup.MediaKeyPath(r.DataDir))
	_ = os.WriteFile(filepath.Join(r.MediaDir, "already-here"), nil, 0o600)
	var out bytes.Buffer
	if err := r.Run(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(log); strings.Count(string(calls), "\n") != 2 || !strings.Contains(out.String(), "Media skipped") {
		t.Fatalf("calls %q, out %q", calls, out.String())
	}
}

// The owners' passwords never reach an error, whether the connection or the restore fails.
func TestMatrixRestoreErrorsNeverCarryThePassword(t *testing.T) {
	const pw = "owner-pw-7f3a9c"
	if _, err := backup.CountRelations(context.Background(), "127.0.0.1:1", "synapse", "synapse", pw); err == nil || strings.Contains(err.Error(), pw) {
		t.Fatalf("CountRelations against a closed port: %v", err)
	}
	r, _ := restoreFixture(t, "")
	_ = os.WriteFile(filepath.Join(r.MatrixDir, "secrets", "mas_db_password"), []byte(pw), 0o600)
	r.Relations, r.DBHost = nil, "127.0.0.1:1"
	if err := r.Run(context.Background(), io.Discard); err == nil || strings.Contains(err.Error(), pw) {
		t.Fatalf("Run with an unreachable database: %v", err)
	}
	r.Relations = func(context.Context, string, string, string, string) (int, error) { return 0, nil }
	fakeTool(t, "pg_restore", `case $1 in --list) exec /bin/cat ;; --file=/dev/null) exit 0 ;; esac
echo 'pg_restore: error: connection failed: password authentication failed for user "mas"' >&2
exit 1
`)
	if err := r.Run(context.Background(), io.Discard); err == nil || strings.Contains(err.Error(), pw) || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("Run with a failing pg_restore: %v", err)
	}
}

// CountRelations sees a table created in any user schema, and a wrong password's error does not
// carry it. Needs KY_TEST_POSTGRES_DSN.
func TestCountRelationsSeesUserTables(t *testing.T) {
	dsn := os.Getenv("KY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("KY_TEST_POSTGRES_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	db := strings.TrimPrefix(u.Path, "/")
	ctx := context.Background()
	before, err := backup.CountRelations(ctx, u.Host, db, u.User.Username(), pw)
	if err != nil {
		t.Fatal(err)
	}
	const wrong = "wrong-pw-5d1e"
	if _, err := backup.CountRelations(ctx, u.Host, db, u.User.Username(), wrong); err == nil || strings.Contains(err.Error(), wrong) {
		t.Fatalf("wrong password: %v", err)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	schema := fmt.Sprintf("countrel_%d", time.Now().UnixNano())
	if _, err := conn.ExecContext(ctx, "CREATE SCHEMA "+schema+"; CREATE TABLE "+schema+".t (id int)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE") })
	if after, err := backup.CountRelations(ctx, u.Host, db, u.User.Username(), pw); err != nil || after != before+1 {
		t.Fatalf("before %d, after %d (%v)", before, after, err)
	}
}

// pg_restore's CONTEXT and DETAIL lines can quote row data; they never reach the error.
func TestMatrixRestoreErrorsDropRowData(t *testing.T) {
	r, _ := restoreFixture(t, "")
	fakeTool(t, "pg_restore", `case $1 in --list) exec /bin/cat ;; --file=/dev/null) exit 0 ;; esac
echo 'pg_restore: error: COPY failed for table "users": ERROR:  invalid input syntax' >&2
echo 'CONTEXT:  COPY users, line 1: "row-secret-1"' >&2
echo 'DETAIL:  Key (name)=(row-secret-2) already exists.' >&2
echo 'pg_restore: detail: Command was: INSERT row-secret-3' >&2
exit 1
`)
	err := r.Run(context.Background(), io.Discard)
	if err == nil || strings.Contains(err.Error(), "row-secret") || !strings.Contains(err.Error(), "COPY failed") {
		t.Fatalf("err = %v", err)
	}
}
