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
	backupPW := filepath.Join(t.TempDir(), "kyb")
	if err := os.WriteFile(backupPW, []byte("kybpw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{
		"KY_MATRIX_SERVER_NAME": "example.com", "KY_MATRIX_HOST": "https://matrix.example.com",
		"KY_MATRIX_CHAT_HOST": "https://chat.example.com", "KY_MATRIX_ADMIN_URL": "http://mas-admin:8081",
		"KY_MATRIX_ADMIN_CLIENT_ID": "01J0000000000000000000ADMN", "KY_MATRIX_ADMIN_SECRET_FILE": secret,
		"KY_MATRIX_DB_HOST": "", "KY_MATRIX_DIR": "/matrix", "KY_MATRIX_MEDIA_DIR": "/matrix-media", "KY_MATRIX_BACKUP_DB_PASSWORD_FILE": backupPW,
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
	if m.Dir != "/matrix" || m.MediaDir != "/matrix-media" || m.DBHost != "postgres" || m.BackupDBPassword != "kybpw" {
		t.Fatalf("backup fields: %+v", m)
	}
	for name, over := range map[string]map[string]string{
		"no admin URL":         {"KY_MATRIX_ADMIN_URL": ""},
		"no client ID":         {"KY_MATRIX_ADMIN_CLIENT_ID": ""},
		"no secret file":       {"KY_MATRIX_ADMIN_SECRET_FILE": ""},
		"missing file":         {"KY_MATRIX_ADMIN_SECRET_FILE": secret + ".nope"},
		"directory":            {"KY_MATRIX_ADMIN_SECRET_FILE": t.TempDir()},
		"admin URL path":       {"KY_MATRIX_ADMIN_URL": "http://mas-admin:8081/x"},
		"admin URL ftp":        {"KY_MATRIX_ADMIN_URL": "ftp://mas-admin:8081"},
		"admin URL userinfo":   {"KY_MATRIX_ADMIN_URL": "http://u:p@mas-admin:8081"},
		"no matrix dir":        {"KY_MATRIX_DIR": ""},
		"relative matrix dir":  {"KY_MATRIX_DIR": "matrix"},
		"no media dir":         {"KY_MATRIX_MEDIA_DIR": ""},
		"relative media dir":   {"KY_MATRIX_MEDIA_DIR": "media"},
		"no backup pw file":    {"KY_MATRIX_BACKUP_DB_PASSWORD_FILE": ""},
		"missing backup pw":    {"KY_MATRIX_BACKUP_DB_PASSWORD_FILE": backupPW + ".nope"},
		"db host is an option": {"KY_MATRIX_DB_HOST": "-oProxyCommand=x"},
		"db host with a space": {"KY_MATRIX_DB_HOST": "postgres x"},
	} {
		set(over)
		if _, err := matrixFromEnv(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	set(map[string]string{"KY_MATRIX_ADMIN_URL": "http://u:hunter2@mas-admin:8081/?token=tok3n"})
	if _, err := matrixFromEnv(); err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "tok3n") {
		t.Errorf("admin URL credentials leak into the error: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "e")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set(map[string]string{"KY_MATRIX_ADMIN_SECRET_FILE": empty})
	if _, err := matrixFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_MATRIX_ADMIN_SECRET_FILE") {
		t.Errorf("empty secret: %v", err)
	}
	set(map[string]string{"KY_MATRIX_BACKUP_DB_PASSWORD_FILE": empty})
	if _, err := matrixFromEnv(); err == nil || !strings.Contains(err.Error(), "KY_MATRIX_BACKUP_DB_PASSWORD_FILE") {
		t.Errorf("empty backup password: %v", err)
	}
	for k := range base {
		t.Setenv(k, "")
	}
	if m, err := matrixFromEnv(); err != nil || m.Enabled() {
		t.Errorf("unset block: %+v %v", m, err)
	}
}
