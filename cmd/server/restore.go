package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// afterExtract is a test seam run between extraction and the target identity check.
var afterExtract = func(target string) {}

// The library owns custodian-share handling, capsule verification and extraction.
// The product invalidates stale grants before reporting a usable restored server.
func restore(capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error {
	if err := checkRestoreTarget(targetDir); err != nil {
		return err
	}
	created := false
	if err := os.Mkdir(targetDir, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	root, err := os.OpenRoot(targetDir)
	if err != nil {
		if created {
			err = errors.Join(err, os.Remove(targetDir))
		}
		return err
	}
	defer root.Close()

	var manifest bytes.Buffer
	if err := recoveryclient.Restore(capsulePath, targetDir, expectService, shares, &manifest); err != nil {
		return errors.Join(err, removeExtracted(root, targetDir, created))
	}
	afterExtract(targetDir)
	if err := sameDir(root, targetDir); err != nil {
		return errors.Join(err, removeExtracted(root, targetDir, created))
	}
	if err := prepareRestoredData(targetDir); err != nil {
		return errors.Join(fmt.Errorf("restore failed, restored files were removed: %w", err), removeExtracted(root, targetDir, created))
	}
	if _, err := io.Copy(stdout, &manifest); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "Restored sessions, challenges and pairings invalidated. Sign in freshly.")
	return err
}

// checkRestoreTarget refuses targets another user could swap: a symlink, or a directory
// whose parent others can write without the sticky bit.
func checkRestoreTarget(target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore target %s is a symlink; pass the real directory", abs)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(abs)
	info, err := os.Stat(parent)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("restore target parent %s is writable by group or others without the sticky bit, so another user could replace the target; restore under a directory only you can write", parent)
	}
	return nil
}

// sameDir confirms target still names the directory root was opened on.
func sameDir(root *os.Root, target string) error {
	a, err := root.Stat(".")
	if err != nil {
		return err
	}
	b, err := os.Lstat(target)
	if err != nil || !b.IsDir() || !os.SameFile(a, b) {
		return errors.New("restore target was replaced during restore")
	}
	return nil
}

// removeExtracted empties the opened target through its handle, never by path, and removes
// the directory itself if restore created it. os.Remove deletes only an empty directory and
// unlinks a symlink without following it.
func removeExtracted(root *os.Root, target string, created bool) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	errs := []error{err, dir.Close()}
	for _, e := range entries {
		errs = append(errs, root.RemoveAll(e.Name()))
	}
	if created {
		errs = append(errs, os.Remove(target))
	}
	return errors.Join(errs...)
}

func prepareRestoredData(target string) error {
	path, err := filepath.Abs(filepath.Join(target, "data", "ky_server.db"))
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("missing regular SQLite snapshot")
	}
	key, err := os.ReadFile(filepath.Join(target, "data", "encryption.key"))
	if err != nil {
		return err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(key)))
	clear(key)
	defer clear(decoded)
	if err != nil || len(decoded) != 32 {
		return errors.New("invalid restored encryption key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}).String()
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: dsn})
	if err != nil {
		return err
	}
	return errors.Join(st.InvalidateRestoredGrants(ctx), st.Close())
}
