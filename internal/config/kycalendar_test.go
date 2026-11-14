package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
)

func TestKyCalendarPairFromEnv(t *testing.T) {
	dir := t.TempDir()
	file := func(name, v string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	long, short := file("long", strings.Repeat("k", 32)+"\n"), file("short", strings.Repeat("k", 31))
	matrix := func(on bool) {
		t.Setenv("KY_DATA_DIR", t.TempDir())
		vars := map[string]string{
			"KY_MATRIX_DIR": "/matrix", "KY_MATRIX_MEDIA_DIR": "/matrix-media", "KY_MATRIX_BACKUP_DB_PASSWORD_FILE": file("pw", "pw"),
			"KY_MATRIX_ADMIN_URL": "http://mas-admin:8081", "KY_MATRIX_ADMIN_CLIENT_ID": "01J0000000000000000000ADMN",
			"KY_MATRIX_ADMIN_SECRET_FILE": file("admin", "s3cret"), "KY_MATRIX_SERVER_NAME": "example.com",
			"KY_MATRIX_HOST": "https://matrix.example.com", "KY_MATRIX_CHAT_HOST": "https://chat.example.com", "KY_KYIDENTITY_HMAC_SECRET": "hmac",
		}
		for k, v := range vars {
			if !on {
				v = ""
			}
			t.Setenv(k, v)
		}
	}
	for _, c := range []struct {
		name, url, secret string
		matrix            bool
		err               string
	}{
		{"unset", "", "", true, ""},
		{"url only", "https://cal.example.com", "", true, "set together"},
		{"file only", "", long, true, "set together"},
		{"no matrix", "https://cal.example.com", long, false, "needs the Matrix stack"},
		{"plain http", "http://cal.example.com", long, true, "KY_KYCALENDAR_URL"},
		{"path", "https://cal.example.com/cal", long, true, "KY_KYCALENDAR_URL"},
		{"short secret", "https://cal.example.com", short, true, "at least 32 bytes"},
		{"missing file", "https://cal.example.com", filepath.Join(dir, "none"), true, "at least 32 bytes"},
		{"ok", "https://cal.example.com/", long, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			matrix(c.matrix)
			t.Setenv("KY_KYCALENDAR_URL", c.url)
			t.Setenv("KY_KYCALENDAR_SECRET_FILE", c.secret)
			cfg, err := config.LoadFromEnv()
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) || strings.Contains(err.Error(), "kkkk") {
					t.Fatalf("err %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.url == "" && (cfg.KyCalendar.URL != "" || cfg.KyCalendar.Secret != nil) {
				t.Fatalf("unset: %+v", cfg.KyCalendar)
			}
			if c.url != "" && (cfg.KyCalendar.URL != "https://cal.example.com" || string(cfg.KyCalendar.Secret) != strings.Repeat("k", 32)) {
				t.Fatalf("pair: %+v", cfg.KyCalendar)
			}
		})
	}
}
