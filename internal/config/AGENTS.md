# Config

## Purpose
Manages environment and file-based configuration loading, defaults, and type conversions for KyMessages.

## Ownership
Owns environment variable parsing, configuration validation, default fallbacks, and security key generations.

## Local Contracts
- `DefaultAppName` is `KyMessages`; it is the default capsule service name as well
  as the visible product name. Keep explicit `KY_APP_NAME` overrides for already
  pinned recovery service names. `AppVersion` supplies CLI and HTTP/scheduled capsule
  paths so they cannot report different build versions.
- `LoadFromEnv() (*Config, error)` must supply safe, valid defaults for all subsystems.
- An empty boolean variable is unset and takes its default, like every other `getEnv*` helper; it never means false.
- Never log plaintext secrets or sensitive tokens.
- `KY_TRUSTED_PROXIES` is a comma-separated list of reverse-proxy IPs or CIDRs, empty by default, parsed once at startup into `[]netip.Prefix`; an unparsable entry fails startup. Only a request whose peer address is in the list may speak for another client through `X-Forwarded-For`. `0.0.0.0/0` and `::/0` are refused at startup; list only the proxy's own address or subnet.
- `KY_COOKIE_SECURE` (which also enables HSTS) defaults to true when `KY_ENV=production` or `KY_APP_URL` is https. Production with a non-https app URL fails startup unless `KY_COOKIE_SECURE=false` is set explicitly.
- Production startup requires an explicit, durable `KY_SESSION_SECRET`. The encryption key comes from `KY_ENCRYPTION_KEY` when set, otherwise from the keyfile at `<DataDir>/encryption.key`, which `keyfile.LoadOrCreate` mints on first start; either is a valid production configuration.

- `KY_BACKUP_DEPOSIT_INTERVAL` is a Go duration (default `24h`), only the default for the schedule the admin screen stores; `0` is off, anything else below `MinDepositInterval` (15m) or negative fails startup. `KY_BACKUP_DIR` (default empty, off) is the sealed local-copy directory and `KY_BACKUP_KEEP` (default 7) how many to retain; below 1 fails startup because the lib refuses it at write time. `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` (default false) admits RFC1918 and CGNAT KyRecovery destinations only.

- Suite sign-in reads `KY_KYIDENTITY_ISSUER`, `KY_KYIDENTITY_CLIENT_ID`, `KY_KYIDENTITY_SECRET` and `KY_KYIDENTITY_HMAC_SECRET`. Any nonempty `KY_KYSIGNON_*` fails startup naming its replacement, so a secret under the old name cannot leave sign-in silently unconfigured.

- `KY_CAPTCHA_PROVIDER` is `pow` (default) or `none`; anything else fails startup, because login verifies nothing else. `KY_CAPTCHA_POW_DIFFICULTY` defaults to 50000.

- `KY_MESSAGING_IDENTITY_RESET_ENABLED` defaults false. Enable only after deployed suite issuer fresh-interaction and callback verification; both initiation and completion enforce the gate.

## Verification
- `go test -v ./internal/config/...`
- `go test -v ./internal/auth/ -run TestClientIP` (the helper that consumes the allowlist)

## Child DOX Index
None.
