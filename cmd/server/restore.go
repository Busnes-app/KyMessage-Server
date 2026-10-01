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
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// The library owns custodian-share handling, capsule verification and extraction.
// The product invalidates stale grants before reporting a usable restored server.
func restore(capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error {
	var manifest bytes.Buffer
	if err := recoveryclient.Restore(capsulePath, targetDir, expectService, shares, &manifest); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(targetDir, backup.MessagesAccounts)); err == nil {
		// Decrypted ciphertext and metadata must not stay behind a refusal.
		return errors.Join(errors.New("this is a messages capsule; restore the people capsule first, then use restore-messages"),
			os.RemoveAll(filepath.Join(targetDir, backup.MessagesDir)))
	}
	if err := prepareRestoredData(targetDir); err != nil {
		return fmt.Errorf("restored files are NOT ready to serve: %w; keep the target offline", err)
	}
	if _, err := io.Copy(stdout, &manifest); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, "Restored sessions, challenges and pairings invalidated. A people capsule holds no threads or messaging devices; any from an older capsule were revoked and their rooms retired. Sign in freshly. Browser keys/history were not restored."); err != nil {
		return err
	}
	dbPath, err := filepath.Abs(filepath.Join(targetDir, "data", "ky_server.db"))
	if err != nil {
		return err
	}
	switch err := backup.CheckMessagesTarget(context.Background(), dbPath); {
	case errors.Is(err, backup.ErrMessagingDataPresent):
		_, err = fmt.Fprintln(stdout, "This capsule predates the people/messages split and kept its messaging rows, so restore-messages cannot run on this target.")
		return err
	case err != nil:
		return err
	}
	_, err = fmt.Fprintf(stdout, "If a messages capsule exists, run `restore-messages -capsule <file> -into %s` before serving.\n", targetDir)
	return err
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

// restoreMessages imports a messages capsule into a people-restored target, offline. The
// opened capsule holds ciphertext and metadata, so it lives in a private temp directory
// that is removed whatever happens.
func restoreMessages(ctx context.Context, capsulePath, targetDir, expectService string, shares []string, stdout io.Writer) error {
	dbPath, err := filepath.Abs(filepath.Join(targetDir, "data", "ky_server.db"))
	if err != nil {
		return err
	}
	if info, err := os.Lstat(dbPath); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is missing; run the people restore first", dbPath)
	}
	if err := backup.CheckMessagesTarget(ctx, dbPath); err != nil {
		return err
	}
	opened, err := os.MkdirTemp(targetDir, "messages-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(opened)
	var manifest bytes.Buffer
	if err := recoveryclient.Restore(capsulePath, opened, expectService, shares, &manifest); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(opened, backup.MessagesAccounts)); err != nil {
		return errors.New("not a messages capsule: it has no " + backup.MessagesAccounts)
	}
	if _, err := os.Lstat(filepath.Join(opened, "data", "ky_server.db")); err == nil {
		return errors.New("not a messages capsule: it holds a people database; use restore")
	}
	// Recovery does not take a context; stop here if a signal arrived while it ran.
	if err := ctx.Err(); err != nil {
		return err
	}
	// Migrate the people snapshot to this build's schema before importing into it.
	st, err := store.Open(ctx, config.DatabaseConfig{Driver: "sqlite", DSN: (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=rw"}).String()})
	if err != nil {
		return err
	}
	if err := st.Close(); err != nil {
		return err
	}
	counts, err := backup.ImportMessages(ctx, dbPath, opened)
	if err != nil {
		return fmt.Errorf("messages NOT imported: %w", err)
	}
	if _, err := io.Copy(stdout, &manifest); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Imported rooms=%d dropped_rooms=%d members=%d dropped_members=%d devices=%d dropped_devices=%d events=%d\n"+
		"Restored devices are suspended. Before users return, an admin reviews GET /api/admin/messaging/devices?status=suspended and revokes unknown ones with POST /api/admin/messaging/devices/{device}/revoke. "+
		"A capsule older than the incident undoes device revocations and identity resets made after it was created, so confirm each device before it resumes. "+
		"An owner resumes a device after a fresh sign-in: POST /api/messaging/devices/{device}/resume, then POST /api/messaging/devices/{device}/resume/verify with the device key's signature.\n",
		counts.Rooms, counts.DroppedRooms, counts.Members, counts.DroppedMembers, counts.Devices, counts.DroppedDevices, counts.Events)
	return err
}
