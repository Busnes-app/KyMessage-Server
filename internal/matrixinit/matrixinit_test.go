package matrixinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func goodInput() Input {
	return Input{ServerName: "example.com", MatrixHost: "https://matrix.example.com",
		AuthHost: "https://auth.example.com", ChatHost: "https://chat.example.com",
		AdminHost: "https://admin.example.com", Issuer: "https://id.example.com",
		ClientID: "mas-client"}
}

// runWithSecret saves the KyIdentity-issued client secret the way the operator does, then runs.
func runWithSecret(t *testing.T, in Input, dir, secret string) (Result, error) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ClientSecretFile), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Run(in, dir)
}

func TestInitRefusesBadInputs(t *testing.T) {
	for name, mutate := range map[string]func(*Input){
		"http host":            func(i *Input) { i.ChatHost = "http://chat.example.com" },
		"host with path":       func(i *Input) { i.AuthHost = "https://auth.example.com/x" },
		"host with query":      func(i *Input) { i.MatrixHost = "https://matrix.example.com?a=b" },
		"host with userinfo":   func(i *Input) { i.AdminHost = "https://u:p@admin.example.com" },
		"missing host":         func(i *Input) { i.MatrixHost = "" },
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
		"KY_MATRIX_MAS_CLIENT_ID": "mas-client",
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
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
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
	for _, n := range []string{"mas_admin_client_id", "mas_admin_client_secret", "kybackup_db_password"} {
		if before[n] == "" {
			t.Errorf("secret %s not created", n)
		}
	}
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
	// Kept: every generated secret (all of secrets/ but the operator's client secret) plus the signing key.
	if len(res.Created) != 0 || len(res.Kept) != len(before) {
		t.Errorf("rerun created %v, kept %v", res.Created, res.Kept)
	}
	el, _ := os.ReadFile(filepath.Join(dir, "element", "config.json"))
	if !strings.Contains(string(el), "talk.example.com") {
		t.Error("non-secret settings not reconciled")
	}
	for _, p := range []string{"secrets", "synapse", "mas", "postgres", "element",
		"synapse/signing.key", "mas/config.yaml", "synapse/homeserver.yaml", "postgres/init.sql", "postgres/kybackup-role.sql"} {
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
	res, err := runWithSecret(t, goodInput(), dir, "s3cret")
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
	// The public listener serves no compat login and no admin API; the admin API is on its own
	// listener bound only to the internal matrix-admin network alias.
	listeners := mas["http"].(map[string]any)["listeners"].([]any)
	if len(listeners) != 2 {
		t.Fatalf("MAS has %d listeners, want web and admin", len(listeners))
	}
	for _, l := range listeners {
		lm := l.(map[string]any)
		var names []string
		for _, r := range lm["resources"].([]any) {
			names = append(names, r.(map[string]any)["name"].(string))
		}
		binds := lm["binds"].([]any)
		switch lm["name"] {
		case "web":
			if slices.Contains(names, "compat") || slices.Contains(names, "adminapi") {
				t.Errorf("public listener serves %v", names)
			}
		case "admin":
			if !slices.Equal(names, []string{"adminapi", "oauth"}) {
				t.Errorf("admin listener resources %v", names)
			}
			if len(binds) != 1 || binds[0].(map[string]any)["host"] != "mas-admin" || binds[0].(map[string]any)["port"] != 8081 {
				t.Errorf("admin listener binds %v, want only mas-admin:8081", binds)
			}
		default:
			t.Errorf("unexpected listener %v", lm["name"])
		}
	}
	if p["on_backchannel_logout"] != "logout_all" {
		t.Errorf("on_backchannel_logout = %v", p["on_backchannel_logout"])
	}
	clients := mas["clients"].([]any)
	admins := mas["policy"].(map[string]any)["data"].(map[string]any)["admin_clients"].([]any)
	c := clients[0].(map[string]any)
	if len(clients) != 1 || len(admins) != 1 || admins[0] != c["client_id"] || c["client_id"] != res.AdminClientID ||
		c["client_auth_method"] != "client_secret_basic" || len(c["client_secret"].(string)) != 64 {
		t.Errorf("admin client %v, admin_clients %v", clients, admins)
	}
	if want := "https://auth.example.com/upstream/backchannel-logout/" + p["id"].(string); res.Registration.BackchannelLogoutURI != want {
		t.Errorf("back-channel URI %q, want %q", res.Registration.BackchannelLogoutURI, want)
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
	secret := `x" , "passwords": {"enabled": true}, "y": "`
	if _, err := runWithSecret(t, goodInput(), dir, secret); err != nil {
		t.Fatal(err)
	}
	var mas map[string]any
	b, _ := os.ReadFile(filepath.Join(dir, "mas", "config.yaml"))
	if err := yaml.Unmarshal(b, &mas); err != nil {
		t.Fatal(err)
	}
	p := mas["upstream_oauth2"].(map[string]any)["providers"].([]any)[0].(map[string]any)
	if p["client_secret"] != secret || mas["passwords"].(map[string]any)["enabled"] != false {
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
		"REVOKE ALL ON DATABASE synapse FROM PUBLIC;",
		"REVOKE ALL ON DATABASE mas FROM PUBLIC;",
	} {
		if !strings.Contains(string(sql), want) {
			t.Errorf("init.sql lacks %q", want)
		}
	}
	// The Postgres superuser password reaches the container only as a Compose secret file.
	ppw, err := os.ReadFile(filepath.Join(dir, "secrets", "postgres_password"))
	if err != nil || len(ppw) == 0 {
		t.Fatalf("postgres superuser password: %v", err)
	}
	if string(spw) == string(mpw) || string(ppw) == string(spw) || string(ppw) == string(mpw) {
		t.Error("database passwords are shared")
	}
}

func TestElementIsBrandedAndContactsNoThirdParty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "element", "config.json"))
	var el map[string]any
	if err := json.Unmarshal(b, &el); err != nil {
		t.Fatal(err)
	}
	br, _ := el["branding"].(map[string]any)
	if el["brand"] != "KyMessages" || br["auth_header_logo_url"] != "https://admin.example.com/app-icon.png" ||
		br["logo_link_url"] != "https://chat.example.com" {
		t.Errorf("branding: %v %v", el["brand"], br)
	}
	if links, ok := br["auth_footer_links"].([]any); !ok || len(links) != 0 {
		t.Errorf("auth_footer_links: %v", br["auth_footer_links"])
	}
	defaults, _ := el["setting_defaults"].(map[string]any)
	themes, _ := defaults["custom_themes"].([]any)
	names := map[string]bool{}
	for _, th := range themes {
		m := th.(map[string]any)
		names[m["name"].(string)] = m["is_dark"].(bool)
		if c, _ := m["colors"].(map[string]any); c["accent-color"] == nil {
			t.Errorf("theme %v has no accent", m["name"])
		}
	}
	if len(names) != 2 || names["Busnes Light"] || !names["Busnes Dark"] {
		t.Errorf("themes: %v", names)
	}
	if el["default_theme"] != "custom-Busnes Light" {
		t.Errorf("default_theme: %v", el["default_theme"])
	}

	// Element's built-in defaults point at element.io and scalar.vector.im; ours must not.
	for _, host := range []string{"element.io", "vector.im"} {
		if strings.Contains(string(b), host) {
			t.Errorf("config mentions %s", host)
		}
	}
	for _, k := range []string{"integrations_ui_url", "integrations_rest_url"} {
		if v, ok := el[k]; !ok || v != nil {
			t.Errorf("%s = %v, want null", k, v)
		}
	}
	if w, ok := el["integrations_widgets_urls"].([]any); !ok || len(w) != 0 {
		t.Errorf("integrations_widgets_urls: %v", el["integrations_widgets_urls"])
	}
	for _, k := range []string{"help_url", "help_encryption_url", "help_key_storage_url"} {
		if v, _ := el[k].(string); !strings.HasPrefix(v, "https://admin.example.com/") {
			t.Errorf("%s = %v", k, el[k])
		}
	}
	if ec, _ := el["element_call"].(map[string]any); ec["disable"] != true {
		t.Errorf("element_call: %v", el["element_call"])
	}
	if defaults["UIFeature.voip"] != false || defaults["UIFeature.widgets"] != false {
		t.Errorf("calls/widgets UI not disabled: %v", defaults)
	}
	if j, _ := el["jitsi"].(map[string]any); j["preferred_domain"] != "jitsi.invalid" {
		t.Errorf("jitsi: %v", el["jitsi"])
	}
	rd, _ := el["room_directory"].(map[string]any)
	if s, _ := rd["servers"].([]any); len(s) != 1 || s[0] != "example.com" {
		t.Errorf("room_directory: %v", el["room_directory"])
	}
}

