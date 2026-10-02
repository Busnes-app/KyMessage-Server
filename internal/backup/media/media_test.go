package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	key   = bytes.Repeat([]byte{3}, 32)
	jan   = time.Date(2026, 1, 15, 3, 0, 0, 0, time.UTC)
	feb   = time.Date(2026, 2, 15, 3, 0, 0, 0, time.UTC)
	files = map[string]string{
		"local_content/ab/cd/one":   "first upload",
		"local_thumbnails/ab/cd/t1": "thumb",
	}
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// store is a Synapse media store with the kept files, a cache dir and a symlink out.
func store(t *testing.T) string {
	src := t.TempDir()
	for rel, body := range files {
		write(t, src, rel, body)
	}
	write(t, src, "remote_content/ex/am/remote", "cache")
	if err := os.Symlink("/etc/hostname", filepath.Join(src, "local_content", "ab", "escape")); err != nil {
		t.Fatal(err)
	}
	return src
}

func restored(t *testing.T, dst string) map[string]string {
	t.Helper()
	got := map[string]string{}
	_ = filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(dst, p)
			got[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return got
}

func TestRunMirrorsEncryptedAndRestores(t *testing.T) {
	src, dir := store(t), t.TempDir()
	res, err := Run(context.Background(), src, dir, key, 3, jan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Copied != 2 || res.Archive != "full-2026-01.tar" {
		t.Fatalf("first run %+v", res)
	}
	for rel, body := range files {
		sealed, err := os.ReadFile(filepath.Join(dir, "mirror", rel))
		if err != nil || bytes.Contains(sealed, []byte(body)) || len(sealed) != len(body)+28 {
			t.Errorf("%s mirrored as %d bytes (%v); want ciphertext of %d+28", rel, len(sealed), err, len(body))
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "mirror", "remote_content")); !os.IsNotExist(err) {
		t.Error("mirrored remote media cache")
	}
	dst := t.TempDir()
	if n, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), false); err != nil || n == 0 || len(restored(t, dst)) != 0 {
		t.Fatalf("check-only restore wrote files or failed: %d %v", n, err)
	}
	if _, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
	if got := restored(t, dst); len(got) != len(files) || got["local_content/ab/cd/one"] != "first upload" {
		t.Fatalf("restored %v", got)
	}
}

// A ciphertext moved to another path does not open: its path is its associated data.
func TestSwappedFileIsRefused(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	one, _ := os.ReadFile(filepath.Join(dir, "mirror", "local_content/ab/cd/one"))
	if err := os.WriteFile(filepath.Join(dir, "mirror", "local_thumbnails/ab/cd/t1"), one, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "full-2026-01.tar")); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	_, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), false)
	if err == nil || !strings.Contains(err.Error(), "local_thumbnails/ab/cd/t1") {
		t.Fatalf("swapped file: %v", err)
	}
}

func TestIncrementalCopiesOnlyNewOrChanged(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), src, dir, key, 3, jan)
	if err != nil || res.Copied != 0 || res.Unchanged != 2 || res.Archive != "" {
		t.Fatalf("unchanged rerun %+v %v", res, err)
	}
	write(t, src, "local_content/ab/cd/one", "edited upload")
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(src, "local_content/ab/cd/one"), later, later); err != nil {
		t.Fatal(err)
	}
	write(t, src, "local_content/zz/yy/two", "second upload")
	if res, err = Run(context.Background(), src, dir, key, 3, jan); err != nil || res.Copied != 2 || res.Unchanged != 1 {
		t.Fatalf("after change %+v %v", res, err)
	}
}

