package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMatrixInitPrintsRegistrationButNoSecrets(t *testing.T) {
	env := map[string]string{
		"KY_MATRIX_SERVER_NAME": "example.com", "KY_MATRIX_HOST": "https://matrix.example.com",
		"KY_MATRIX_AUTH_HOST": "https://auth.example.com", "KY_MATRIX_CHAT_HOST": "https://chat.example.com",
		"KY_ADMIN_HOST": "https://admin.example.com", "KY_KYIDENTITY_ISSUER": "https://id.example.com",
		"KY_MATRIX_MAS_CLIENT_ID": "mas-client", "KY_MATRIX_MAS_CLIENT_SECRET": "clientsecretvalue",
	}
	dir := filepath.Join(t.TempDir(), "matrix")
	var out bytes.Buffer
	if err := runMatrixInit([]string{"-dir", dir}, func(k string) string { return env[k] }, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		"https://auth.example.com/upstream/callback/", "openid profile email", "confidential",
		"KY_MATRIX_UID=" + strconv.Itoa(os.Getuid()), "KY_MATRIX_GID=" + strconv.Itoa(os.Getgid()),
		"secrets/mas_encryption",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "clientsecretvalue") {
		t.Error("printed the client secret")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "secrets"))
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, "secrets", e.Name()))
		if e.Name() != "upstream_provider_id" && strings.Contains(s, strings.TrimSpace(string(b))) {
			t.Errorf("printed secret %s", e.Name())
		}
	}

	delete(env, "KY_MATRIX_HOST")
	if err := runMatrixInit([]string{"-dir", dir}, func(k string) string { return env[k] }, &out); err == nil {
		t.Error("missing env accepted")
	}
}
