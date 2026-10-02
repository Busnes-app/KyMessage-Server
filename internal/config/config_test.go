package config_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
)

func TestConfigLoadDefaults(t *testing.T) {
	t.Setenv("KY_APP_NAME", "")
	t.Setenv("KY_DATA_DIR", t.TempDir())
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Server.AppName != "KyMessages" {
		t.Errorf("expected KyMessages service name, got %q", cfg.Server.AppName)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Database.Driver != "sqlite" {
		t.Errorf("expected default driver sqlite, got %s", cfg.Database.Driver)
	}
	if cfg.Captcha.Provider != "pow" {
		t.Errorf("expected default captcha provider pow, got %s", cfg.Captcha.Provider)
	}
	if cfg.Backup.Dir != "" {
		t.Errorf("expected empty default backup dir (sealed local copies off), got %q", cfg.Backup.Dir)
	}
}

func TestConfigLoadFromEnvOverrides(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_PORT", "9090")
	t.Setenv("KY_DB_DRIVER", "postgres")
	t.Setenv("KY_DB_DSN", "postgres://user:pass@localhost:5432/testdb")
	t.Setenv("KY_APP_NAME", "CustomBusnesApp")
	t.Setenv("KY_CAPTCHA_PROVIDER", "none")

	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.Port)
	}
	if cfg.Database.Driver != "postgres" {
		t.Errorf("expected driver postgres, got %s", cfg.Database.Driver)
	}
	if cfg.Database.DSN != "postgres://user:pass@localhost:5432/testdb" {
		t.Errorf("expected custom DSN, got %s", cfg.Database.DSN)
	}
	if cfg.Server.AppName != "CustomBusnesApp" {
		t.Errorf("expected custom app name, got %s", cfg.Server.AppName)
	}
	if cfg.Captcha.Provider != "none" {
		t.Errorf("expected captcha provider none, got %s", cfg.Captcha.Provider)
	}
}

// Login verifies only proof-of-work, so any other provider name would silently turn the
// check off. Startup must refuse it instead.
func TestUnverifiedCaptchaProviderFailsStartup(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	for _, provider := range []string{"turnstile", "friendly", "POW"} {
		t.Setenv("KY_CAPTCHA_PROVIDER", provider)
		if _, err := config.LoadFromEnv(); err == nil {
			t.Errorf("provider %q loaded; want a startup error", provider)
		}
	}
}

func TestEncryptionKeyPersistsAcrossLoads(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_ENCRYPTION_KEY", "")
	a, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Security.EncryptionKey) != 32 || !bytes.Equal(a.Security.EncryptionKey, b.Security.EncryptionKey) {
		t.Fatal("encryption key was not persisted between loads")
	}
}

func TestEncryptionKeyFromEnvMustBe32Bytes(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_ENCRYPTION_KEY", "deadbeef")
	if _, err := config.LoadFromEnv(); err == nil {
		t.Fatal("8-byte key accepted")
	}
}

func TestDepositIntervalFromEnv(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 24 * time.Hour, true},
		{"90m", 90 * time.Minute, true},
		{"15m", 15 * time.Minute, true},
		{"0", 0, true},
		{"1s", 0, false},
		{"14m", 0, false},
		{"-1h", 0, false},
		{"daily", 0, false},
	} {
		t.Setenv("KY_BACKUP_DEPOSIT_INTERVAL", tc.in)
		cfg, err := config.LoadFromEnv()
		if (err == nil) != tc.ok {
			t.Errorf("%q: err=%v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && cfg.Backup.DepositInterval != tc.want {
			t.Errorf("%q: got %v, want %v", tc.in, cfg.Backup.DepositInterval, tc.want)
		}
	}
}

func TestBackupConfigFromEnv(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_BACKUP_DIR", "/tmp/x")
	t.Setenv("KY_BACKUP_KEEP", "3")
	t.Setenv("KY_BACKUP_ALLOW_PRIVATE_RECOVERY", "true")
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.Dir != "/tmp/x" || cfg.Backup.Keep != 3 || !cfg.Backup.AllowPrivateRecovery {
		t.Fatalf("%+v", cfg.Backup)
	}
}

func TestBackupKeepBelowOneIsRefused(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_BACKUP_KEEP", "0")
	if _, err := config.LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_BACKUP_KEEP") {
		t.Fatalf("want KY_BACKUP_KEEP error, got %v", err)
	}
}

