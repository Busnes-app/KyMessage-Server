# SSO

## Purpose
Provides KyIdentity sign-in (OpenID Connect) and KyIdentity's signed directory sync webhooks. There is no generic OIDC or SAML provider.

## Ownership
Owns the application adapters around KyIdentity OAuth/OIDC login and the HMAC-SHA256 signed directory sync webhooks. `syncstatus.go` owns the webhook record and the rejection counters.

## Local Contracts
- `KyIdentityClient.HandleSyncWebhook` implements KyIdentity's suite-webhook contract: a bare SCIM 2.0 User body verified with `ky-primitives/syncauth` (event type and ID are signed headers, ±5 minutes) before any local change. Events: `user.created`, `user.updated` (`active:false` deactivates), `user.mfa_reset` and `user.deleted`; others are ignored with success. The SCIM `id` is the OIDC `sub`. A primary role of `admin` maps to admin, anything else to user. Unauthenticated is 401, a body without `id` or `meta.version` is 400.
- An acknowledged delivery (applied, superseded or duplicate; the same set that wakes the sweep) writes `kyidentity_webhook_last` (`WebhookRecordKey`, `WebhookRecord{at, kind}`) on a detached 5 s context. `syncstatus.go` is its only writer; a failed write is logged, never a failed delivery. Refused deliveries (`ErrSyncUnauthorized`, `ErrSyncMalformed`, `ErrSyncUsernameConflict`) are counted in memory only (`Rejections()`: count since start, last time, last reason); bodies are never kept. Reasons: `not_configured` (secret unset or shorter than `syncauth.MinKeyBytes`), `bad_signature` (missing or not matching), `stale` (outside ±5 minutes), `bad_headers` (event type, ID or timestamp missing or unparseable), `malformed` (signed, but not a usable SCIM user), `username_conflict` (signed, but a `user.created` whose username another account holds).
- A username another account holds (`store.ErrUsernameTaken`, exact case, including a write that lost a race) is never merged into or alters that account, and is logged once and audited `sso.sync_conflict` (resource the username, details `subject="..."`, plus `kept="..."` on an update). Other refusals reach neither the database nor the audit log.
  - On `user.created` it is `ErrSyncUsernameConflict` naming the username: 409, counted as `username_conflict`, nothing written (the event stays unspent). KyIdentity takes a 409 on a create as delivered and never resends it, so recovery is manual: rename the local account (`KY_ADMIN_USERNAME` only applies to an empty database) or change the KyIdentity user, then press Resync Directory on the system in KyIdentity, which sends the user as a new event.
  - On `user.updated` only the rename is refused: the user keeps its stored username and everything else (status, role, email, name) applies, sessions are revoked on a status or role change, and the delivery is acknowledged. A deactivation or downgrade carried with a clashing rename can never be dropped, and a 409 here would be retried and then marked failed by KyIdentity, losing it. `ErrSyncUnauthorized` wraps its cause with `%w`, so reasons are told apart with `errors.Is`. A delivery that fails on the database (500) is neither recorded nor counted.
- `SuiteDirectory` builds the `ky-primitives/suitedir` client on the effective KyIdentity settings (an installer setup's secret file read each call, as sign-in does); nil when SSO is off or has no issuer.
- PKCE with `S256` is enforced on all OAuth/OIDC authorization requests.
- ID tokens require provider signature, issuer, audience, expiry, and one-time nonce verification before claims are trusted.
- The verifier's JWKS client (`strongJWKSTransport`, built once with the provider) refuses a key set holding an RSA key below 2048 bits, reading JWK members case-sensitively as go-jose does, and refuses JWKS redirects (`TestIDTokenRSAKeyFloor`, `TestStrongJWKSTransportInput`). `TestOIDCTrustedRootKeyFloorUnderProfile` covers Go's TLS chain floor under `GODEBUG=fips140=on` (`docs/STRONG-CRYPTO.md`).
- OAuth discovery, authorization URLs, PKCE parameters, code exchange, and token verification are delegated to `golang.org/x/oauth2` and `coreos/go-oidc`; application code only maps verified claims.
- Login does not require `auth_time`, but a plausible signed one (positive, not after `iat`
  or now) becomes `IdentityClaims.AuthenticatedAt`, the session's credential time for
  step-up; the callback time never stands in for it. `BuildAuthURL(..., fresh)` adds
  `prompt=login` and `max_age=0`.
- Every IdP call (discovery, keys, code exchange) uses a client with `idpTimeout` (10 s) unless the caller's context carries one (`oidc.ClientContext`, tests); discovery also runs under that deadline, so a hung IdP cannot queue sign-ins behind the flow's discovery mutex (`TestDiscoveryAgainstAHungIdPTimesOut`).
- Directory events are ordered by KyIdentity's per-user revision (`meta.version` `W/"n"`), never by timestamp or arrival. Updates apply one at a time and only through `ApplyDirectoryProfile`/`CreateDirectoryUser`/`DeleteDirectoryUser`, which apply only a revision newer than the subject's persisted one; equal or older revisions (outbox retries, delayed deliveries) succeed without effect, across restarts and after deletion. Deletions of subjects with no local account still record their revision. `-1` is KyIdentity's post-restore resend and resets the order. Because a reset lowers the stored revision, every delivered event ID is also spent permanently in `directory_sync_events`, applied or refused, atomically with the write; a redelivered event is acknowledged without effect even across a reset. Status or role changes revoke the user's sessions.

- Managed setup requires verified issuer/sub ownership and reads the client credential
  from its protected reference per flow. Native global roles, email and usernames do not
  authorize managed account creation or administration.
- In managed mode the suite sync route uses the installer directory handler: positive
  canonical revisions, exact duplicate digest acceptance, stale/conflicting refusal and
  app claim `kymessage.admin`. Native unmanaged webhook behavior remains independent.
  A subject setup does not own (`store.ErrNotInstallerOwned`) is a member: the handler passes
  the verified event to `ApplyMemberEvent`, the native path above with role always `user`
  (roles ignored), recorded and counted alike; this is the only way a KyIdentity row is
  created under setup. Members never get a console session (sign-in admits owned identities only).
  Members fail closed (`applyMember`): `user.deactivated` is an inactive update; `user.deleted`
  keeps the row as `deleted` and no later event reactivates it; a field an event omits keeps
  its stored value (a new member without a username takes its subject); any other event type
  is `ErrMemberRefused`. Equal revisions with another body are acknowledged without effect, as natively.

## Verification
- `go test -v ./internal/sso/...`

## Child DOX Index
None.
