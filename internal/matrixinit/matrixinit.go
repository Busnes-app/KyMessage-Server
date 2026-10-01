// Package matrixinit renders the Synapse, MAS, Element and Postgres-init configuration for a
// KyMessages Matrix stack. Secrets are generated once and never overwritten; everything else
// is re-rendered on each run.
package matrixinit

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"q":    quote,
	"sqlq": func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" },
}).ParseFS(templateFS, "templates/*"))

// MAS claims templates (minijinja). The localpart keeps [a-z0-9._=-] and maps every other
// character to "_"; a missing claim renders empty, which "require" refuses.
const (
	localpartTemplate   = `{% for c in user.preferred_username | lower %}{% if c in "abcdefghijklmnopqrstuvwxyz0123456789._=-" %}{{ c }}{% else %}_{% endif %}{% endfor %}`
	displayNameTemplate = `{{ user.name }}`
	emailTemplate       = `{{ user.email }}`
)

var serverNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Input is what an operator supplies. Hosts are https origins with no path.
type Input struct {
	ServerName, MatrixHost, AuthHost, ChatHost, AdminHost string
	Issuer, ClientID, ClientSecret                        string
}

// Registration is what the operator enters in KyIdentity for the MAS client.
type Registration struct {
	RedirectURI string
	Scopes      []string
	ClientType  string
}

// Result lists paths relative to Dir: Created and Kept are write-once secrets, Rendered are
// configs rewritten on every run.
type Result struct {
	Dir          string
	Created      []string
	Kept         []string
	Rendered     []string
	Registration Registration
}

// InputFromEnv reads the KY_* variables and validates them.
func InputFromEnv(getenv func(string) string) (Input, error) {
	var in Input
	for _, f := range []struct {
		env string
		dst *string
	}{
		{"KY_MATRIX_SERVER_NAME", &in.ServerName}, {"KY_MATRIX_HOST", &in.MatrixHost},
		{"KY_MATRIX_AUTH_HOST", &in.AuthHost}, {"KY_MATRIX_CHAT_HOST", &in.ChatHost},
		{"KY_ADMIN_HOST", &in.AdminHost}, {"KY_KYIDENTITY_ISSUER", &in.Issuer},
		{"KY_MATRIX_MAS_CLIENT_ID", &in.ClientID}, {"KY_MATRIX_MAS_CLIENT_SECRET", &in.ClientSecret},
	} {
		if *f.dst = getenv(f.env); *f.dst == "" {
			return Input{}, fmt.Errorf("%s is required", f.env)
		}
	}
	return in.validate()
}

// validate returns in with host origins normalised (no trailing slash).
func (in Input) validate() (Input, error) {
	if len(in.ServerName) > 253 || !serverNameRE.MatchString(in.ServerName) {
		return Input{}, fmt.Errorf("server name %q is not a lowercase DNS name", in.ServerName)
	}
	for _, h := range []struct {
		name string
		v    *string
	}{{"matrix host", &in.MatrixHost}, {"auth host", &in.AuthHost}, {"chat host", &in.ChatHost}, {"admin host", &in.AdminHost}} {
		u, err := httpsURL(*h.v)
		if err != nil {
			return Input{}, fmt.Errorf("%s: %w", h.name, err)
		}
		if u.Path != "" && u.Path != "/" {
			return Input{}, fmt.Errorf("%s %q must have no path", h.name, *h.v)
		}
		*h.v = "https://" + u.Host
	}
	// The issuer may carry a path and must match what KyIdentity advertises byte for byte.
	if _, err := httpsURL(in.Issuer); err != nil {
		return Input{}, fmt.Errorf("issuer: %w", err)
	}
	for name, v := range map[string]string{"client ID": in.ClientID, "client secret": in.ClientSecret} {
		if v == "" || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return Input{}, fmt.Errorf("%s must be non-empty with no control characters", name)
		}
	}
	return in, nil
}

func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || strings.ContainsAny(raw, "?#") {
		return nil, fmt.Errorf("%q must be an https URL with no credentials, query or fragment", raw)
	}
	return u, nil
}

// secretSpecs are written once under secrets/. The provider ID is not secret but must be
// just as stable: KyIdentity's redirect URI embeds it.
var secretSpecs = []struct {
	name string
	gen  func() ([]byte, error)
}{
	{"synapse_db_password", hexSecret},
	{"mas_db_password", hexSecret},
	{"synapse_macaroon_secret_key", hexSecret},
	{"synapse_form_secret", hexSecret},
	{"mas_synapse_shared_secret", hexSecret},
	{"mas_encryption", hexSecret}, // MAS requires exactly 32 bytes, hex encoded
	{"mas_signing_rsa", rsaKeyPEM},
	{"mas_signing_ec", ecKeyPEM},
	{"upstream_provider_id", newULID},
}

