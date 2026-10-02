package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// mediaKeyPath encrypts the local media mirror (internal/backup/media). It rides in the
// capsule, so only k custodians together can open the mirror.
const mediaKeyPath = "data/media.key"

// MediaKeyPath is the media key file under the data directory.
func MediaKeyPath(dataDir string) string { return filepath.Join(dataDir, "media.key") }

// DumpTimeout bounds both pg_dump runs together. cmd/server's backupWaitTimeout counts it.
const DumpTimeout = 3 * time.Minute

// partBytes is one dump member: the capsule's per-file limit.
var partBytes = capsule.MaxFileBytes

// expandLimit is the most a payload may expand to. capsule.Open counts tar framing (a 512-byte
// header per member, content padded to 512, the file list and a 1 KiB trailer) against
// MaxExpandedBytes; capsule.Seal counts content only, so ky-primitives v0.8.0 seals payloads
// it then refuses to open. memberBytes counts the framing; 1 MiB is held back for the list and
// trailer.
var expandLimit = capsule.MaxExpandedBytes - 1<<20

// matrixDirs are the matrix-init subdirectories a rebuilt stack needs: secrets, the signing
// key and every rendered config. dumps/ exists only in a restored tree and is never collected.
var matrixDirs = []string{"secrets", "synapse", "mas", "element", "postgres"}

// superuserSecret is never collected: a fresh Postgres volume needs no old superuser password,
// and matrix-init regenerates it. Compose hides it from the app too.
const superuserSecret = "secrets/postgres_password"

// dumps run MAS first: Postgres snapshots do not span databases, and MAS re-provisions a user
// created in between at sign-in. Synapse's backup guide: one-time keys must not be restored.
var dumps = []struct {
	db, base string
	extra    []string
}{
	{"mas", "matrix/dumps/mas.dump", nil},
	{"synapse", "matrix/dumps/synapse.dump", []string{"--exclude-table-data=e2e_one_time_keys_json"}},
}

func dumpBases() []string {
	out := make([]string, 0, len(dumps))
	for _, d := range dumps {
		out = append(out, d.base)
	}
	return out
}

// SizeError is a payload past the expanded limit: Bytes is the full measured size, Member the
// first member past the limit.
type SizeError struct {
	Bytes  int64
	Member string
}

// Error rounds the size up and the limit down, so the size always reads as past the limit.
func (e *SizeError) Error() string {
	return fmt.Sprintf("backup: the capsule would expand to %d MiB, past the %d MiB limit, at %s",
		(e.Bytes+1<<20-1)>>20, expandLimit>>20, e.Member)
}

func (e *SizeError) Unwrap() error { return capsule.ErrCapsuleTooLarge }

func memberBytes(n int64) int64 { return 512 + (n+511)/512*512 }

// Measure is the expanded size of files as capsule.Open counts it, file list and trailer aside.
func Measure(files []recoveryclient.File) int64 {
	var n int64
	for _, f := range files {
		n += memberBytes(int64(len(f.Data)))
	}
	return n
}

// budget counts members as they are collected and remembers the first one past the limit.
type budget struct {
	used int64
	over string
}

func (b *budget) add(path string, n int64) {
	b.used += memberBytes(n)
	if b.used > expandLimit && b.over == "" {
		b.over = path
	}
}

func (b *budget) err() error {
	if b.over == "" {
		return nil
	}
	return &SizeError{Bytes: b.used, Member: b.over}
}