func TestMonthlyArchiveAndPruning(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(src, "local_content/ab/cd/one")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	// Same month: the archive exists, so the mirror keeps the deleted media.
	if res, err := Run(context.Background(), src, dir, key, 3, jan); err != nil || res.Pruned != 0 {
		t.Fatalf("same month %+v %v", res, err)
	}
	res, err := Run(context.Background(), src, dir, key, 3, feb)
	if err != nil || res.Archive != "full-2026-02.tar" || res.Pruned != 1 {
		t.Fatalf("new month %+v %v", res, err)
	}
	if !slices.Contains(tarNames(t, filepath.Join(dir, "full-2026-02.tar")), "mirror/local_content/ab/cd/one") {
		t.Error("the new archive lacks media deleted since the last one")
	}
	if _, err := os.Stat(filepath.Join(dir, "mirror", "local_content/ab/cd/one")); !os.IsNotExist(err) {
		t.Error("deleted media still mirrored after the new archive")
	}
	for _, m := range []int{3, 4, 5} {
		if _, err := Run(context.Background(), src, dir, key, 3, time.Date(2026, time.Month(m), 1, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := archives(dir)
	if want := []string{"full-2026-05.tar", "full-2026-04.tar", "full-2026-03.tar"}; !slices.Equal(got, want) {
		t.Fatalf("archives %v, want %v", got, want)
	}
}

func tarNames(t *testing.T, path string) []string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var names []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

// Restore applies the newest archive, then the mirror, which wins.
func TestRestorePrefersTheMirrorOverTheArchive(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	write(t, src, "local_content/ab/cd/one", "newer upload")
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(src, "local_content/ab/cd/one"), later, later)
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if _, err := Restore(context.Background(), dir, key, dst, os.Getuid(), os.Getgid(), true); err != nil {
		t.Fatal(err)
	}
	if got := restored(t, dst)["local_content/ab/cd/one"]; got != "newer upload" {
		t.Fatalf("restored %q", got)
	}
}

func TestRunSkipsAFileDeletedMidRun(t *testing.T) {
	src, dir := store(t), t.TempDir()
	afterScan = func() { _ = os.Remove(filepath.Join(src, "local_content/ab/cd/one")) }
	t.Cleanup(func() { afterScan = func() {} })
	res, err := Run(context.Background(), src, dir, key, 3, jan)
	if err != nil || res.Copied != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRunRefusesAMirrorUnderAnotherKey(t *testing.T) {
	src, dir := store(t), t.TempDir()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "mirror", "local_content/ab/cd/one"))
	other := bytes.Repeat([]byte{4}, 32)
	if _, err := Run(context.Background(), src, dir, other, 3, jan); err == nil || !strings.Contains(err.Error(), "does not open under the media key") {
		t.Fatalf("other key: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "mirror", "local_content/ab/cd/one")); !bytes.Equal(before, after) {
		t.Error("mirror rewritten under another key")
	}
	if _, err := Restore(context.Background(), dir, other, t.TempDir(), os.Getuid(), os.Getgid(), false); err == nil {
		t.Error("restore with another key accepted")
	}
}

func TestRunSweepsCrashLeftoversAndIsSingleFlight(t *testing.T) {
	src, dir := store(t), t.TempDir()
	write(t, dir, "mirror/local_content/ab/cd/.one.tmp-123", "half")
	if _, err := Run(context.Background(), src, dir, key, 3, jan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mirror/local_content/ab/cd/.one.tmp-123")); !os.IsNotExist(err) {
		t.Error("crash leftover not swept")
	}
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Run(context.Background(), src, dir, key, 3, jan); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent run: %v", err)
	}
}

func TestRestoreRefusesAnIndexOutsideTheStore(t *testing.T) {
	dir := t.TempDir()
	a, err := newAEAD(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "mirror"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../escape", "local_content/../../x", "/etc/passwd", "remote_content/x"} {
		if err := writeIndex(a, filepath.Join(dir, "mirror", "index"), Index{bad: {}}); err != nil {
			t.Fatal(err)
		}
		if _, err := Restore(context.Background(), dir, key, t.TempDir(), os.Getuid(), os.Getgid(), false); err == nil {
			t.Errorf("index entry %q accepted", bad)
		}
	}
	if _, err := Restore(context.Background(), t.TempDir(), key, t.TempDir(), 0, 0, false); !errors.Is(err, ErrNoBackup) {
		t.Errorf("empty backup dir: %v", err)
	}
}
