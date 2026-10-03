# SSO

## Purpose
Provides unified Single Sign-On federation for KyIdentity, Generic OpenID Connect (Google, Microsoft Entra ID, Okta, Keycloak), and SAML 2.0 Service Provider (SP).

## Ownership
Owns the application adapters around OAuth/OIDC login, KyIdentity HMAC-SHA256 signed directory sync webhooks, and SAML metadata publication. `syncstatus.go` owns the webhook record and the rejection counters.

## Local Contracts
- `KyIdentityClient.HandleSyncWebhook` implements KyIdentity's suite-webhook contract: a bare SCIM 2.0 User body verified with `ky-primitives/syncauth` (event type and ID are signed headers, ±5 minutes) before any local change. Events: `user.created`, `user.updated` (`active:false` deactivates), `user.mfa_reset` and `user.deleted`; others are ignored with success. The SCIM `id` is the OIDC `sub`. A primary role of `admin` maps to admin, anything else to user. Unauthenticated is 401, a body without `id` or `meta.version` is 400.
- An acknowledged delivery (applied, superseded or duplicate; the same set that wakes the sweep) writes `kyidentity_webhook_last` (`WebhookRecordKey`, `WebhookRecord{at, kind}`) on a detached 5 s context. `syncstatus.go` is its only writer; a failed write is logged, never a failed delivery. Refused deliveries (`ErrSyncUnauthorized`, `ErrSyncMalformed`, `ErrSyncUsernameConflict`) are counted in memory only (`Rejections()`: count since start, last time, last reason); bodies are never kept. Reasons: `not_configured` (secret unset or shorter than `syncauth.MinKeyBytes`), `bad_signature` (missing or not matching), `stale` (outside ±5 minutes), `bad_headers` (event type, ID or timestamp missing or unparseable), `malformed` (signed, but not a usable SCIM user), `username_conflict` (signed, but a `user.created` whose username another account holds).
- A username another account holds (`store.ErrUsernameTaken`, exact case, including a write that lost a race) is never merged into or alters that account, and is logged once and audited `sso.sync_conflict` (resource the username, details `subject="..."`, plus `kept="..."` on an update). Other refusals reach neither the database nor the audit log.
  - On `user.created` it is `ErrSyncUsernameConflict` naming the username: 409, counted as `username_conflict`, nothing written (the event stays unspent). KyIdentity takes a 409 on a create as delivered and never resends it, so recovery is manual: rename the local account (`KY_ADMIN_USERNAME` only applies to an empty database) or change the KyIdentity user, then press Resync Directory on the system in KyIdentity, which sends the user as a new event.
  - On `user.updated` only the rename is refused: the user keeps its stored username and everything else (status, role, email, name) applies, sessions are revoked on a status or role change, and the delivery is acknowledged. A deactivation or downgrade carried with a clashing rename can never be dropped, and a 409 here would be retried and then marked failed by KyIdentity, losing it. `ErrSyncUnauthorized` wraps its cause with `%w`, so reasons are told apart with `errors.Is`. A delivery that fails on the database (500) is neither recorded nor counted.
- PKCE with `S256` is enforced on all OAuth/OIDC authorization requests.
- ID tokens require provider signature, issuer, audience, expiry, and one-time nonce verification before claims are trusted.
- OAuth discovery, authorization URLs, PKCE parameters, code exchange, and token verification are delegated to `golang.org/x/oauth2` and `coreos/go-oidc`; application code only maps verified claims.
- Login does not require `auth_time`, but a plausible signed one (positive, not after `iat`
  or now) becomes `IdentityClaims.AuthenticatedAt`, the session's credential time for
  step-up; the callback time never stands in for it. `BuildAuthURL(..., fresh)` adds
  `prompt=login` and `max_age=0`.
- SAML assertion parsing is not implemented locally; metadata XML uses `encoding/xml` and no ACS route is exposed until a maintained SAML service-provider library is configured.
- Directory events are ordered by KyIdentity's per-user revision (`meta.version` `W/"n"`), never by timestamp or arrival. Updates apply one at a time and only through `ApplyDirectoryProfile`/`CreateDirectoryUser`/`DeleteDirectoryUser`, which apply only a revision newer than the subject's persisted one; equal or older revisions (outbox retries, delayed deliveries) succeed without effect, across restarts and after deletion. Deletions of subjects with no local account still record their revision. `-1` is KyIdentity's post-restore resend and resets the order. Because a reset lowers the stored revision, every delivered event ID is also spent permanently in `directory_sync_events`, applied or refused, atomically with the write; a redelivered event is acknowledged without effect even across a reset. Status or role changes revoke the user's sessions.

## Verification
- `go test -v ./internal/sso/...`

## Child DOX Index
None.