// An existing directory is the operator's: refuse a loose one rather than lock down, say, "-dir .".
func TestInitRefusesALooseExistingDirectory(t *testing.T) {
	for _, sub := range []string{".", "secrets"} {
		dir := filepath.Join(t.TempDir(), "m")
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Run(goodInput(), dir)
		if err == nil || !strings.Contains(err.Error(), "chmod 700") {
			t.Errorf("%s 0755: err = %v, want a refusal naming chmod 700", sub, err)
		}
		if fi, _ := os.Stat(filepath.Join(dir, sub)); fi.Mode().Perm() != 0o755 {
			t.Errorf("%s: mode changed to %v", sub, fi.Mode().Perm())
		}
		if _, err := os.Stat(filepath.Join(dir, "secrets", "mas_encryption")); !os.IsNotExist(err) {
			t.Errorf("%s: wrote secrets into a loose tree", sub)
		}
	}
	dir := filepath.Join(t.TempDir(), "m")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(goodInput(), dir); err != nil {
		t.Errorf("existing 0700 dir refused: %v", err)
	}
}

// Without the KyIdentity-issued secret there is no MAS config, so Compose cannot start MAS
// half-configured; everything else is rendered so the operator can register the client.
func TestFirstRunWithoutClientSecretSkipsMAS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	res, err := Run(goodInput(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ClientSecretMissing {
		t.Error("ClientSecretMissing not reported")
	}
	if _, err := os.Stat(filepath.Join(dir, "mas", "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("mas/config.yaml rendered without a client secret: %v", err)
	}
	for _, p := range []string{"synapse/homeserver.yaml", "element/config.json", "postgres/init.sql"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if res.Registration.RedirectURI == "" {
		t.Error("no registration values")
	}
	res, err = runWithSecret(t, goodInput(), dir, "issued-by-kyidentity")
	if err != nil || res.ClientSecretMissing {
		t.Fatalf("second pass: %v, missing=%v", err, res.ClientSecretMissing)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "mas", "config.yaml"))
	if !strings.Contains(string(b), `client_secret: "issued-by-kyidentity"`) {
		t.Errorf("MAS config lacks the saved secret:\n%s", b)
	}
}

func TestClientSecretFileMustBePrivateAndClean(t *testing.T) {
	for name, c := range map[string]struct {
		content string
		mode    os.FileMode
		want    string
	}{
		"0644":         {"s3cret", 0o644, "chmod 600"},
		"0640":         {"s3cret", 0o640, "chmod 600"},
		"empty":        {"", 0o600, "empty"},
		"control char": {"a\rb", 0o600, "control"},
	} {
		dir := filepath.Join(t.TempDir(), "m")
		if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, ClientSecretFile)
		if err := os.WriteFile(p, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, c.mode); err != nil {
			t.Fatal(err)
		}
		_, err := Run(goodInput(), dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, c.want)
		}
		if _, err := os.Stat(filepath.Join(dir, "mas", "config.yaml")); !os.IsNotExist(err) {
			t.Errorf("%s: rendered MAS anyway", name)
		}
	}
	// 0400 is stricter than 0600 and fine.
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, ClientSecretFile), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(goodInput(), dir); err != nil {
		t.Errorf("0400 refused: %v", err)
	}
}

