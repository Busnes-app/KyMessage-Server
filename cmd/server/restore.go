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

// The library owns custodian-share handling, capsule verification and extraction.
// The product invalidates stale grants before reporting a usable restored server.
func restore(capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error {
	var manifest bytes.Buffer
	_, statErr := os.Lstat(targetDir)
	existed := statErr == nil
	if err := recoveryclient.Restore(capsulePath, targetDir, expectService, shares, &manifest); err != nil {
		return err
	}
	if err := prepareRestoredData(targetDir); err != nil {
		return errors.Join(fmt.Errorf("restore failed, restored files were removed: %w", err), removeExtracted(targetDir, existed))
	}
	if _, err := io.Copy(stdout, &manifest); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, "Restored sessions, challenges and pairings invalidated. Sign in freshly.")
	return err
}

// removeExtracted deletes what extraction wrote: the whole target if restore created it, else
// only its entries (the lib requires an empty target, so nothing else is in there).
func removeExtracted(target string, existed bool) error {
	if !existed {
		return os.RemoveAll(target)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(target, e.Name())))
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
