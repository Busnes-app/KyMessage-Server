package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Busnes-app/ky-primitives/applysetup"
)

// InstallerSocket is private IPC; it is never mounted on the public HTTP router.
func InstallerSocket(dataDir string) (string, error) {
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", errors.New("invalid installer data directory")
	}
	return filepath.Join(dir, "installer", "setup.sock"), nil
}

// ApplyInstallerConfiguration narrows an already validated setup into runtime
// config. Nonempty operator SSO settings are immutable to the installer.
func ApplyInstallerConfiguration(cfg *Config, b applysetup.Common) error {
	if err := b.Validate("kymessage", "/api/sso/kyidentity/callback"); err != nil {
		return err
	}
	if b.SSO == nil || len(b.Admins) == 0 {
		return errors.New("setup requires KyIdentity and a dedicated console administrator")
	}
	if s := b.SSO; s != nil {
		if strings.TrimRight(s.IssuerURL, "/") != s.IssuerURL {
			return errors.New("issuer must be canonical")
		}
		if cfg.SCIM.Enabled && (os.Getenv("KY_SCIM_TOKEN") != "" || os.Getenv("KY_SCIM_ENABLED") != "") {
			return errors.New("installer refuses existing SCIM configuration")
		}
		cfg.SCIM.Enabled = false
		cfg.SCIM.BearerToken = ""
		if s.RedirectURI != cfg.Server.AppURL+"/api/sso/kyidentity/callback" {
			return errors.New("installer callback does not match the configured product URL")
		}
		if s.SCIMTokenFile != "" || s.DirectorySecretFile == "" {
			return errors.New("KyMessages setup requires the protected KyIdentity directory webhook secret")
		}
		secret, err := applysetup.Secret(s.ClientSecretFile)
		if err != nil {
			return err
		}
		directorySecret, err := applysetup.Secret(s.DirectorySecretFile)
		if err != nil {
			return err
		}
		if cfg.SSO.AutoProvision && (cfg.SSO.KyIdentityIssuer != "" || os.Getenv("KY_SSO_AUTO_PROVISION") != "") || cfg.SSO.GenericOIDCIssuer != "" || cfg.SSO.SAMLMetadataURL != "" {
			return errors.New("installer refuses conflicting authentication or automatic provisioning")
		}
		for _, pair := range [][2]string{{cfg.SSO.KyIdentityIssuer, s.IssuerURL}, {cfg.SSO.KyIdentityClientID, s.ClientID}, {cfg.SSO.KyIdentitySecret, secret}, {cfg.SSO.KyIdentityHMACSecret, directorySecret}} {
			if pair[0] != "" && pair[0] != pair[1] {
				return errors.New("installer refuses to replace configured SSO values")
			}
		}
		if len(directorySecret) < 16 {
			return errors.New("directory secret must have at least 16 bytes")
		}
		owned := *s
		cfg.InstallerSSO = &owned
		cfg.SSO.Installer = &owned
		cfg.SSO.AutoProvision = false
		cfg.SSO.Enabled = true
		cfg.SSO.KyIdentityIssuer = s.IssuerURL
		cfg.SSO.KyIdentityClientID = s.ClientID
		cfg.SSO.KyIdentitySecret = secret
		cfg.SSO.KyIdentityHMACSecret = directorySecret
	}
	if (len(b.Admins) > 0 || len(b.Everyday) > 0) && (cfg.SSO.KyIdentityIssuer == "" || cfg.SSO.KyIdentityHMACSecret == "") {
		return errors.New("directory identities require configured KyIdentity and directory webhook")
	}
	for _, list := range [][]applysetup.Identity{b.Admins, b.Everyday} {
		for _, id := range list {
			if !ValidUsername(id.Username) {
				return errors.New("identity username is invalid")
			}
			if id.Issuer != cfg.SSO.KyIdentityIssuer {
				return errors.New("identity issuer differs from active KyIdentity")
			}
		}
	}
	if backup := b.Backup; backup != nil {
		if backup.Recovery != nil {
			return errors.New("installer recovery pairing is not yet supported")
		}
		if backup.Directory != "" {
			if cfg.Backup.Dir != "" && cfg.Backup.Dir != backup.Directory {
				return errors.New("installer refuses to replace configured backup directory")
			}
			cfg.Backup.Dir = backup.Directory
		}
		if backup.Keep != 0 {
			if os.Getenv("KY_BACKUP_KEEP") != "" && cfg.Backup.Keep != backup.Keep {
				return errors.New("installer refuses to replace configured retention")
			}
			cfg.Backup.Keep = backup.Keep
		}
		if backup.Interval != "" {
			d, _ := applysetup.Interval(backup.Interval)
			if os.Getenv("KY_BACKUP_DEPOSIT_INTERVAL") != "" && cfg.Backup.DepositInterval != d {
				return errors.New("installer refuses to replace configured backup interval")
			}
			cfg.Backup.DepositInterval = d
		}
	}
	return nil
}