// A kept secret that others can read is refused, not silently tightened: the operator should
// know it was exposed.
func TestInitRefusesALooseKeptSecret(t *testing.T) {
	for _, rel := range []string{"secrets/mas_encryption", "synapse/signing.key"} {
		dir := filepath.Join(t.TempDir(), "m")
		if _, err := Run(goodInput(), dir); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, rel)
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(goodInput(), dir); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("%s 0644: err = %v", rel, err)
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode silently changed", rel)
		}
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

// The backup role is created by its own idempotent file, which must sort after init.sql: the
// Postgres entrypoint runs /docker-entrypoint-initdb.d/*.sql in name order and stops at the
// first error, and the role's grants name databases init.sql creates.
func TestBackupRoleSQL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	res, err := Run(goodInput(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Rendered, "postgres/kybackup-role.sql") {
		t.Fatalf("rendered %v", res.Rendered)
	}
	if !slices.Contains(res.Created, "secrets/kybackup_db_password") {
		t.Fatalf("created %v", res.Created)
	}
	var names []string
	for _, r := range res.Rendered {
		if strings.HasPrefix(r, "postgres/") && strings.HasSuffix(r, ".sql") {
			names = append(names, filepath.Base(r))
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"init.sql", "kybackup-role.sql"}) {
		t.Fatalf("entrypoint order %v: init.sql must run first", names)
	}
	pw, _ := os.ReadFile(filepath.Join(dir, "secrets", "kybackup_db_password"))
	b, err := os.ReadFile(filepath.Join(dir, "postgres", "kybackup-role.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, want := range []string{
		"CREATE ROLE kybackup;",
		"EXCEPTION WHEN duplicate_object THEN NULL;",
		"ALTER ROLE kybackup WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '" + string(pw) + "';",
		"GRANT pg_read_all_data TO kybackup;",
		"GRANT CONNECT ON DATABASE synapse, mas TO kybackup;",
		"REVOKE CONNECT ON DATABASE postgres, template1 FROM PUBLIC;",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("kybackup-role.sql lacks %q:\n%s", want, sql)
		}
	}
	for _, bad := range []string{"pg_write_all_data", "GRANT ALL"} {
		if strings.Contains(sql, bad) {
			t.Errorf("kybackup-role.sql grants %q", bad)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "postgres", "kybackup-role.sql")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600: it holds the password", fi.Mode().Perm())
	}
}

