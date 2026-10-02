package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/backup/media"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// MatrixRestore loads a server capsule's Matrix half into a fresh stack: the dumps from the
// restored matrix/, then media from the local backup directory. Run checks everything first and
// changes nothing unless every check passes.
type MatrixRestore struct {
	MatrixDir string // restored matrix-init directory, holding dumps/ and secrets/
	MediaDir  string // Synapse's media store; must be empty
	BackupDir string // KY_BACKUP_DIR, holding media/
	DataDir   string // restored data directory, holding media.key
	DBHost    string // host or host:port
	SkipMedia bool
	// Relations counts a database's user relations; nil means CountRelations. Tests replace it.
	Relations func(ctx context.Context, host, db, owner, password string) (int, error)
}

// restoreDBs run in dump order, each as its owner (matrix-init's init.sql).
var restoreDBs = []struct{ db, secret string }{{"mas", "mas_db_password"}, {"synapse", "synapse_db_password"}}

func (r MatrixRestore) Run(ctx context.Context, out io.Writer) error {
	relations := r.Relations
	if relations == nil {
		relations = CountRelations
	}
	type plan struct {
		db, password string
		parts        []string
	}
	var plans []plan
	for _, d := range restoreDBs {
		pw, err := os.ReadFile(filepath.Join(r.MatrixDir, "secrets", d.secret))
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		password := strings.TrimSpace(string(pw))
		parts, err := restoreParts(filepath.Join(r.MatrixDir, "dumps"), d.db+".dump")
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		toc, err := listFiles(ctx, parts)
		if err == nil {
			err = readFiles(ctx, parts)
		}
		if err != nil {
			return fmt.Errorf("refused: the %s dump: %w", d.db, err)
		}
		// An owner cannot create most extensions; restoring as postgres is not an option.
		if strings.Contains(toc, " EXTENSION ") {
			return fmt.Errorf("refused: the %s dump creates an extension, which its owner cannot restore", d.db)
		}
		n, err := relations(ctx, r.DBHost, d.db, d.db, password)
		if err != nil {
			return fmt.Errorf("refused: database %s: %w", d.db, err)
		}
		if n != 0 {
			return fmt.Errorf("refused: database %s already holds %d relations; restore-matrix loads only into a newly created stack. If this stack is meant to be replaced, `docker compose down -v` deletes its Matrix database and media", d.db, n)
		}
		plans = append(plans, plan{d.db, password, parts})
	}
	var key []byte
	if !r.SkipMedia {
		if err := r.checkMediaStore(); err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		var err error
		if key, err = keyfile.Load(MediaKeyPath(r.DataDir), 32); err != nil {
			return fmt.Errorf("refused: media key: %w", err)
		}
		defer clear(key)
		n, err := media.Restore(ctx, filepath.Join(r.BackupDir, "media"), key, r.MediaDir, os.Getuid(), os.Getgid(), false)
		if errors.Is(err, media.ErrNoBackup) {
			return fmt.Errorf("refused: no media backup in %s; pass -skip-media to restore the databases only", r.BackupDir)
		}
		if err != nil {
			return fmt.Errorf("refused: %w", err)
		}
		fmt.Fprintf(out, "Checked %d media files\n", n)
	}
	for _, p := range plans {
		if err := pgRestore(ctx, r.DBHost, p.db, p.password, p.parts); err != nil {
			return fmt.Errorf("restoring %s failed; its transaction rolled back, but earlier databases are restored: start again from a fresh stack: %w", p.db, err)
		}
		fmt.Fprintf(out, "Restored database %s from %d parts\n", p.db, len(p.parts))
	}
	if r.SkipMedia {
		fmt.Fprintln(out, "Media skipped (-skip-media): uploads from before the backup are missing")
		return nil
	}
	n, err := media.Restore(ctx, filepath.Join(r.BackupDir, "media"), key, r.MediaDir, os.Getuid(), os.Getgid(), true)
	if err != nil {
		return fmt.Errorf("media restore failed after the databases were restored; start again from a fresh stack: %w", err)
	}
	fmt.Fprintf(out, "Restored %d media files\n", n)
	return nil
}

