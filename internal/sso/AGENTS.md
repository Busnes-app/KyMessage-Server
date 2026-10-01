# SSO

## Purpose
Provides unified Single Sign-On federation for KyIdentity, Generic OpenID Connect (Google, Microsoft Entra ID, Okta, Keycloak), and SAML 2.0 Service Provider (SP).

## Ownership
Owns the application adapters around OAuth/OIDC login, KyIdentity HMAC-SHA256 signed directory sync webhooks, and SAML metadata publication.

## Local Contracts
- `KyIdentityClient.HandleSyncWebhook` implements KyIdentity's suite-webhook contract: a bare SCIM 2.0 User body verified with `ky-primitives/syncauth` (event type and ID are signed headers, ±5 minutes) before any local change. Events: `user.created`, `user.updated` (`active:false` deactivates), `user.mfa_reset` and `user.deleted`; others are ignored with success. The SCIM `id` is the OIDC `sub`. A primary role of `admin` maps to admin, anything else to user. Unauthenticated is 401, a body without `id` or `meta.version` is 400.
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
