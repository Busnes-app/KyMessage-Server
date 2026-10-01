package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatrixConfigRequiresAdminAccess(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(secret, []byte("abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		"KY_MATRIX_SERVER_NAME": "example.com", "KY_MATRIX_HOST": "https://matrix.example.com",
		"KY_MATRIX_CHAT_HOST": "https://chat.example.com", "KY_MATRIX_ADMIN_URL": "http://mas-admin:8081",
		"KY_MATRIX_ADMIN_CLIENT_ID": "01J0000000000000000000ADMN", "KY_MATRIX_ADMIN_SECRET_FILE": secret,
	}
	set := func(over map[string]string) {
		for k, v := range base {
			t.Setenv(k, v)
		}
		for k, v := range over {
			t.Setenv(k, v)
		}
	}
	set(nil)
	m, err := matrixFromEnv()
	if err != nil || m.AdminSecret != "abc123" || m.AdminURL != "http://mas-admin:8081" || !m.Enabled() {
		t.Fatalf("good config: %+v %v", m, err)
	}
	for name, over := range map[string]map[string]string{
		"no admin URL":       {"KY_MATRIX_ADMIN_URL": ""},
		"no client ID":       {"KY_MATRIX_ADMIN_CLIENT_ID": ""},
		"no secret file":     {"KY_MATRIX_ADMIN_SECRET_FILE": ""},
		"missing file":       {"KY_MATRIX_ADMIN_SECRET_FILE": secret + ".nope"},
		"directory":          {"KY_MATRIX_ADMIN_SECRET_FILE": t.TempDir()},
		"admin URL path":     {"KY_MATRIX_ADMIN_URL": "http://mas-admin:8081/x"},
		"admin URL ftp":      {"KY_MATRIX_ADMIN_URL": "ftp://mas-admin:8081"},
		"admin URL userinfo": {"KY_MATRIX_ADMIN_URL": "http://u:p@mas-admin:8081"},
	} {
		set(over)
		if _, err := matrixFromEnv(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	empty := filepath.Join(t.TempDir(), "e")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set(map[string]string{"KY_MATRIX_ADMIN_SECRET_FILE": empty})
	if _, err := matrixFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_MATRIX_ADMIN_SECRET_FILE") {
		t.Errorf("empty secret: %v", err)
	}
	for k := range base {
		t.Setenv(k, "")
	}
	if m, err := matrixFromEnv(); err != nil || m.Enabled() {
		t.Errorf("unset block: %+v %v", m, err)
	}
}