// checkMediaStore requires an empty store owned by this process, which is Synapse's user
// (KY_MATRIX_UID:GID), so restored media is Synapse's without any privilege to chown.
func (r MatrixRestore) checkMediaStore() error {
	fi, err := os.Stat(r.MediaDir)
	if err != nil {
		return err
	}
	if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
		return fmt.Errorf("media store %s belongs to %d:%d; run as that user (KY_MATRIX_UID:KY_MATRIX_GID)", r.MediaDir, st.Uid, st.Gid)
	}
	entries, err := os.ReadDir(r.MediaDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("media store %s is not empty; restore-matrix loads only into a newly created stack", r.MediaDir)
	}
	return nil
}

// restoreParts returns base's parts in dir from .000, refusing none, a gap or a stray part.
func restoreParts(dir, base string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, base+".*"))
	if err != nil {
		return nil, err
	}
	parts := dumpPartPaths(dir, base)
	if len(parts) == 0 || len(parts) != len(matches) {
		return nil, fmt.Errorf("the %s parts in %s are missing or not contiguous from .000", base, dir)
	}
	return parts, nil
}

func listFiles(ctx context.Context, parts []string) (string, error) {
	rd, closeAll, err := openFiles(parts)
	if err != nil {
		return "", err
	}
	defer closeAll()
	return restoreTOC(ctx, rd)
}

// readFiles reads the whole dump, decompressing every data block, without a database:
// --list reads only the TOC, so a truncated or missing last part would pass it.
func readFiles(ctx context.Context, parts []string) error {
	rd, closeAll, err := openFiles(parts)
	if err != nil {
		return err
	}
	defer closeAll()
	cmd := exec.CommandContext(ctx, "pg_restore", "--file=/dev/null")
	cmd.Stdin = rd
	cmd.Env = []string{}
	var stderr tail
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore: %w: %s", err, stderr.String())
	}
	return nil
}

// pgRestore restores one database as its owner in one transaction. The parts are joined into a
// private temporary file first: pg_restore can seek in a file, not in a pipe.
func pgRestore(ctx context.Context, host, db, password string, parts []string) error {
	tmp, err := os.CreateTemp("", "kymessages-"+db+"-*.dump")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	rd, closeAll, err := openFiles(parts)
	if err != nil {
		tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, rd)
	closeAll()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	h, port := splitHost(host)
	args := []string{"--host=" + h, "--port=" + port, "--username=" + db, "--dbname=" + db, "--no-password",
		"--no-owner", "--no-privileges", "--single-transaction", "--exit-on-error", tmp.Name()}
	cmd := exec.CommandContext(ctx, "pg_restore", args...)
	// As pg_dump: the password never reaches argv, and no inherited PG* variable applies.
	cmd.Env = []string{"PGPASSWORD=" + password, "PGCONNECT_TIMEOUT=10"}
	var stderr tail
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore %s: %w: %s", db, err, stderr.String())
	}
	return nil
}

// splitHost takes host or host:port; the port defaults to 5432.
func splitHost(host string) (string, string) {
	if h, p, err := net.SplitHostPort(host); err == nil {
		return h, p
	}
	return host, "5432"
}

// CountRelations counts relations outside the system schemas, connecting to host (host or
// host:port) as owner.
func CountRelations(ctx context.Context, host, db, owner, password string) (int, error) {
	h, port := splitHost(host)
	u := url.URL{Scheme: "postgres", User: url.UserPassword(owner, password), Host: net.JoinHostPort(h, port),
		Path: "/" + db, RawQuery: "connect_timeout=10"}
	conn, err := sql.Open("pgx", u.String())
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var n int
	err = conn.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp%'`).Scan(&n)
	return n, err
}
