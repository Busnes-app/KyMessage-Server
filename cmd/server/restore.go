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
	"syscall"
	"time"

	"github.com/Busnes-app/ky-primitives/recoveryclient"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Test seams: beforeCreate runs between validating the target and creating it, afterExtract
// between extraction and the target identity check.
var beforeCreate, afterExtract = func(target string) {}, func(target string) {}

// The library owns custodian-share handling, capsule verification and extraction.
// The product invalidates stale grants before reporting a usable restored server.
func restore(capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error {
	target, existed, err := resolveRestoreTarget(targetDir)
	if err != nil {
		return err
	}
	beforeCreate(target)
	created := !existed
	if created {
		if err := os.Mkdir(target, 0o700); errors.Is(err, os.ErrExist) {
			return fmt.Errorf("restore target %s appeared during restore; retry", target)
		} else if err != nil {
			return err
		}
	}
	// removeCreated undoes only our Mkdir; os.Remove deletes an empty directory alone.
	removeCreated := func(err error) error {
		if created {
			return errors.Join(err, os.Remove(target))
		}
		return err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return removeCreated(err)
	}
	defer root.Close()
	if err := checkTarget(root, target); err != nil {
		return err
	}
	if err := requireEmpty(root, target); err != nil {
		return err
	}

	// The library rolls back its own failures, so nothing of ours needs cleaning here.
	var manifest bytes.Buffer
	if err := recoveryclient.Restore(capsulePath, target, expectService, shares, &manifest); err != nil {
		return removeCreated(err)
	}
	afterExtract(target)
	if err := checkTarget(root, target); err != nil {
		return errors.Join(err, removeExtracted(root, target, created))
	}
	if err := prepareRestoredData(target); err != nil {
		return errors.Join(fmt.Errorf("restore failed, restored files were removed: %w", err), removeExtracted(root, target, created))
	}
	if _, err := io.Copy(stdout, &manifest); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "Restored sessions, challenges and pairings invalidated. Sign in freshly.")
	return err
}

// resolveRestoreTarget resolves symlinked parents and returns the real target path, and
// whether it exists, once the target and every ancestor up to / pass pathRefusal. With none of them writable or owned
// by another user, nobody else can swap the path between checkTarget and the path-based
// extraction and preparation.
func resolveRestoreTarget(targetDir string) (string, bool, error) {
	abs, err := filepath.Abs(targetDir)
	if err != nil {
		return "", false, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", false, err
	}
	target := filepath.Join(parent, filepath.Base(abs))
	info, err := os.Lstat(target)
	existed := err == nil
	if existed {
		if err := pathRefusal(target, info.Sys().(*syscall.Stat_t).Uid, info.Mode(), true); err != nil {
			return "", false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	for dir := parent; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return "", false, err
		}
		if err := pathRefusal(dir, info.Sys().(*syscall.Stat_t).Uid, info.Mode(), false); err != nil {
			return "", false, err
		}
		if dir == filepath.Dir(dir) {
			return target, existed, nil
		}
	}
}

// pathRefusal decides one path element. The target must be our own directory; an ancestor
// must be ours or root's. Neither may be group- or world-writable, except a root-owned
// sticky ancestor such as /tmp, where others cannot rename or delete what we own.
func pathRefusal(path string, uid uint32, mode os.FileMode, target bool) error {
	what := "restore target"
	if !target {
		what = "restore target ancestor"
	}
	me := uint32(os.Getuid())
	switch {
	case mode&os.ModeSymlink != 0:
		return fmt.Errorf("%s %s is a symlink; pass the real directory", what, path)
	case !mode.IsDir():
		return fmt.Errorf("%s %s is not a directory", what, path)
	case uid != me && (target || uid != 0):
		return fmt.Errorf("%s %s is owned by uid %d, not you or root, so its owner could replace the target; restore into a directory you own under directories you or root own", what, path, uid)
	case mode.Perm()&0o022 != 0 && (target || uid != 0 || mode&os.ModeSticky == 0):
		return fmt.Errorf("%s %s is writable by group or others, so another user could replace the target; restore under directories only you or root can write (a root-owned sticky dir like /tmp is fine)", what, path)
	}
	return nil
}

// requireEmpty refuses a target with existing contents before anything is extracted.
func requireEmpty(root *os.Root, target string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	if _, err := dir.ReadDir(1); err == nil {
		return fmt.Errorf("restore target %s is not empty; restore into a new or empty directory", target)
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// checkTarget confirms target still names the directory root holds and that this directory
// passes the target rule.
func checkTarget(root *os.Root, target string) error {
	a, err := root.Stat(".")
	if err != nil {
		return err
	}
	b, err := os.Lstat(target)
	if err != nil || !b.IsDir() || !os.SameFile(a, b) {
		return errors.New("restore target was replaced during restore")
	}
	return pathRefusal(target, a.Sys().(*syscall.Stat_t).Uid, a.Mode(), true)
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
