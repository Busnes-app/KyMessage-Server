package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/keyfile"
	"github.com/Busnes-app/ky_server_base/internal/matrixinit"
)

// Config encapsulates all runtime configuration for KyMessages.
type Config struct {
	Server   ServerConfig   `json:"server"`
	Database DatabaseConfig `json:"database"`
	Security SecurityConfig `json:"security"`
	SSO      SSOConfig      `json:"sso"`
	SCIM     SCIMConfig     `json:"scim"`
	Backup   BackupConfig   `json:"backup"`
	Captcha  CaptchaConfig  `json:"captcha"`
	Matrix   MatrixConfig   `json:"matrix"`
}

// MatrixConfig locates the optional Matrix stack: all set, or none. Hosts are https origins
// with no trailing slash. AdminURL reaches MAS's admin API on the internal network.
type MatrixConfig struct {
	ServerName    string `json:"server_name"`
	Host          string `json:"host"`
	ChatHost      string `json:"chat_host"`
	AdminURL      string `json:"admin_url"`
	AdminClientID string `json:"admin_client_id"`
	AdminSecret   string `json:"-"`
	// Backups: matrix-init's output and Synapse's media store, both mounted read-only, the
	// Postgres host on matrix-db, and the read-only kybackup role's password.
	Dir              string `json:"dir"`
	MediaDir         string `json:"media_dir"`
	DBHost           string `json:"db_host"`
	BackupDBPassword string `json:"-"`
}

// Enabled reports whether the Matrix stack is configured.
func (m MatrixConfig) Enabled() bool { return m.ServerName != "" }

// ServerConfig defines HTTP and network settings.
type ServerConfig struct {
	Host         string        `json:"host"`
	Port         int           `json:"port"`
	AppURL       string        `json:"app_url"`
	AppName      string        `json:"app_name"`
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	Environment  string        `json:"environment"`
}