// After a restore the capsule has every secret but postgres_password; matrix-init makes only
// that one and keeps the rest, so the restored configs and databases still match.
func TestInitRegeneratesOnlyAMissingSecret(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	before := readAll(t, filepath.Join(dir, "secrets"))
	key1, _ := os.ReadFile(filepath.Join(dir, "synapse", "signing.key"))
	rendered := []string{"synapse/homeserver.yaml", "mas/config.yaml", "postgres/init.sql", "postgres/kybackup-role.sql"}
	configs := map[string]string{}
	for _, rel := range rendered {
		b, _ := os.ReadFile(filepath.Join(dir, rel))
		configs[rel] = string(b)
	}
	if err := os.Remove(filepath.Join(dir, "secrets", "postgres_password")); err != nil {
		t.Fatal(err)
	}
	res, err := Run(goodInput(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Created, []string{"secrets/postgres_password"}) {
		t.Errorf("created %v, want only secrets/postgres_password", res.Created)
	}
	after := readAll(t, filepath.Join(dir, "secrets"))
	for name, v := range before {
		if name != "postgres_password" && after[name] != v {
			t.Errorf("secret %s changed", name)
		}
	}
	if after["postgres_password"] == "" || after["postgres_password"] == before["postgres_password"] {
		t.Error("postgres_password not regenerated")
	}
	if key2, _ := os.ReadFile(filepath.Join(dir, "synapse", "signing.key")); string(key1) != string(key2) {
		t.Error("signing key changed")
	}
	for _, rel := range rendered {
		if b, _ := os.ReadFile(filepath.Join(dir, rel)); string(b) != configs[rel] || len(b) == 0 {
			t.Errorf("%s changed: the superuser password must not be rendered", rel)
		}
	}
}

// The app mounts ./matrix piece by piece so the superuser password stays out of its view. Every
// other secret must be mounted, or the capsule silently lacks it and a restore regenerates it.
func TestComposeMountsEverySecretButTheSuperusers(t *testing.T) {
	raw, err := os.ReadFile("../../docker-compose.matrix.yml")
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Volumes []any `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range compose.Services["app"].Volumes {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if target, _ := m["target"].(string); strings.HasPrefix(target, "/matrix/secrets/") {
			got = append(got, strings.TrimPrefix(target, "/matrix/secrets/"))
		}
	}
	want := []string{filepath.Base(ClientSecretFile)}
	for _, spec := range secretSpecs {
		if spec.name != "postgres_password" {
			want = append(want, spec.name)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("app mounts secrets %v, want %v", got, want)
	}
}

// Element's config.json reaches a running Element only through its inode: Compose binds the
// single file, and a rename is invisible to the container.
func TestElementConfigIsRewrittenInPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	el := filepath.Join(dir, "element", "config.json")
	hs := filepath.Join(dir, "synapse", "homeserver.yaml")
	elBefore, _ := os.Stat(el)
	hsBefore, _ := os.Stat(hs)
	// The console set another brand, and someone narrowed the mode.
	if err := os.WriteFile(el, []byte(`{"brand":"Acme"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(el, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	elAfter, _ := os.Stat(el)
	hsAfter, _ := os.Stat(hs)
	if !os.SameFile(elBefore, elAfter) {
		t.Fatal("element/config.json was replaced; a running Element keeps the old inode")
	}
	if elAfter.Mode().Perm() != 0o644 {
		t.Fatalf("element/config.json mode %04o, want 0644", elAfter.Mode().Perm())
	}
	b, _ := os.ReadFile(el)
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil || cfg["brand"] != "KyMessages" || cfg["default_server_config"] == nil {
		t.Fatalf("not fully re-rendered (err %v): %s", err, b)
	}
	if os.SameFile(hsBefore, hsAfter) {
		t.Fatal("other configs must still be published by rename")
	}
}

func TestElementConfigThatIsNotAFileIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	el := filepath.Join(dir, "element", "config.json")
	if err := os.Remove(el); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(el, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(goodInput(), dir); err == nil || !strings.Contains(err.Error(), "element/config.json") {
		t.Fatalf("got %v, want a refusal naming element/config.json", err)
	}
}
