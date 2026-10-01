package matrixinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func goodInput() Input {
	return Input{ServerName: "example.com", MatrixHost: "https://matrix.example.com",
		AuthHost: "https://auth.example.com", ChatHost: "https://chat.example.com",
		AdminHost: "https://admin.example.com", Issuer: "https://id.example.com",
		ClientID: "mas-client", ClientSecret: "s3cret"}
}

func TestInitRefusesBadInputs(t *testing.T) {
	for name, mutate := range map[string]func(*Input){
		"http host":            func(i *Input) { i.ChatHost = "http://chat.example.com" },
		"host with path":       func(i *Input) { i.AuthHost = "https://auth.example.com/x" },
		"host with query":      func(i *Input) { i.MatrixHost = "https://matrix.example.com?a=b" },
		"host with userinfo":   func(i *Input) { i.AdminHost = "https://u:p@admin.example.com" },
		"missing host":         func(i *Input) { i.MatrixHost = "" },
		"missing secret":       func(i *Input) { i.ClientSecret = "" },
		"control char in id":   func(i *Input) { i.ClientID = "a\nb: c" },
		"bad server name":      func(i *Input) { i.ServerName = "Not A Domain" },
		"single label server":  func(i *Input) { i.ServerName = "localhost" },
		"http issuer":          func(i *Input) { i.Issuer = "http://id.example.com" },
		"issuer with fragment": func(i *Input) { i.Issuer = "https://id.example.com#x" },
	} {
		in := goodInput()
		mutate(&in)
		dir := t.TempDir()
		if _, err := Run(in, filepath.Join(dir, "m")); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(filepath.Join(dir, "m")); !os.IsNotExist(err) {
			t.Errorf("%s: wrote output before refusing", name)
		}
	}
}

func TestInputFromEnvReadsAndValidates(t *testing.T) {
	env := map[string]string{
		"KY_MATRIX_SERVER_NAME": "example.com", "KY_MATRIX_HOST": "https://matrix.example.com/",
		"KY_MATRIX_AUTH_HOST": "https://auth.example.com", "KY_MATRIX_CHAT_HOST": "https://chat.example.com",
		"KY_ADMIN_HOST": "https://admin.example.com", "KY_KYIDENTITY_ISSUER": "https://id.example.com",
		"KY_MATRIX_MAS_CLIENT_ID": "mas-client", "KY_MATRIX_MAS_CLIENT_SECRET": "s3cret",
	}
	in, err := InputFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := goodInput()
	if in != want {
		t.Errorf("got %+v, want %+v (trailing slash trimmed)", in, want)
	}
	delete(env, "KY_MATRIX_AUTH_HOST")
	if _, err := InputFromEnv(func(k string) string { return env[k] }); err == nil ||
		!strings.Contains(err.Error(), "KY_MATRIX_AUTH_HOST") {
		t.Errorf("missing env not named: %v", err)
	}
}

func TestInitIsWriteOnceForSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, filepath.Join(dir, "secrets"))
	key1, _ := os.ReadFile(filepath.Join(dir, "synapse", "signing.key"))
	in := goodInput()
	in.ChatHost = "https://talk.example.com" // non-secret change
	res, err := Run(in, dir)
	if err != nil {
		t.Fatal(err)
	}
	after := readAll(t, filepath.Join(dir, "secrets"))
	key2, _ := os.ReadFile(filepath.Join(dir, "synapse", "signing.key"))
	if len(before) == 0 || len(before) != len(after) {
		t.Fatalf("secrets changed in count: %d -> %d", len(before), len(after))
	}
	for name, v := range before {
		if after[name] != v {
			t.Errorf("secret %s changed on rerun", name)
		}
	}
	if string(key1) != string(key2) {
		t.Error("signing key changed on rerun")
	}
	if len(res.Created) != 0 || len(res.Kept) != len(before)+1 {
		t.Errorf("rerun created %v, kept %v", res.Created, res.Kept)
	}
	el, _ := os.ReadFile(filepath.Join(dir, "element", "config.json"))
	if !strings.Contains(string(el), "talk.example.com") {
		t.Error("non-secret settings not reconciled")
	}
	for _, p := range []string{"secrets", "synapse", "mas", "postgres", "element",
		"synapse/signing.key", "mas/config.yaml", "synapse/homeserver.yaml", "postgres/init.sql"} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode %v readable by group/other", p, fi.Mode().Perm())
		}
	}
	for name := range after {
		fi, _ := os.Stat(filepath.Join(dir, "secrets", name))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("secret %s mode %v", name, fi.Mode().Perm())
		}
	}
}

func TestInitRefusesAnEmptySecretFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "secrets", "mas_encryption")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(goodInput(), dir); err == nil {
		t.Error("an empty secret was accepted")
	}
}

func TestSynapseConfigIsClosedAndEncrypted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	var hs map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "synapse", "homeserver.yaml"))
	if err := yaml.Unmarshal(b, &hs); err != nil {
		t.Fatal(err)
	}
	if hs["server_name"] != "example.com" || hs["enable_registration"] != false ||
		hs["encryption_enabled_by_default_for_room_type"] != "all" ||
		hs["public_baseurl"] != "https://matrix.example.com/" {
		t.Errorf("synapse settings wrong: %v", hs)
	}
	// Synapse 1.162 docs: an empty list "is the recommended way of disabling federation".
	if fed, ok := hs["federation_domain_whitelist"].([]any); !ok || len(fed) != 0 {
		t.Errorf("federation not disabled: %v", hs["federation_domain_whitelist"])
	}
	if ks, ok := hs["trusted_key_servers"].([]any); !ok || len(ks) != 0 {
		t.Errorf("key servers: %v", hs["trusted_key_servers"])
	}
	for _, l := range hs["listeners"].([]any) {
		for _, r := range l.(map[string]any)["resources"].([]any) {
			for _, n := range r.(map[string]any)["names"].([]any) {
				if n != "client" {
					t.Errorf("listener serves %v", n)
				}
			}
		}
	}
	msc := hs["matrix_authentication_service"].(map[string]any)
	sec, _ := os.ReadFile(filepath.Join(dir, "secrets", "mas_synapse_shared_secret"))
	if msc["enabled"] != true || msc["secret"] != string(sec) {
		t.Errorf("MAS delegation: %v", msc)
	}
	db := hs["database"].(map[string]any)["args"].(map[string]any)
	pw, _ := os.ReadFile(filepath.Join(dir, "secrets", "synapse_db_password"))
	if db["password"] != string(pw) || db["user"] != "synapse" {
		t.Errorf("database args: %v", db)
	}
	key, _ := os.ReadFile(filepath.Join(dir, "synapse", "signing.key"))
	if f := strings.Fields(string(key)); len(f) != 3 || f[0] != "ed25519" || !strings.HasPrefix(f[1], "a_") {
		t.Errorf("signing key format: %q", key)
	}
}

func TestMASConfigTrustsOnlyKyIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	res, err := Run(goodInput(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var mas map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "mas", "config.yaml"))
	if err := yaml.Unmarshal(b, &mas); err != nil {
		t.Fatal(err)
	}
	pw := mas["passwords"].(map[string]any)
	if pw["enabled"] != false {
		t.Error("local passwords enabled")
	}
	if mas["account"].(map[string]any)["password_registration_enabled"] != false {
		t.Error("password registration enabled")
	}
	providers := mas["upstream_oauth2"].(map[string]any)["providers"].([]any)
	if len(providers) != 1 || providers[0].(map[string]any)["issuer"] != "https://id.example.com" {
		t.Errorf("providers: %v", providers)
	}
	p := providers[0].(map[string]any)
	if p["client_secret"] != "s3cret" || p["scope"] != "openid profile email" {
		t.Errorf("provider: %v", p)
	}
	lp := p["claims_imports"].(map[string]any)["localpart"].(map[string]any)
	if lp["action"] != "require" || lp["on_conflict"] != "fail" ||
		!strings.Contains(lp["template"].(string), "| lower") {
		t.Errorf("localpart mapping: %v", lp)
	}
	if strings.Contains(string(b), "insecure") {
		t.Error("shipped MAS config contains an insecure relaxation")
	}
	for _, l := range mas["http"].(map[string]any)["listeners"].([]any) {
		for _, r := range l.(map[string]any)["resources"].([]any) {
			n := r.(map[string]any)["name"]
			if n == "compat" || (n == "adminapi" && l.(map[string]any)["name"] == "web") {
				t.Errorf("listener %v serves %v", l.(map[string]any)["name"], n)
			}
		}
	}
	keys := mas["secrets"].(map[string]any)["keys"].([]any)
	if len(keys) == 0 || !strings.Contains(keys[0].(map[string]any)["key"].(string), "PRIVATE KEY") {
		t.Errorf("signing keys: %v", keys)
	}
	id := strings.TrimPrefix(res.Registration.RedirectURI, "https://auth.example.com/upstream/callback/")
	if id != p["id"] || len(id) != 26 ||
		strings.Join(res.Registration.Scopes, " ") != "openid profile email" ||
		res.Registration.ClientType != "confidential" {
		t.Errorf("registration: %+v", res.Registration)
	}
}

func TestClientSecretCannotInjectConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	in := goodInput()
	in.ClientSecret = `x" , "passwords": {"enabled": true}, "y": "`
	if _, err := Run(in, dir); err != nil {
		t.Fatal(err)
	}
	var mas map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "mas", "config.yaml"))
	if err := yaml.Unmarshal(b, &mas); err != nil {
		t.Fatal(err)
	}
	p := mas["upstream_oauth2"].(map[string]any)["providers"].([]any)[0].(map[string]any)
	if p["client_secret"] != in.ClientSecret || mas["passwords"].(map[string]any)["enabled"] != false {
		t.Errorf("secret escaped its field: %v", p["client_secret"])
	}
}

func TestElementAndPostgresConfigs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	var el struct {
		Default struct {
			HS struct {
				BaseURL    string `json:"base_url"`
				ServerName string `json:"server_name"`
			} `json:"m.homeserver"`
		} `json:"default_server_config"`
		DisableCustomURLs bool `json:"disable_custom_urls"`
		DisableGuests     bool `json:"disable_guests"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "element", "config.json"))
	if err := json.Unmarshal(b, &el); err != nil {
		t.Fatal(err)
	}
	if el.Default.HS.BaseURL != "https://matrix.example.com" || el.Default.HS.ServerName != "example.com" ||
		!el.DisableCustomURLs || !el.DisableGuests {
		t.Errorf("element: %s", b)
	}
	sql, _ := os.ReadFile(filepath.Join(dir, "postgres", "init.sql"))
	spw, _ := os.ReadFile(filepath.Join(dir, "secrets", "synapse_db_password"))
	mpw, _ := os.ReadFile(filepath.Join(dir, "secrets", "mas_db_password"))
	for _, want := range []string{
		"CREATE USER synapse PASSWORD '" + string(spw) + "'",
		"CREATE USER mas PASSWORD '" + string(mpw) + "'",
		"CREATE DATABASE synapse OWNER synapse ENCODING 'UTF8' LC_COLLATE='C' LC_CTYPE='C' TEMPLATE template0",
		"CREATE DATABASE mas OWNER mas",
	} {
		if !strings.Contains(string(sql), want) {
			t.Errorf("init.sql lacks %q", want)
		}
	}
	if string(spw) == string(mpw) {
		t.Error("synapse and mas share a database password")
	}
}

func readAll(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		out[e.Name()] = string(b)
	}
	return out
}