// DatabaseConfig holds connection settings for pluggable storage (SQLite, PostgreSQL, MySQL).
type DatabaseConfig struct {
	Driver          string        `json:"driver"` // "sqlite", "postgres", "mysql"
	DSN             string        `json:"dsn"`    // Connection string or file path
	DataDir         string        `json:"data_dir"`
	MaxOpenConns    int           `json:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
}

// SecurityConfig holds encryption keys, cookie secrets, and session settings.
type SecurityConfig struct {
	SessionSecret string `json:"session_secret"`
	EncryptionKey []byte `json:"-"` // 32 bytes for AES-256-GCM; never serialised
	CookieSecure  bool   `json:"cookie_secure"`
	CookieDomain  string `json:"cookie_domain"`
	SessionTTL    time.Duration
	// TrustedProxies is the parsed KY_TRUSTED_PROXIES allowlist. Only a request whose peer
	// address falls inside it may speak for a client other than itself.
	TrustedProxies []netip.Prefix `json:"-"`
}

// SSOConfig holds identity provider and federation parameters.
type SSOConfig struct {
	Enabled              bool   `json:"enabled"`
	KyIdentityIssuer     string `json:"kyidentity_issuer"`
	KyIdentityClientID   string `json:"kyidentity_client_id"`
	KyIdentitySecret     string `json:"kyidentity_secret"`
	KyIdentityHMACSecret string `json:"kyidentity_hmac_secret"`
	GenericOIDCIssuer    string `json:"generic_oidc_issuer"`
	GenericOIDCClientID  string `json:"generic_oidc_client_id"`
	GenericOIDCSecret    string `json:"generic_oidc_secret"`
	SAMLEntityID         string `json:"saml_entity_id"`
	SAMLMetadataURL      string `json:"saml_metadata_url"`
	AutoProvision        bool   `json:"auto_provision"`
}

// SCIMConfig holds settings for RFC 7643/7644 inbound user provisioning.
type SCIMConfig struct {
	Enabled     bool   `json:"enabled"`
	BearerToken string `json:"bearer_token"`
}

// BackupConfig holds parameters for KyBackup capsules & recovery drills.
type BackupConfig struct {
	Dir string `json:"dir"`
	// Keep is how many capsule generations recoveryclient retains; it refuses values below 1.
	Keep int `json:"keep"`
	// DepositInterval is how often a paired instance seals and deposits a capsule to
	// KyRecovery. Zero disables the schedule; deposits then happen only on request.
	DepositInterval time.Duration `json:"deposit_interval"`
	// AllowPrivateRecovery admits private and CGNAT KyRecovery destinations (HTTPS still
	// required). Off by default: KyRecovery destinations must be public.
	AllowPrivateRecovery bool `json:"allow_private_recovery"`
	// MediaFullKeep is how many monthly media archives to keep (Matrix only).
	MediaFullKeep int `json:"media_full_keep"`
}

// CaptchaConfig holds anti-abuse settings for password login.
type CaptchaConfig struct {
	Provider      string `json:"provider"` // "pow" or "none"
	DifficultyPoW int    `json:"difficulty_pow"`
}

// MinDepositInterval is the shortest schedule accepted: each run snapshots the whole database
// and uploads it, and KyRecovery admits 60 deposits per token per 15 minutes.
const MinDepositInterval = 15 * time.Minute

// DefaultAppName is the service name an unconfigured instance runs under. Capsules are sealed
// under it, so the restore CLI has to agree with it without loading a whole Config.
const DefaultAppName = "KyMessages"

// AppVersion is shared by the CLI and every capsule-producing path.
const AppVersion = "0.1.0-dev"

// LoadFromEnv initializes a Config struct populated from environment variables with sensible defaults.
func LoadFromEnv() (*Config, error) {
	if err := rejectLegacyEnvironment(); err != nil {
		return nil, err
	}
	port := getEnvInt("KY_PORT", getEnvInt("PORT", 8080))
	host := getEnv("KY_HOST", "0.0.0.0")
	appURL := getEnv("KY_APP_URL", fmt.Sprintf("http://localhost:%d", port))
	appName := getEnv("KY_APP_NAME", DefaultAppName)
	env := getEnv("KY_ENV", "development")

	driver := strings.ToLower(getEnv("KY_DB_DRIVER", "sqlite"))
	dataDir := getEnv("KY_DATA_DIR", "./data")
	defaultDSN := fmt.Sprintf("%s/ky_server.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)", dataDir)
	if driver == "postgres" || driver == "postgresql" {
		driver = "postgres"
		defaultDSN = "postgres://postgres:postgres@localhost:5432/ky_server?sslmode=disable"
	}
	dsn := getEnv("KY_DB_DSN", defaultDSN)

	sessionSecret := getEnv("KY_SESSION_SECRET", "")
	if env == "production" && sessionSecret == "" {
		return nil, fmt.Errorf("KY_SESSION_SECRET is required in production")
	}
	if sessionSecret == "" {
		sessionSecret = generateRandomHex(32)
	}

	encryptionKey, ok, err := keyfile.FromEnv("KY_ENCRYPTION_KEY", 32)
	if err != nil {
		return nil, fmt.Errorf("KY_ENCRYPTION_KEY: %w", err)
	}
	if !ok {
		encryptionKey, err = keyfile.LoadOrCreate(filepath.Join(dataDir, "encryption.key"), 32)
		if err != nil {
			return nil, fmt.Errorf("encryption key: %w", err)
		}
	}

	depositInterval, err := getEnvDuration("KY_BACKUP_DEPOSIT_INTERVAL", 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("KY_BACKUP_DEPOSIT_INTERVAL: %w", err)
	}
	if depositInterval != 0 && depositInterval < MinDepositInterval {
		return nil, fmt.Errorf("KY_BACKUP_DEPOSIT_INTERVAL: %s is below the %s minimum (0 disables)", depositInterval, MinDepositInterval)
	}

	backupKeep := getEnvInt("KY_BACKUP_KEEP", 7)
	if backupKeep < 1 {
		return nil, fmt.Errorf("KY_BACKUP_KEEP: must be at least 1, got %d", backupKeep)
	}

	mediaKeep := getEnvInt("KY_BACKUP_MEDIA_FULL_KEEP", 3)
	if mediaKeep < 1 {
		return nil, fmt.Errorf("KY_BACKUP_MEDIA_FULL_KEEP: must be at least 1, got %d", mediaKeep)
	}

	// An https app URL means browsers reach it over TLS, so cookies are Secure (and HSTS sent)
	// whatever KY_ENV says. Production over plain HTTP must be an explicit choice.
	https := strings.HasPrefix(strings.ToLower(appURL), "https://")
	cookieSecure := getEnvBool("KY_COOKIE_SECURE", env == "production" || https)
	if env == "production" && !https && cookieSecure {
		return nil, fmt.Errorf("KY_APP_URL must be https in production (set KY_COOKIE_SECURE=false to serve plain HTTP deliberately)")
	}

	trustedProxies, err := ParseTrustedProxies(getEnv("KY_TRUSTED_PROXIES", ""))
	if err != nil {
		return nil, fmt.Errorf("KY_TRUSTED_PROXIES: %w", err)
	}

	matrix, err := matrixFromEnv()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Server: ServerConfig{
			Host:         host,
			Port:         port,
			AppURL:       strings.TrimRight(appURL, "/"),
			AppName:      appName,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			Environment:  env,
		},
		Database: DatabaseConfig{
			Driver:          driver,
			DSN:             dsn,
			DataDir:         dataDir,
			MaxOpenConns:    getEnvInt("KY_DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    getEnvInt("KY_DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: 15 * time.Minute,
		},
		Security: SecurityConfig{
			SessionSecret:  sessionSecret,
			EncryptionKey:  encryptionKey,
			CookieSecure:   cookieSecure,
			CookieDomain:   getEnv("KY_COOKIE_DOMAIN", ""),
			SessionTTL:     7 * 24 * time.Hour,
			TrustedProxies: trustedProxies,
		},
		SSO: SSOConfig{
			Enabled:              getEnvBool("KY_SSO_ENABLED", true),
			KyIdentityIssuer:     getEnv("KY_KYIDENTITY_ISSUER", ""),
			KyIdentityClientID:   getEnv("KY_KYIDENTITY_CLIENT_ID", ""),
			KyIdentitySecret:     getEnv("KY_KYIDENTITY_SECRET", ""),
			KyIdentityHMACSecret: getEnv("KY_KYIDENTITY_HMAC_SECRET", ""),
			GenericOIDCIssuer:    getEnv("KY_OIDC_ISSUER", ""),
			GenericOIDCClientID:  getEnv("KY_OIDC_CLIENT_ID", ""),
			GenericOIDCSecret:    getEnv("KY_OIDC_SECRET", ""),
			SAMLEntityID:         getEnv("KY_SAML_ENTITY_ID", ""),
			SAMLMetadataURL:      getEnv("KY_SAML_METADATA_URL", ""),
			AutoProvision:        getEnvBool("KY_SSO_AUTO_PROVISION", true),
		},
		SCIM: SCIMConfig{
			Enabled:     getEnvBool("KY_SCIM_ENABLED", true),
			BearerToken: getEnv("KY_SCIM_TOKEN", generateRandomHex(24)),
		},
		Backup: BackupConfig{
			Dir:                  getEnv("KY_BACKUP_DIR", ""),
			Keep:                 backupKeep,
			DepositInterval:      depositInterval,
			AllowPrivateRecovery: getEnvBool("KY_BACKUP_ALLOW_PRIVATE_RECOVERY", false),
			MediaFullKeep:        mediaKeep,
		},
		Captcha: CaptchaConfig{
			Provider:      getEnv("KY_CAPTCHA_PROVIDER", "pow"),
			DifficultyPoW: getEnvInt("KY_CAPTCHA_POW_DIFFICULTY", 50000),
		},
		Matrix: matrix,
	}
	// Unsigned directory webhooks are refused, so without the secret a KyIdentity disable or
	// delete would never reach MAS.
	if cfg.Matrix.Enabled() && cfg.SSO.KyIdentityHMACSecret == "" {
		return nil, errors.New("KY_KYIDENTITY_HMAC_SECRET is required with the Matrix stack (KyIdentity's suite_webhook secret)")
	}
	// Login verifies only proof-of-work. Accepting another name would silently disable it.
	if p := cfg.Captcha.Provider; p != "pow" && p != "none" {
		return nil, fmt.Errorf("KY_CAPTCHA_PROVIDER: %q is not supported (use pow or none)", p)
	}

	return cfg, nil
}

// matrixFromEnv validates like matrix-init. A partial block fails: serving half of it would
// send members to a homeserver nobody configured.
func matrixFromEnv() (MatrixConfig, error) {
	m := MatrixConfig{
		ServerName: getEnv("KY_MATRIX_SERVER_NAME", ""),
		Host:       getEnv("KY_MATRIX_HOST", ""),
		ChatHost:   getEnv("KY_MATRIX_CHAT_HOST", ""),

		AdminURL:      getEnv("KY_MATRIX_ADMIN_URL", ""),
		AdminClientID: getEnv("KY_MATRIX_ADMIN_CLIENT_ID", ""),

		Dir:      getEnv("KY_MATRIX_DIR", ""),
		MediaDir: getEnv("KY_MATRIX_MEDIA_DIR", ""),
		DBHost:   getEnv("KY_MATRIX_DB_HOST", ""),
	}
	secretFile := getEnv("KY_MATRIX_ADMIN_SECRET_FILE", "")
	backupPWFile := getEnv("KY_MATRIX_BACKUP_DB_PASSWORD_FILE", "")
	if m == (MatrixConfig{}) && secretFile == "" && backupPWFile == "" {
		return m, nil
	}
	if err := matrixinit.ValidServerName(m.ServerName); err != nil {
		return MatrixConfig{}, fmt.Errorf("KY_MATRIX_SERVER_NAME: %w", err)
	}
	for _, h := range []struct {
		env string
		v   *string
	}{{"KY_MATRIX_HOST", &m.Host}, {"KY_MATRIX_CHAT_HOST", &m.ChatHost}} {
		o, err := matrixinit.Origin(*h.v)
		if err != nil {
			return MatrixConfig{}, fmt.Errorf("%s: %w", h.env, err)
		}
		*h.v = o
	}
	u, err := url.Parse(m.AdminURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		// The value is not echoed: it may carry credentials.
		return MatrixConfig{}, errors.New("KY_MATRIX_ADMIN_URL must be an http(s) origin with no userinfo, path, query or fragment")
	}
	m.AdminURL = u.Scheme + "://" + u.Host
	if m.AdminClientID == "" {
		return MatrixConfig{}, errors.New("KY_MATRIX_ADMIN_CLIENT_ID is required with the Matrix stack (printed by matrix-init)")
	}
	b, err := os.ReadFile(secretFile)
	m.AdminSecret = strings.TrimSpace(string(b))
	if err != nil || m.AdminSecret == "" {
		return MatrixConfig{}, fmt.Errorf("KY_MATRIX_ADMIN_SECRET_FILE %q must name a readable, non-empty file: %v", secretFile, err)
	}
	for _, d := range []struct{ env, v string }{{"KY_MATRIX_DIR", m.Dir}, {"KY_MATRIX_MEDIA_DIR", m.MediaDir}} {
		if !filepath.IsAbs(d.v) {
			return MatrixConfig{}, fmt.Errorf("%s must be an absolute path (the Matrix overlay sets it)", d.env)
		}
	}
	if m.DBHost == "" {
		m.DBHost = "postgres"
	}
	if !dbHostRE.MatchString(m.DBHost) {
		return MatrixConfig{}, errors.New("KY_MATRIX_DB_HOST must be a host name")
	}
	pw, err := os.ReadFile(backupPWFile)
	m.BackupDBPassword = strings.TrimSpace(string(pw))
	if err != nil || m.BackupDBPassword == "" {
		return MatrixConfig{}, fmt.Errorf("KY_MATRIX_BACKUP_DB_PASSWORD_FILE %q must name a readable, non-empty file: %v", backupPWFile, err)
	}
	return m, nil
}

// dbHostRE keeps KY_MATRIX_DB_HOST a plain host name: it is passed to pg_dump as --host=.
var dbHostRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)

// rejectLegacyEnvironment refuses the pre-rename KY_KYSIGNON_* names: ignoring a set one
// would silently leave suite sign-in unconfigured.
func rejectLegacyEnvironment() error {
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		if rest, ok := strings.CutPrefix(name, "KY_KYSIGNON_"); ok && strings.TrimSpace(value) != "" {
			return fmt.Errorf("legacy environment variable %s is set; use KY_KYIDENTITY_%s instead", name, rest)
		}
	}
	return nil
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok {
		if intVal, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return intVal
		}
	}
	return defaultVal
}

// getEnvBool treats an empty value as unset, like getEnv: a compose file's `${X:-}` must not
// silently turn off a security default such as Secure cookies.
func getEnvBool(key string, defaultVal bool) bool {
	lower := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if lower == "" {
		return defaultVal
	}
	return lower == "true" || lower == "1" || lower == "yes" || lower == "on"
}

// getEnvDuration parses a Go duration such as "24h" or "90m". Negative is refused; "0" disables.
func getEnvDuration(key string, defaultVal time.Duration) (time.Duration, error) {
	val, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(val) == "" {
		return defaultVal, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(val))
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("%s is negative", val)
	}
	return d, nil
}

func generateRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ParseTrustedProxies parses a comma-separated list of IPs and CIDR blocks into prefixes.
// A bare IP becomes a single-address prefix. An unparsable entry is a startup error: a
// silently dropped proxy would make the server ignore its X-Forwarded-For and lump every
// client behind it into one rate-limit bucket.
func ParseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if strings.Contains(field, "/") {
			prefix, err := netip.ParsePrefix(field)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q: %w", field, err)
			}
			// Checked after unmapping: a mapped form like ::ffff:0.0.0.0/96 has Bits() == 96
			// here but means 0.0.0.0/0 once rewritten, and would otherwise slip the guard.
			masked := unmapPrefix(prefix).Masked()
			if masked.Bits() == 0 {
				return nil, fmt.Errorf("trusted proxy %q trusts every address; list the proxy's own address or subnet instead", field)
			}
			out = append(out, masked)
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q: %w", field, err)
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// unmapPrefix rewrites an IPv4-mapped IPv6 prefix as the plain IPv4 one it means. Peers are
// unmapped before the allowlist is consulted, and netip.Prefix.Contains never matches across
// families, so a mapped entry would otherwise match nothing at all.
func unmapPrefix(p netip.Prefix) netip.Prefix {
	if !p.Addr().Is4In6() || p.Bits() < 96 {
		return p
	}
	return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
}
