package matrixinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func chatOnlyEnv() map[string]string {
	return map[string]string{
		"KY_MATRIX_SERVER_NAME": "example.com", "KY_MATRIX_HOST": "https://matrix.example.com",
		"KY_MATRIX_AUTH_HOST": "https://auth.example.com", "KY_MATRIX_CHAT_HOST": "https://chat.example.com",
		"KY_ADMIN_HOST": "https://admin.example.com", "KY_KYIDENTITY_ISSUER": "https://id.example.com",
		"KY_MATRIX_MAS_CLIENT_ID": "mas-client", "KY_MATRIX_CALLS": "off",
	}
}

// Chat only: no RTC host or media IP, no call discovery anywhere, Element without call
// buttons, and no LiveKit config. LiveKit's secrets still exist: Compose mounts them.
func TestChatOnlyNeedsNoCallSettingsAndAdvertisesNoCalls(t *testing.T) {
	env := chatOnlyEnv()
	in, err := InputFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !in.CallsOff {
		t.Fatal("KY_MATRIX_CALLS=off did not turn calls off")
	}
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, in, dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "livekit/config.yaml")); !os.IsNotExist(err) {
		t.Errorf("chat only rendered a LiveKit config: %v", err)
	}
	for _, f := range []string{"secrets/livekit_api_key", "secrets/livekit_api_secret", "secrets/notes_api_secret"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: Compose mounts it, so it must exist: %v", f, err)
		}
	}
	hs := synapseConfig(t, dir)
	if _, ok := hs["matrix_rtc"]; ok {
		t.Error("Synapse still advertises a call transport")
	}
	var el map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "element/config.json"))
	if err := json.Unmarshal(b, &el); err != nil {
		t.Fatal(err)
	}
	if ec := el["element_call"].(map[string]any); ec["disable"] != true {
		t.Errorf("Element Call is not disabled: %v", ec)
	}
	if v := el["setting_defaults"].(map[string]any)["UIFeature.voip"]; v != false {
		t.Errorf("Element still shows call buttons: UIFeature.voip=%v", v)
	}
	// Turning calls back on renders LiveKit again with the same keys.
	on := goodInput()
	if _, err := runWithSecret(t, on, dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "livekit/config.yaml")); err != nil {
		t.Errorf("calls on did not render LiveKit: %v", err)
	}
}

func TestCallsSwitchIsStrict(t *testing.T) {
	for name, set := range map[string]map[string]string{
		"unknown value":       {"KY_MATRIX_CALLS": "no"},
		"TURN with calls off": {"KY_MATRIX_TURN_HOST": "turn.example.com", "KY_MATRIX_TURN_CERT_FILE": "/c", "KY_MATRIX_TURN_KEY_FILE": "/k"},
	} {
		env := chatOnlyEnv()
		for k, v := range set {
			env[k] = v
		}
		if _, err := InputFromEnv(func(k string) string { return env[k] }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// On, explicitly or by default, still needs both call settings.
	for _, v := range []string{"", "on"} {
		env := chatOnlyEnv()
		env["KY_MATRIX_CALLS"] = v
		if _, err := InputFromEnv(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "KY_MATRIX_RTC_HOST") {
			t.Errorf("KY_MATRIX_CALLS=%q without call settings: %v", v, err)
		}
	}
}

// The media ports LiveKit advertises are the ones Compose publishes: both read
// KY_MATRIX_MEDIA_TCP_PORT and KY_MATRIX_MEDIA_UDP_PORT, defaulting to 7881 and 7882.
func TestLiveKitMediaPortsAreConfigurable(t *testing.T) {
	env := chatOnlyEnv()
	delete(env, "KY_MATRIX_CALLS")
	env["KY_MATRIX_RTC_HOST"], env["KY_MATRIX_MEDIA_IP"] = "https://sfu.example.com", "192.0.2.10"
	env["KY_MATRIX_MEDIA_TCP_PORT"], env["KY_MATRIX_MEDIA_UDP_PORT"] = "17881", "17882"
	in, err := InputFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(in, dir); err != nil {
		t.Fatal(err)
	}
	var lk map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "livekit/config.yaml"))
	if err := yaml.Unmarshal(b, &lk); err != nil {
		t.Fatal(err)
	}
	if rtc := lk["rtc"].(map[string]any); rtc["tcp_port"] != 17881 || rtc["udp_port"] != 17882 {
		t.Errorf("LiveKit advertises %v/%v, want 17881/17882", rtc["tcp_port"], rtc["udp_port"])
	}
	for name, ports := range map[string][2]string{
		"not a number":      {"78a1", ""},
		"zero":              {"0", ""},
		"privileged":        {"443", ""},
		"too large":         {"", "65536"},
		"signalling port":   {"7880", ""},
		"TURN TLS listener": {"5349", ""},
		"TURN UDP port":     {"", "3478"},
		"TURN relay range":  {"", "30050"},
		"leading zero":      {"07881", ""},
		"signed":            {"+7881", ""},
	} {
		e := map[string]string{}
		for k, v := range env {
			e[k] = v
		}
		e["KY_MATRIX_MEDIA_TCP_PORT"], e["KY_MATRIX_MEDIA_UDP_PORT"] = ports[0], ports[1]
		if _, err := InputFromEnv(func(k string) string { return e[k] }); err == nil {
			t.Errorf("%s (%q/%q): accepted", name, ports[0], ports[1])
		}
	}
}
