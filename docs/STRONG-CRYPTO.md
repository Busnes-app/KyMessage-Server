# Key strength and the Go TLS profile

[Running](RUNNING.md) · [Security policy](../SECURITY.md) · [Assurance case](ASSURANCE-CASE.md)

This guide covers the code KyMessages produces: the Go server, CLI and `matrix-init`
(the `kymessages` binary), and the optional meeting-notes bot (`notes/`). It does not
cover the stock upstream images that run beside them; [Out of scope](#out-of-scope) lists
those. It is a key-strength configuration, not a FIPS certification.

Minimums used: 112-bit symmetric keys, RSA-2048, EC-224, SHA-224 (NIST SP 800-57 for 2030).

## Preserve existing data and keys

Keep the database, `encryption.key` (or `KY_ENCRYPTION_KEY`), `KY_SESSION_SECRET`, the
recovery public-key pin, sealed token, local capsules, custodian cards and every file under
`./matrix/secrets` and `./matrix/synapse/signing.key`. Keep the data volume and the Compose
image/build, DNS, network and overlay choices. Nothing in this guide rotates or deletes a key:
a key below its floor is refused with a message naming it, and stays as it is.

The `ky-primitives` dependency is pinned to merged source `bae5c14dba4c`
(`v0.10.2-0.20261026050236-bae5c14dba4c`) for its TOTP floor. The capsule, key and share
formats are unchanged; existing capsules restore as before.

## Key rules in every mode

| Boundary | Default | Smaller keys | Where |
| --- | --- | --- | --- |
| Deployment key | 32 random bytes in `encryption.key`, or `KY_ENCRYPTION_KEY` (hex) | Anything but exactly 32 bytes fails startup; the file is never overwritten. AES-256-GCM, purpose-derived with HMAC-SHA256 | `internal/config`, `ky-primitives/keyfile`, `internal/crypto` |
| `KY_SESSION_SECRET` (CSRF and proof-of-work HMAC-SHA256 key) | Production requires one; development generates 32 random bytes | Below 16 bytes fails startup. Use `openssl rand -hex 32`. Changing it only invalidates outstanding CSRF tokens and challenges | `internal/config` |
| Directory webhook (`KY_KYIDENTITY_HMAC_SECRET`) | KyIdentity's generated 32-byte suite secret | `ky-primitives/syncauth` refuses a key below 16 bytes on every delivery (counted `not_configured`); installer setup refuses a shorter directory secret | `internal/sso`, `internal/config/installer.go` |
| KyIdentity ID tokens | KyIdentity signs RS256 with RSA-2048 | A JWKS holding an RSA key below 2048 bits is refused, so no ID token verifies against it. Redirects on the JWKS fetch are refused. Signature, issuer, audience, expiry and nonce checks stay in `coreos/go-oidc` | `internal/sso/jwks_strength.go` |
| TOTP | 20 random bytes | Below 16 decoded bytes cannot authenticate (RFC 4226). Secrets are only generated here, never imported | `ky-primitives/totp` |
| LiveKit call grants (HS256) | `matrix-init`'s 32 random bytes, hex | Key and secret files outside 32–256 bytes fail startup; `matrix-init` refuses a kept secret below 32 bytes | `internal/config`, `internal/matrixinit` |
| MAS signing keys (rendered into MAS's config) | RSA-2048 and P-256 | A kept RSA key below 2048 bits, or one that is not an unencrypted RSA PEM, is refused before any config is rendered. Every curve MAS reads is 256 bits or more | `internal/matrixinit` |
| MAS encryption key | 32 random bytes, hex | Anything but 64 hex digits is refused | `internal/matrixinit` |
| Synapse macaroon and form secrets | 32 random bytes, hex | A kept secret below 32 bytes is refused | `internal/matrixinit` |
| Synapse signing key | Ed25519 (fixed size) | No size to choose | `internal/matrixinit` |
| TURN/TLS certificate (`KY_MATRIX_TURN_*`) | Operator-supplied | Every certificate in the supplied file must be RSA-2048+, ECDSA (P-224 or larger) or Ed25519; anything else is refused before `matrix-init` writes | `internal/matrixinit` |
| Recovery capsules | `kycap/3`: X-Wing, HKDF-SHA256, AES-256-GCM, sealed to the pinned suite key | Fixed suite | `ky-primitives/recoveryclient` |
| Other credentials | Session tokens 256-bit random, stored as SHA-256; device pairing 24 random bytes; notes-bot secret 32 random bytes (32–256 accepted) | Credentials, not cipher keys. Passwords use Argon2id (`ky-primitives/password`) | `internal/auth`, `internal/devices`, `internal/config` |

TOTP stays HMAC-SHA1 for authenticator compatibility (RFC 6238); that is an HMAC, not a
SHA-1 signature. Every other hash KyMessages computes is SHA-256.

## Enable the Go TLS profile

Use the toolchain `go.mod` selects (the Dockerfile's builder pins it). Set
`GODEBUG=fips140=on` before the server starts. Use `on`, not the diagnostic `only` mode:
Argon2id, HMAC-SHA1 TOTP and the recovery suite stay in use. Go fixes the setting at process
start, so restart after changing it. Keep any other GODEBUG entries, with `fips140=on` once.

With Compose, append `docker-compose.strong-crypto.yml` to the existing `COMPOSE_FILE` chain
in the protected `.env`, keeping its separator and every other overlay. The base file lists
the app's environment explicitly, so `GODEBUG` in `.env` alone is not forwarded; the overlay
forwards it and changes nothing else. Then `docker compose up -d`. Commands run with
`docker compose exec app kymessages ...` (`deposit`, `backup-drill`, `export-capsule`) inherit
it. A host-built `kymessages restore` needs `GODEBUG=fips140=on` in its own shell.
`restore-matrix` connects only to Postgres on the internal `matrix-db` network.
On Kubernetes, `ky-kubernetes` owns the manifest: set it in the app container's `env`.

`python3 docs/check-strong-crypto-compose.py` renders nine Compose chains (base, source
build, LAN DNS, proxy, static IP, Matrix, TURN and the notes bot) with synthetic values,
starts nothing, and requires that removing the one `GODEBUG` entry leaves the whole resolved
configuration identical.

Go then allows TLS 1.2 and 1.3 only, and filters the entire verified chain, explicitly trusted
roots included, to RSA-2048+, P-256/P-384/P-521 and Ed25519 keys. A private CA does not waive
it. `TestOIDCTrustedRootKeyFloorUnderProfile` shows the KyIdentity client refusing an
RSA-1024 and RSA-2047 root before any request, while the ordinary profile connects. The
policy covers every Go TLS client KyMessages has:

- KyIdentity discovery, JWKS and token exchange. Configure an `https` `KY_KYIDENTITY_ISSUER`:
  an `http` issuer has no TLS for the profile to filter.
- KyRecovery deposits and drills (`https` only).
- Call grants for users of other homeservers (`https` only, public addresses only).
- PostgreSQL as the app database (`KY_DB_DRIVER=postgres`): use `sslmode=verify-full`.

The MAS admin, Synapse admin and LiveKit room-service clients, the meeting-notes bot's session
route and backup `pg_dump` use plain HTTP or the Postgres protocol on the private Compose
networks (`matrix-admin`, `matrix-db`, the project network). They carry no TLS key; keep those
networks private.

No KyMessages client disables certificate or hostname verification. Never work around an
incompatible endpoint by disabling verification; replace its certificate chain.

## Meeting-notes bot

The bot is Node, not Go: `GODEBUG` does not configure it. Its supported configuration is the
one `docker-compose.notes.yml` ships, where it reaches `http://app:8080`,
`http://synapse:8008` and `ws://livekit:7880` on the private project network. It makes no TLS
connection there, so there is no TLS key to size.

Keys it holds or accepts:

- Its secret: `matrix-init`'s 32 random bytes; the app accepts 32–256 bytes.
- Matrix end-to-end keys: matrix-js-sdk's Rust crypto (vodozemac): fixed-size Curve25519 and
  Ed25519, Megolm with AES-256.
- Snapshot attachments: a fresh 32-byte AES-256-CTR key each, SHA-256 hash.
- Call media keys: its own are 16 random bytes (matrix-js-sdk). A member's key below 16 bytes
  is refused and never set, so that member's audio is not transcribed (`mediaKeyOK` in
  `notes/src/rules.mjs`); matrix-js-sdk passes on keys of any length. Frame keys are derived
  with HKDF for AES-GCM.
- Call media to LiveKit is WebRTC: DTLS and SRTP inside `@livekit/rtc-node`'s native libwebrtc,
  whose certificates and peer-key policy this project does not configure. The peer is the
  stack's own stock LiveKit (out of scope).

Pointing `NOTES_APP_URL`, `NOTES_MATRIX_URL` or `NOTES_LIVEKIT_URL` at a TLS endpoint is
outside this profile. Node's OpenSSL would apply: in the bot's image (Node 26.12.1, OpenSSL
3.5.9) it refused RSA-1024 leaf and root certificates but accepted RSA-2047 ones by default and
with `--tls-cipher-list=DEFAULT@SECLEVEL=2`; `SECLEVEL=3` refused RSA-2048 chains too. A
`wss://` LiveKit URL would use rtc-node's Rust client, which was not tested.

## Configure the independent HTTPS edge

KyMessages and the Matrix services serve private HTTP. Put them behind a TLS proxy and keep
`KY_TRUSTED_PROXIES` to the proxy's own address
([Reverse proxy networking](Reverse_Proxy_Networking.md)). Use TLS 1.2 or 1.3 and a verified
RSA-2048+ or P-256+ certificate chain. With Nginx/OpenSSL the relevant policy includes:

```nginx
ssl_protocols TLSv1.2 TLSv1.3;
ssl_ciphers 'DEFAULT@SECLEVEL=2';
```

Set the real certificate paths and routes. Verify the edge before rollout: `GODEBUG` does not
configure the proxy, cloudflared or browsers.

## Out of scope

These are stock upstream software shipped beside KyMessages, and are not claimed here. Their
key and TLS policies are their own:

- Synapse (federation and outbound HTTPS; its federation client's default minimum is TLS 1.0).
- Matrix Authentication Service (its HTTPS client and its verification of KyIdentity's ID
  tokens; MAS applies no RSA floor to them).
- Element Web and Element Call, and the browser's TLS.
- LiveKit: its TURN/TLS listener's runtime policy (KyMessages only checks the certificate it is
  given) and WebRTC DTLS. Research on 2026-10-26 found LiveKit's DTLS dependency accepting
  RSA-1024 peer certificates even with `fips140=on`; no stock LiveKit setting changes that.
- PostgreSQL, the reverse proxy, cloudflared, and the KyIdentity and KyRecovery services
  (separate projects with their own guides).

## Verification

CI's Go job runs the whole suite under the race detector twice, ordinarily and with
`GODEBUG=fips140=on`, then the Compose check. The key regressions are
`TestSessionSecretKeyFloor`, `TestIDTokenRSAKeyFloor` (RSA-1024 and 2047 ID tokens refused,
RSA-2048 signs in), `TestStrongJWKSTransportInput`, `TestOIDCTrustedRootKeyFloorUnderProfile`,
`TestInitRefusesWeakKeptKeys` (kept files unchanged, configs not re-rendered),
`TestTurnTLSRefusesWeakChainKeys` and the bot's `mediaKeyOK` test.

```sh
go test -race -count=1 ./...
GODEBUG=fips140=on go test -race -count=1 ./...
python3 docs/check-strong-crypto-compose.py
(cd notes && npm ci --ignore-scripts && npm test)
```

These are evidence for this source, not for a live deployment. After rollout, check the HTTPS
edge, sign-in through KyIdentity, existing TOTP, a `matrix-init` re-run on the existing
`./matrix` (it must keep every secret), a call, and a backup drill.

References: [NIST SP 800-57 Part 1](https://csrc.nist.gov/pubs/sp/800/57/pt1/r5/final),
[RFC 4226](https://www.rfc-editor.org/rfc/rfc4226#section-4),
[Go FIPS 140-3 mode](https://go.dev/doc/security/fips140).