// Run validates in fully, then writes the configuration under dir.
func Run(in Input, dir string) (Result, error) {
	in, err := in.validate()
	if err != nil {
		return Result{}, err
	}
	res := Result{Dir: dir}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, err
	}
	for _, sub := range []string{".", "secrets", "synapse", "mas", "element", "postgres"} {
		p := filepath.Join(dir, sub)
		if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return Result{}, err
		}
		if err := os.Chmod(p, 0o700); err != nil {
			return Result{}, err
		}
	}

	s := map[string]string{}
	ensure := func(rel string, gen func() ([]byte, error)) (string, error) {
		v, created, err := ensureFile(filepath.Join(dir, rel), gen)
		if err != nil {
			return "", fmt.Errorf("%s: %w", rel, err)
		}
		if created {
			res.Created = append(res.Created, rel)
		} else {
			res.Kept = append(res.Kept, rel)
		}
		return v, nil
	}
	for _, spec := range secretSpecs {
		if s[spec.name], err = ensure(filepath.Join("secrets", spec.name), spec.gen); err != nil {
			return Result{}, err
		}
	}
	if _, err := ensure(filepath.Join("synapse", "signing.key"), synapseSigningKey); err != nil {
		return Result{}, err
	}

	data := map[string]any{"In": in, "S": s,
		"Localpart": localpartTemplate, "DisplayName": displayNameTemplate, "Email": emailTemplate}
	for _, r := range []struct {
		tmpl, rel string
		mode      os.FileMode
	}{
		{"homeserver.yaml.tmpl", "synapse/homeserver.yaml", 0o600},
		{"mas.yaml.tmpl", "mas/config.yaml", 0o600},
		{"pg-init.sql.tmpl", "postgres/init.sql", 0o600},
		// No secrets; the Element container reads it as a different user.
		{"element.json.tmpl", "element/config.json", 0o644},
	} {
		var b bytes.Buffer
		if err := templates.ExecuteTemplate(&b, r.tmpl, data); err != nil {
			return Result{}, err
		}
		if err := replaceFile(filepath.Join(dir, r.rel), b.Bytes(), r.mode); err != nil {
			return Result{}, fmt.Errorf("%s: %w", r.rel, err)
		}
		res.Rendered = append(res.Rendered, r.rel)
	}

	res.Registration = Registration{
		RedirectURI: in.AuthHost + "/upstream/callback/" + s["upstream_provider_id"],
		Scopes:      []string{"openid", "profile", "email"},
		ClientType:  "confidential",
	}
	return res, nil
}

// ensureFile returns the existing content of path, or generates it and publishes it with a
// hard link, which fails rather than overwrite and never exposes a half-written file.
func ensureFile(path string, gen func() ([]byte, error)) (string, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(bytes.TrimSpace(b)) == 0 {
			return "", false, errors.New("exists but is empty; restore it from backup (deleting it rotates the secret)")
		}
		return string(b), false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	v, err := gen()
	if err != nil {
		return "", false, err
	}
	tmp, err := writeTemp(path, v, 0o600)
	if err != nil {
		return "", false, err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil {
		return "", false, err
	}
	return string(v), true, nil
}

func replaceFile(path string, b []byte, mode os.FileMode) error {
	tmp, err := writeTemp(path, b, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func writeTemp(path string, b []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return "", err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// quote renders s as a JSON string, which is also a valid YAML double-quoted scalar.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func hexSecret() ([]byte, error) {
	b := make([]byte, 32)
	rand.Read(b)
	return []byte(hex.EncodeToString(b)), nil
}

// synapseSigningKey uses Synapse's "ed25519 <key id> <unpadded base64 seed>" format.
func synapseSigningKey() ([]byte, error) {
	id := make([]byte, 2)
	seed := make([]byte, 32)
	rand.Read(id)
	rand.Read(seed)
	return fmt.Appendf(nil, "ed25519 a_%s %s\n", hex.EncodeToString(id), base64.RawStdEncoding.EncodeToString(seed)), nil
}

func rsaKeyPEM() ([]byte, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), nil
}

func ecKeyPEM() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// newULID returns a 26-character Crockford base32 ULID: 48-bit millisecond time, 80 random bits.
func newULID() ([]byte, error) {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	for i := range 6 {
		b[i] = byte(ms >> (40 - 8*i))
	}
	rand.Read(b[6:])
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	n := new(big.Int).SetBytes(b[:])
	out := make([]byte, 26)
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[n.Uint64()&31]
		n.Rsh(n, 5)
	}
	return out, nil
}
