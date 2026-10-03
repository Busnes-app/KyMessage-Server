package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// fakeTool puts an executable shell script named name first on PATH. The real binaries get an
// empty environment, so the scripts use only shell builtins and absolute paths.
func fakeTool(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// matrixInstance is a SQLite instance with Matrix enabled: a matrix-init tree, and a fake
// pg_dump that prints masDump or synapseDump and logs its arguments one line per call.
func matrixInstance(t *testing.T, masDump, synapseDump []byte) (*config.Config, string) {
	t.Helper()
	cfg, _ := sqliteInstance(t)
	mdir := t.TempDir()
	for rel, body := range map[string]string{
		"secrets/synapse_db_password":      "spw",
		"secrets/postgres_password":        "superuser-pw",
		"secrets/.synapse_db_password.123": "matrix-init temp file",
		"synapse/signing.key":              "ed25519 a_abcd seed",
		"synapse/homeserver.yaml":          "server_name: example.com",
		"mas/config.yaml":                  "clients: []",
		"element/config.json":              "{}",
		"livekit/config.yaml":              "keys: {}",
		"livekit/turn.crt":                 "certificate",
		"livekit/turn.key":                 "private key",
		"postgres/init.sql":                "CREATE USER synapse;",
		"postgres/kybackup-role.sql":       "CREATE ROLE kybackup;",
	} {
		p := filepath.Join(mdir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Matrix = config.MatrixConfig{ServerName: "example.com", Dir: mdir, MediaDir: t.TempDir(),
		RTCHost: "https://sfu.example.com", DBHost: "postgres", BackupDBPassword: "pw-never-in-argv"}
	fx := t.TempDir()
	mas, syn, log := filepath.Join(fx, "mas"), filepath.Join(fx, "synapse"), filepath.Join(fx, "log")
	if err := os.WriteFile(mas, masDump, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(syn, synapseDump, 0o600); err != nil {
		t.Fatal(err)
	}
	fakeTool(t, "pg_dump", fmt.Sprintf(`echo "$* env=$PGPASSWORD" >> %s
case "$*" in *--dbname=mas*) exec /bin/cat %s ;; *) exec /bin/cat %s ;; esac
`, log, mas, syn))
	return cfg, log
}

func dumpOf(n int) []byte { return append([]byte("PGDMP"), bytes.Repeat([]byte{7}, n-5)...) }

func members(p recoveryclient.Payload, prefix string) []recoveryclient.File {
	var out []recoveryclient.File
	for _, f := range p.Files {
		if strings.HasPrefix(f.Path, prefix) {
			out = append(out, f)
		}
	}
	return out
}

func joined(files []recoveryclient.File) []byte {
	var b []byte
	for _, f := range files {
		b = append(b, f.Data...)
	}
	return b
}

func TestCollectSplitsDumpsAndRejoins(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	mas, syn := dumpOf(2500), dumpOf(700)
	cfg, log := matrixInstance(t, mas, syn)
	p, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range members(p, "matrix/dumps/") {
		names = append(names, f.Path)
	}
	want := []string{"matrix/dumps/mas.dump.000", "matrix/dumps/mas.dump.001", "matrix/dumps/mas.dump.002", "matrix/dumps/synapse.dump.000"}
	if !slices.Equal(names, want) {
		t.Fatalf("dump members %v, want %v (MAS first, from .000)", names, want)
	}
	if !bytes.Equal(joined(members(p, "matrix/dumps/mas.")), mas) || !bytes.Equal(joined(members(p, "matrix/dumps/synapse.")), syn) {
		t.Fatal("parts do not rejoin to the dumps")
	}
	calls, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "--dbname=mas") || !strings.Contains(lines[1], "--dbname=synapse") {
		t.Fatalf("pg_dump calls: %q", lines)
	}
	if !strings.Contains(lines[1], "--exclude-table-data=e2e_one_time_keys_json") || strings.Contains(lines[0], "exclude-table-data") {
		t.Errorf("one-time keys must be excluded from the Synapse dump only: %q", lines)
	}
	for _, l := range lines {
		args, env, _ := strings.Cut(l, " env=")
		if strings.Contains(args, "pw-never-in-argv") || env != "pw-never-in-argv" {
			t.Errorf("password must reach pg_dump by PGPASSWORD only: %q", l)
		}
		if !strings.Contains(args, "--username=kybackup") || !strings.Contains(args, "--host=postgres") || !strings.Contains(args, "--format=custom") {
			t.Errorf("pg_dump args %q", args)
		}
	}
	for _, path := range []string{"matrix/secrets/synapse_db_password", "matrix/synapse/signing.key", "matrix/mas/config.yaml",
		"matrix/element/config.json", "matrix/postgres/init.sql", "matrix/postgres/kybackup-role.sql", "data/media.key"} {
		if findFile(p.Files, path) == nil {
			t.Errorf("payload lacks %s", path)
		}
	}
	if findFile(p.Files, "matrix/secrets/.synapse_db_password.123") != nil {
		t.Error("collected a matrix-init temp file")
	}
	// The superuser password stays out: a fresh volume does not need it, matrix-init makes a new one.
	if findFile(p.Files, "matrix/secrets/postgres_password") != nil {
		t.Error("collected the Postgres superuser password")
	}
	if dumps, _ := p.VerificationRecipe["pg_dumps"].([]string); !slices.Equal(dumps, []string{"matrix/dumps/mas.dump", "matrix/dumps/synapse.dump"}) {
		t.Errorf("recipe pg_dumps = %v", p.VerificationRecipe["pg_dumps"])
	}
}

// The media key is created once, 0600, and the same key rides in every capsule.
func TestCollectSealsAWriteOnceMediaKey(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(10), dumpOf(10))
	a, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	b, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	ka, kb := findFile(a.Files, "data/media.key"), findFile(b.Files, "data/media.key")
	if ka == nil || kb == nil || len(ka.Data) != 65 || !bytes.Equal(ka.Data, kb.Data) {
		t.Fatalf("media key files %v then %v", ka != nil, kb != nil)
	}
	fi, err := os.Stat(filepath.Join(cfg.Database.DataDir, "media.key"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("data/media.key: %v %v", fi, err)
	}
}

func TestCollectSplitsAtExactBoundaries(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(2000), dumpOf(1000))
	p, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(members(p, "matrix/dumps/mas.")); n != 2 {
		t.Errorf("2000 bytes in 1000-byte parts gave %d parts, want 2 (no empty third)", n)
	}
	if n := len(members(p, "matrix/dumps/synapse.")); n != 1 {
		t.Errorf("1000 bytes gave %d parts, want 1", n)
	}
	cfg, _ = matrixInstance(t, dumpOf(10), nil)
	if _, err := backup.Collect(context.Background(), cfg, "1.0.0"); err == nil || !strings.Contains(err.Error(), "pg_dump synapse wrote nothing") {
		t.Errorf("empty dump: %v", err)
	}
}

func TestCollectRefusesPastTheLimit(t *testing.T) {
	backup.SetLimitsForTest(t, 1000, 1<<20)
	cfg, _ := matrixInstance(t, dumpOf(100), dumpOf(5000))
	full, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	limit := backup.Measure(full.Files) - 3000 // crosses inside the Synapse dump
	backup.SetLimitsForTest(t, 1000, limit)
	_, err = backup.Collect(context.Background(), cfg, "1.0.0")
	var se *backup.SizeError
	if !errors.As(err, &se) || !errors.Is(err, capsule.ErrCapsuleTooLarge) {
		t.Fatalf("err = %v, want a SizeError wrapping ErrCapsuleTooLarge", err)
	}
	if se.Bytes != backup.Measure(full.Files) {
		t.Errorf("measured %d, want the full size %d", se.Bytes, backup.Measure(full.Files))
	}
	if !strings.HasPrefix(se.Member, "matrix/dumps/synapse.dump.") || !strings.Contains(se.Error(), se.Member) || !strings.Contains(se.Error(), "MiB") {
		t.Errorf("error %q does not name the size and the member", se.Error())
	}
}

func TestCollectFailsWhenADumpFails(t *testing.T) {
	cfg, _ := matrixInstance(t, dumpOf(10), dumpOf(10))
	fakeTool(t, "pg_dump", `case "$*" in *--dbname=mas*) echo 'PGDMP partial'; echo 'pg_dump: error: connection to server lost' >&2; exit 1 ;; esac
exec /bin/cat /dev/null
`)
	_, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "pg_dump mas") || !strings.Contains(err.Error(), "connection to server lost") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectWithoutMatrixIsUnchanged(t *testing.T) {
	cfg, _ := payloadConfig(t)
	p, err := backup.Collect(context.Background(), cfg, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(members(p, "matrix/")) != 0 || findFile(p.Files, "data/media.key") != nil || p.VerificationRecipe["pg_dumps"] != nil {
		t.Fatalf("Matrix members without Matrix: %v", p.Files)
	}
	if _, err := os.Stat(filepath.Join(cfg.Database.DataDir, "media.key")); !os.IsNotExist(err) {
		t.Error("media.key created without Matrix")
	}
}

// Measure must count what capsule.Open counts: a tar header per member and 512-byte padding.
func TestMeasureMatchesTarFraming(t *testing.T) {
	var files []recoveryclient.File
	for _, n := range []int{0, 1, 511, 512, 513, 4096} {
		files = append(files, recoveryclient.File{Path: fmt.Sprint("f", n), Data: make([]byte, n)})
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.Path, Mode: 0o600, Size: int64(len(f.Data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := backup.Measure(files), int64(buf.Len()); got != want {
		t.Fatalf("Measure %d, tar stream %d", got, want)
	}
}

// The message rounds the size up and names the limit actually enforced, so it never reads
// "255 MiB, past the 256 MiB limit".
func TestSizeErrorNamesTheEnforcedLimit(t *testing.T) {
	err := &backup.SizeError{Bytes: 255<<20 + 1, Member: "matrix/dumps/synapse.dump.003"}
	if want := "backup: the capsule would expand to 256 MiB, past the 255 MiB limit, at matrix/dumps/synapse.dump.003"; err.Error() != want {
		t.Errorf("got %q, want %q", err.Error(), want)
	}
}

// pg_dump gets only what it needs: an inherited PG* variable must not point it elsewhere.
func TestPgDumpDropsInheritedPGEnv(t *testing.T) {
	cfg, _ := matrixInstance(t, dumpOf(10), dumpOf(10))
	t.Setenv("PGHOST", "attacker.example")
	t.Setenv("PGSERVICEFILE", "/tmp/evil")
	envLog := filepath.Join(t.TempDir(), "env")
	fakeTool(t, "pg_dump", fmt.Sprintf("export -p >> %s\nprintf PGDMP12345\n", envLog))
	_, _ = backup.Collect(context.Background(), cfg, "1.0.0")
	env, err := os.ReadFile(envLog)
	if err != nil || len(env) == 0 {
		t.Fatalf("fake pg_dump did not run: %v", err)
	}
	if strings.Contains(string(env), "PGHOST") || strings.Contains(string(env), "PGSERVICEFILE") || !strings.Contains(string(env), "PGPASSWORD") {
		t.Errorf("pg_dump environment:\n%s", env)
	}
}
