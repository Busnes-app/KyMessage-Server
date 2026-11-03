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
		"KY_MATRIX_RTC_HOST":      "https://sfu.example.com", "KY_MATRIX_MEDIA_IP": "192.0.2.10",
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
	if !strings.Contains(s, "back-channel logout URI") ||
		!strings.Contains(s, "KyMessages' MAS admin client (offboarding and the console). Set:\n  KY_MATRIX_ADMIN_CLIENT_ID=") {
		t.Errorf("output lacks back-channel URI or the labelled admin client ID:\n%s", s)
	}
	if !strings.Contains(s, "-f /docker-entrypoint-initdb.d/kybackup-role.sql") {
		t.Errorf("second pass does not say how to apply the backup role to a running stack:\n%s", s)
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

// The second pass says what Compose needs: the chat-only overlay with calls off, the media
// ports to publish when they are not the defaults, and nothing about ports otherwise.
func TestMatrixInitSaysWhatComposeNeedsForCalls(t *testing.T) {
	for name, c := range map[string]struct {
		set        map[string]string
		want, deny []string
	}{
		"default": {nil, []string{"forward TCP 7881 and UDP 7882", "restart synapse mas element livekit"},
			[]string{"KY_MATRIX_MEDIA_TCP_PORT", "chat-only"}},
		"ports": {map[string]string{"KY_MATRIX_MEDIA_TCP_PORT": "17881", "KY_MATRIX_MEDIA_UDP_PORT": "17882"},
			[]string{"forward TCP 17881 and UDP 17882", "KY_MATRIX_MEDIA_TCP_PORT=17881\n  KY_MATRIX_MEDIA_UDP_PORT=17882"}, nil},
		"chat only": {map[string]string{"KY_MATRIX_CALLS": "off", "KY_MATRIX_RTC_HOST": "", "KY_MATRIX_MEDIA_IP": ""},
			[]string{"KY_MATRIX_CALLS=off", "docker-compose.chat-only.yml", "restart synapse mas element\n"},
			[]string{"element livekit", "livekit/config.yaml", "forward TCP"}},
	} {
		env := matrixEnv()
		for k, v := range c.set {
			env[k] = v
		}
		dir := filepath.Join(t.TempDir(), "matrix")
		if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "secrets", "kyidentity_client_secret"), []byte("clientsecretvalue\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runMatrixInit([]string{"-dir", dir}, func(k string) string { return env[k] }, 4242, &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		s := out.String()
		for _, w := range c.want {
			if !strings.Contains(s, w) {
				t.Errorf("%s: output lacks %q:\n%s", name, w, s)
			}
		}
		for _, d := range c.deny {
			if strings.Contains(s, d) {
				t.Errorf("%s: output has %q:\n%s", name, d, s)
			}
		}
	}
}
