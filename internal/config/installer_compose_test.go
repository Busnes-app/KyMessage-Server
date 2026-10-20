package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// composeEnv resolves the app service's environment in docker-compose.yml as Compose would with
// no .env: `${X:-d}` and `${X-d}` give d, a required `${X:?...}` gets a placeholder.
func composeEnv(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	entry := regexp.MustCompile(`(?m)^\s+- (KY_[A-Z0-9_]+)=(.*)$`)
	ref := regexp.MustCompile(`^\$\{[A-Z0-9_]+(:?[-?])(.*)\}$`)
	env := map[string]string{}
	for _, m := range entry.FindAllStringSubmatch(string(raw), -1) {
		v := m[2]
		if r := ref.FindStringSubmatch(v); r != nil {
			v = r[2]
			if r[1] == ":?" {
				v = "required-by-compose"
			}
		}
		env[m[1]] = v
	}
	if len(env) == 0 {
		t.Fatal("no app environment found in docker-compose.yml")
	}
	return env
}

// The stock Compose file is an installer target: nothing it sets on its own may read as an
// operator choice that apply-setup refuses (SCIM, retention, interval).
func TestStockComposeEnvironmentAcceptsInstallerSetup(t *testing.T) {
	env := composeEnv(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_APP_URL", "https://message.example.test") // setup needs the operator's https URL
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	secret, directory := filepath.Join(dir, "client"), filepath.Join(dir, "directory")
	for _, p := range []string{secret, directory} {
		if err := os.WriteFile(p, []byte("private-fixture-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	issuer := "https://identity.example.test"
	b := applysetup.Common{Schema: applysetup.Schema, Service: "kymessage", SetupID: "271333e2-c221-4601-b249-563b3d828530",
		SSO:    &applysetup.SSO{IssuerURL: issuer, ClientID: "message", ClientSecretFile: secret, DirectorySecretFile: directory, RedirectURI: cfg.Server.AppURL + "/api/sso/kyidentity/callback"},
		Admins: []applysetup.Identity{{Issuer: issuer, Subject: "owner-sub", Username: "owner-admin"}},
		Backup: &applysetup.Backup{Directory: env["KY_BACKUP_DIR"], Keep: 14, Interval: "48h"}}
	if err := config.ApplyInstallerConfiguration(cfg, b); err != nil {
		t.Fatalf("stock compose environment refuses installer setup: %v", err)
	}
	if cfg.SCIM.Enabled || cfg.Backup.Keep != 14 {
		t.Fatalf("setup not applied: scim=%v keep=%d", cfg.SCIM.Enabled, cfg.Backup.Keep)
	}
}