// collectMatrix returns matrix-init's files, then the dumps. Past the limit the dumps keep
// draining, so the error carries the full size, but no more data is kept.
func collectMatrix(ctx context.Context, m config.MatrixConfig, b *budget) ([]recoveryclient.File, error) {
	var files []recoveryclient.File
	for _, sub := range matrixDirs {
		err := filepath.WalkDir(filepath.Join(m.Dir, sub), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Regular files only; matrix-init's in-flight temp files start with ".".
			if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel, err := filepath.Rel(m.Dir, path)
			if err != nil || filepath.ToSlash(rel) == superuserSecret {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			name := "matrix/" + filepath.ToSlash(rel)
			b.add(name, int64(len(data)))
			files = append(files, recoveryclient.File{Path: name, Data: data, Mode: 0600})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("backup: Matrix config: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, DumpTimeout)
	defer cancel()
	for _, d := range dumps {
		parts, err := dumpParts(ctx, m, d.db, d.base, d.extra, b)
		if err != nil {
			return nil, err
		}
		files = append(files, parts...)
	}
	return files, nil
}

// dumpParts runs pg_dump as kybackup and cuts its output into <base>.000, .001, ...
func dumpParts(ctx context.Context, m config.MatrixConfig, db, base string, extra []string, b *budget) ([]recoveryclient.File, error) {
	args := append([]string{"--format=custom", "--host=" + m.DBHost, "--username=kybackup", "--dbname=" + db, "--no-password"}, extra...)
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	// Only what pg_dump needs: the password never reaches argv, and no inherited PG* variable
	// can point the dump elsewhere.
	cmd.Env = []string{"PGPASSWORD=" + m.BackupDBPassword, "PGCONNECT_TIMEOUT=10"}
	var stderr tail
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("backup: pg_dump %s: %w", db, err)
	}
	var files []recoveryclient.File
	var readErr error
	for i := 0; ; i++ {
		part, err := io.ReadAll(io.LimitReader(out, partBytes))
		if err != nil {
			readErr = err
			break
		}
		if len(part) == 0 {
			break
		}
		name := fmt.Sprintf("%s.%03d", base, i)
		b.add(name, int64(len(part)))
		if b.over == "" {
			files = append(files, recoveryclient.File{Path: name, Data: part, Mode: 0600})
		}
		if int64(len(part)) < partBytes {
			break
		}
	}
	if err := errors.Join(readErr, cmd.Wait()); err != nil {
		return nil, fmt.Errorf("backup: pg_dump %s: %w: %s", db, err, stderr.String())
	}
	if len(files) == 0 && b.over == "" {
		return nil, fmt.Errorf("backup: pg_dump %s wrote nothing", db)
	}
	return files, nil
}

// tail keeps the last 2 KiB of a tool's stderr. String drops CONTEXT and DETAIL lines, which
// can quote row data, and a line cut by the limit.
type tail struct {
	b   []byte
	cut bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b, t.cut = t.b[len(t.b)-2048:], true
	}
	return len(p), nil
}

func (t *tail) String() string {
	s := string(t.b)
	if t.cut {
		_, s, _ = strings.Cut(s, "\n")
	}
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		l := strings.ToLower(line)
		if !strings.Contains(l, "context:") && !strings.Contains(l, "detail:") {
			keep = append(keep, line)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// openFiles reads paths in order as one stream; close releases them all.
func openFiles(paths []string) (io.Reader, func(), error) {
	var readers []io.Reader
	var opened []*os.File
	closeAll := func() {
		for _, f := range opened {
			f.Close()
		}
	}
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		opened = append(opened, f)
		readers = append(readers, f)
	}
	return io.MultiReader(readers...), closeAll, nil
}

// restoreTOC is pg_restore --list over a custom-format dump read from r.
func restoreTOC(ctx context.Context, r io.Reader) (string, error) {
	cmd := exec.CommandContext(ctx, "pg_restore", "--list")
	cmd.Stdin = r
	cmd.Env = []string{}
	var stderr tail
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pg_restore --list: %w: %s", err, stderr.String())
	}
	return string(out), nil
}

// dumpPartPaths lists base.000, base.001, ... under dir, stopping at the first absent or
// non-regular part. Callers decide whether an empty result is an error.
func dumpPartPaths(dir, base string) []string {
	var paths []string
	for i := 0; ; i++ {
		full, _ := drillPath(dir, fmt.Sprintf("%s.%03d", base, i))
		if fi, err := os.Lstat(full); err != nil || !fi.Mode().IsRegular() {
			return paths
		}
		paths = append(paths, full)
	}
}
