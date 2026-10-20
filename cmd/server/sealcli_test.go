package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// managedConsole commits an installer setup into a fresh SQLite data directory, as apply-setup
// does, and leaves the CLI to find it there.
func managedConsole(t *testing.T) {
	t.Helper()
	if os.Getenv("KY_TEST_POSTGRES_DSN") != "" {
		t.Skip("native sealed snapshots require SQLite")
	}
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_DB_DRIVER", "sqlite")
	t.Setenv("KY_PORT", "8080") // Compose sets it; the drill checks it
	t.Setenv("KY_APP_URL", "https://message.example.test")
	dir := t.TempDir()
	client, directory := filepath.Join(dir, "client"), filepath.Join(dir, "directory")
	for _, p := range []string{client, directory} {
		if err := os.WriteFile(p, []byte("disposable-private-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b := applysetup.Common{Schema: applysetup.Schema, Service: "kymessage", SetupID: "271333e2-c221-4601-b249-563b3d828530", SSO: &applysetup.SSO{IssuerURL: "https://id.example.test", ClientID: "client", ClientSecretFile: client, DirectorySecretFile: directory, RedirectURI: cfg.Server.AppURL + "/api/sso/kyidentity/callback"}, Admins: []applysetup.Identity{{Issuer: "https://id.example.test", Subject: "owner", Username: "owner-admin"}}}
	if _, err := st.ApplyInstaller(ctx, b, false); err != nil {
		t.Fatal(err)
	}
}

// deposit, export-capsule and backup-drill seal what the server seals: on an installer-managed
// console that includes the installer credentials, and the drill over them passes.
func TestCLISealIncludesInstallerCredentials(t *testing.T) {
	managedConsole(t)
	ctx := context.Background()
	cfg, st, err := openForSeal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	found := map[string]bool{}
	for _, f := range collectFiles(ctx, cfg).Files {
		found[f.Path] = true
	}
	for _, name := range []string{"installer/client.secret", "installer/directory.secret", "installer/credential-paths.json"} {
		if !found[name] {
			t.Errorf("CLI seal misses %s", name)
		}
	}
	if err := backupDrill(ctx, cfg, io.Discard); err != nil {
		t.Errorf("drill on a managed console: %v", err)
	}
}

// A failed drill is an error (non-zero exit), not a printed FAILED with exit 0.
func TestBackupDrillFailureIsAnError(t *testing.T) {
	managedConsole(t)
	ctx := context.Background()
	cfg, err := config.LoadFromEnv() // without the stored setup the managed checks must fail
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InstallerSSO != nil {
		t.Fatal("fixture loaded the installer setup")
	}
	if err := backupDrill(ctx, cfg, io.Discard); err == nil {
		t.Fatal("failed drill returned no error")
	}
}