// Operators often set an https app URL behind a TLS proxy and never touch KY_ENV. Cookies must
// still be Secure there, and production must not silently run insecure over plain HTTP.
func TestCookieSecureFollowsTheAppURL(t *testing.T) {
	for _, tc := range []struct {
		env, url, secure string
		want, fails      bool
	}{
		{"", "https://chat.example.com", "", true, false},
		{"", "http://localhost:8080", "", false, false},
		{"production", "https://chat.example.com", "", true, false},
		{"production", "http://chat.lan", "", false, true},
		{"production", "http://chat.lan", "false", false, false},
		{"", "https://chat.example.com", "false", false, false},
	} {
		t.Setenv("KY_DATA_DIR", t.TempDir())
		t.Setenv("KY_SESSION_SECRET", "a-durable-secret-for-this-test")
		t.Setenv("KY_ENV", tc.env)
		t.Setenv("KY_APP_URL", tc.url)
		t.Setenv("KY_COOKIE_SECURE", tc.secure)
		cfg, err := config.LoadFromEnv()
		if tc.fails {
			if err == nil {
				t.Errorf("%+v: loaded, want a startup error", tc)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		if cfg.Security.CookieSecure != tc.want {
			t.Errorf("%+v: CookieSecure=%v, want %v", tc, cfg.Security.CookieSecure, tc.want)
		}
	}
}

// A secret left under its pre-rename name must stop startup, not leave sign-in unconfigured.
func TestLegacyKySignOnEnvironmentFailsStartup(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	for _, suffix := range []string{"ISSUER", "CLIENT_ID", "SECRET", "HMAC_SECRET"} {
		t.Run(suffix, func(t *testing.T) {
			t.Setenv("KY_KYSIGNON_"+suffix, "set")
			_, err := config.LoadFromEnv()
			if err == nil || !strings.Contains(err.Error(), "KY_KYSIGNON_"+suffix) || !strings.Contains(err.Error(), "KY_KYIDENTITY_"+suffix) {
				t.Fatalf("legacy KY_KYSIGNON_%s: err = %v, want one naming both variables", suffix, err)
			}
		})
	}
}

// Compose passes unset variables as empty strings; an empty legacy name is not a misconfiguration.
func TestEmptyLegacyKySignOnEnvironmentIsIgnored(t *testing.T) {
	t.Setenv("KY_DATA_DIR", t.TempDir())
	t.Setenv("KY_KYSIGNON_SECRET", " ")
	t.Setenv("KY_KYIDENTITY_ISSUER", "https://id.example.test")
	t.Setenv("KY_KYIDENTITY_CLIENT_ID", "client")
	t.Setenv("KY_KYIDENTITY_SECRET", "secret")
	t.Setenv("KY_KYIDENTITY_HMAC_SECRET", "hmac")
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if s := cfg.SSO; s.KyIdentityIssuer != "https://id.example.test" || s.KyIdentityClientID != "client" || s.KyIdentitySecret != "secret" || s.KyIdentityHMACSecret != "hmac" {
		t.Fatalf("KY_KYIDENTITY_* not loaded: %+v", s)
	}
}

// Matrix is optional, but a partial or malformed block must stop startup rather than serve a
// .well-known that points members at the wrong homeserver.
func TestMatrixConfigFromEnv(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "admin-secret")
	if err := os.WriteFile(secret, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set := func(name, host, chat string) {
		t.Setenv("KY_DATA_DIR", t.TempDir())
		t.Setenv("KY_MATRIX_ADMIN_URL", "http://mas-admin:8081")
		t.Setenv("KY_MATRIX_ADMIN_CLIENT_ID", "01J0000000000000000000ADMN")
		t.Setenv("KY_MATRIX_ADMIN_SECRET_FILE", secret)
		t.Setenv("KY_MATRIX_SERVER_NAME", name)
		t.Setenv("KY_MATRIX_HOST", host)
		t.Setenv("KY_MATRIX_CHAT_HOST", chat)
		t.Setenv("KY_KYIDENTITY_HMAC_SECRET", "hmac")
	}
	set("", "", "")
	for _, k := range []string{"KY_MATRIX_ADMIN_URL", "KY_MATRIX_ADMIN_CLIENT_ID", "KY_MATRIX_ADMIN_SECRET_FILE"} {
		t.Setenv(k, "")
	}
	cfg, err := config.LoadFromEnv()
	if err != nil || cfg.Matrix != (config.MatrixConfig{}) {
		t.Fatalf("unset: %+v, %v", cfg, err)
	}
	set("example.com", "https://matrix.example.com/", "https://chat.example.com")
	cfg, err = config.LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if want := (config.MatrixConfig{ServerName: "example.com", Host: "https://matrix.example.com", ChatHost: "https://chat.example.com", AdminURL: "http://mas-admin:8081", AdminClientID: "01J0000000000000000000ADMN", AdminSecret: "s3cret"}); cfg.Matrix != want {
		t.Fatalf("got %+v want %+v", cfg.Matrix, want)
	}
	// Without the webhook secret every directory delivery is refused, so a KyIdentity disable
	// or delete never reaches MAS.
	t.Setenv("KY_KYIDENTITY_HMAC_SECRET", "")
	if _, err := config.LoadFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_KYIDENTITY_HMAC_SECRET") {
		t.Errorf("Matrix without HMAC secret: err = %v", err)
	}
	for _, tc := range []struct{ name, host, chat, wantVar string }{
		{"example.com", "https://matrix.example.com", "", "KY_MATRIX_CHAT_HOST"},
		{"", "https://matrix.example.com", "https://chat.example.com", "KY_MATRIX_SERVER_NAME"},
		{"example.com", "http://matrix.example.com", "https://chat.example.com", "KY_MATRIX_HOST"},
		{"example.com", "https://matrix.example.com/x", "https://chat.example.com", "KY_MATRIX_HOST"},
		{"example.com", "https://matrix.example.com", "https://chat.example.com?a", "KY_MATRIX_CHAT_HOST"},
		{"Example.com", "https://matrix.example.com", "https://chat.example.com", "KY_MATRIX_SERVER_NAME"},
		{"localhost", "https://matrix.example.com", "https://chat.example.com", "KY_MATRIX_SERVER_NAME"},
	} {
		set(tc.name, tc.host, tc.chat)
		if _, err := config.LoadFromEnv(); err == nil || !strings.Contains(err.Error(), tc.wantVar) {
			t.Errorf("%+v: err = %v, want one naming %s", tc, err, tc.wantVar)
		}
	}
}
