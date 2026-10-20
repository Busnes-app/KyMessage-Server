package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// apply-setup against a server whose environment refuses setup prints why, although the
// socket answers every refusal with the same generic 409.
func TestApplySetupRefusalSaysWhy(t *testing.T) {
	if os.Getenv("KY_TEST_POSTGRES_DSN") != "" {
		t.Skip("uses a SQLite data directory")
	}
	data := t.TempDir()
	t.Setenv("KY_DATA_DIR", data)
	t.Setenv("KY_DB_DRIVER", "sqlite")
	t.Setenv("KY_APP_URL", "https://message.example.test")
	t.Setenv("KY_SCIM_ENABLED", "true")
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
	socket, err := config.InstallerSocket(data)
	if err != nil {
		t.Fatal(err)
	}
	stop, err := applysetup.StartLocal(ctx, socket, api.NewServer(cfg, st).ApplyInstaller, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	dir := t.TempDir()
	secret, directory, bundle := filepath.Join(dir, "client"), filepath.Join(dir, "directory"), filepath.Join(dir, "setup.json")
	for _, p := range []string{secret, directory} {
		if err := os.WriteFile(p, []byte("private-fixture-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	issuer := "https://identity.example.test"
	raw, _ := json.Marshal(applysetup.Common{Schema: applysetup.Schema, Service: "kymessage", SetupID: "271333e2-c221-4601-b249-563b3d828530",
		SSO:    &applysetup.SSO{IssuerURL: issuer, ClientID: "message", ClientSecretFile: secret, DirectorySecretFile: directory, RedirectURI: cfg.Server.AppURL + "/api/sso/kyidentity/callback"},
		Admins: []applysetup.Identity{{Issuer: issuer, Subject: "owner-sub", Username: "owner-admin"}}})
	if err := os.WriteFile(bundle, raw, 0600); err != nil {
		t.Fatal(err)
	}

	err = runInstaller([]string{"--file", bundle, "--check"})
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "KY_SCIM_ENABLED is on") {
		t.Fatalf("refusal without its reason: %v", err)
	}
	if strings.Contains(err.Error(), "private-fixture-secret") {
		t.Fatal("refusal names a secret")
	}
}
