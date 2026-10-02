package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func matrixEnv() map[string]string {
	return map[string]string{
		"KY_MATRIX_SERVER_NAME": "example.com", "KY_MATRIX_HOST": "https://matrix.example.com",
		"KY_MATRIX_AUTH_HOST": "https://auth.example.com", "KY_MATRIX_CHAT_HOST": "https://chat.example.com",
		"KY_ADMIN_HOST": "https://admin.example.com", "KY_KYIDENTITY_ISSUER": "https://id.example.com",
		"KY_MATRIX_MAS_CLIENT_ID": "mas-client",
	}
}

func TestMatrixInitTwoPassesPrintRegistrationButNoSecrets(t *testing.T) {
	env := matrixEnv()
	getenv := func(k string) string { return env[k] }
	dir := filepath.Join(t.TempDir(), "matrix")
	var out bytes.Buffer
	if err := runMatrixInit([]string{"-dir", dir}, getenv, 4242, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{
		"https://auth.example.com/upstream/callback/", "openid profile email", "confidential",
		"KY_MATRIX_UID=4242", "KY_MATRIX_GID=" + strconv.Itoa(os.Getgid()),
		"secrets/mas_encryption",
		"Register this client in KyIdentity, save the secret it shows to " + dir +
			"/secrets/kyidentity_client_secret (mode 0600), and run matrix-init again.",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("first pass output lacks %q:\n%s", want, s)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "mas", "config.yaml")); !os.IsNotExist(err) {
		t.Error("first pass rendered MAS")
	}

	if err := os.WriteFile(filepath.Join(dir, "secrets", "kyidentity_client_secret"), []byte("clientsecretvalue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runMatrixInit([]string{"-dir", dir}, getenv, 4242, &out); err != nil {
		t.Fatal(err)
	}
	s = out.String()
	if strings.Contains(s, "run matrix-init again") || !strings.Contains(s, "mas/config.yaml") ||
		!strings.Contains(s, "docker compose restart synapse mas element") {
		t.Errorf("second pass output:\n%s", s)
	}
	if !strings.Contains(s, "back-channel logout URI") || !strings.Contains(s, "KY_MATRIX_ADMIN_CLIENT_ID=") {
		t.Errorf("output lacks back-channel URI or admin client ID:\n%s", s)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "secrets"))
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, "secrets", e.Name()))
		if e.Name() != "upstream_provider_id" && e.Name() != "mas_admin_client_id" && strings.Contains(s, strings.TrimSpace(string(b))) {
			t.Errorf("printed secret %s", e.Name())
		}
	}

	delete(env, "KY_MATRIX_HOST")
	if err := runMatrixInit([]string{"-dir", dir}, getenv, 4242, &out); err == nil {
		t.Error("missing env accepted")
	}
}

// Root would make every Matrix container run as root.
func TestMatrixInitRefusesRoot(t *testing.T) {
	env := matrixEnv()
	dir := filepath.Join(t.TempDir(), "matrix")
	var out bytes.Buffer
	err := runMatrixInit([]string{"-dir", dir}, func(k string) string { return env[k] }, 0, &out)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("err = %v, want a refusal naming root", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("wrote output as root")
	}
}
