# SSO

## Purpose
Provides unified Single Sign-On federation for KySignOn, Generic OpenID Connect (Google, Microsoft Entra ID, Okta, Keycloak), and SAML 2.0 Service Provider (SP).

## Ownership
Owns the application adapters around OAuth/OIDC login, KySignOn HMAC-SHA256 signed directory sync webhooks, and SAML metadata publication.

## Local Contracts
- `KySignOnClient.HandleSyncWebhook` verifies HMAC-SHA256 signatures before modifying local user state.
- PKCE with `S256` is enforced on all OAuth/OIDC authorization requests.
- ID tokens require provider signature, issuer, audience, expiry, and one-time nonce verification before claims are trusted.
- OAuth discovery, authorization URLs, PKCE parameters, code exchange, and token verification are delegated to `golang.org/x/oauth2` and `coreos/go-oidc`; application code only maps verified claims.
- Suite reauthentication uses `BuildReauthenticationURL` (`prompt=login`, `max_age=0`)
  and `ExchangeReauthenticationCode`: same subject/state/nonce, signed integer
  `auth_time` at or after request start, not after token issuance or local now, and
  a request younger than five minutes. No `iat` fallback or clock-skew allowance;
  comparisons use Unix seconds. Ordinary login does not require `auth_time`, but a plausible
  signed one (positive, not after `iat` or now) becomes `IdentityClaims.AuthenticatedAt`, the
  session's credential time for step-up; the callback time never stands in for it.
  `BuildAuthURL(..., fresh)` adds `prompt=login` and `max_age=0`.
- `ReauthenticationRequest` is server-owned state. The messaging recovery-auth API
  seals it, binds it to the originating live session, browser binder cookie and pending device/registry,
  and binds the current identity generation. After verification, the store atomically
  consumes it; an explicitly confirmed, default-off identity reset also increments
  the generation and revokes prior devices and memberships. Verification without
  reset intent only consumes and audits authentication; it grants no reset or approval.
- SAML assertion parsing is not implemented locally; metadata XML uses `encoding/xml` and no ACS route is exposed until a maintained SAML service-provider library is configured.
- Directory webhook timestamps are accepted only within five minutes; status or role changes revoke the user's sessions. Updates apply one at a time and only through `ApplyDirectoryProfile`/`CreateDirectoryUser`/`DeleteDirectoryUser`, which compare against the subject's persisted order record: an update older than the last applied one succeeds without effect (so the sender stops retrying), across restarts and after deletion. Deactivations and deletions for subjects with no local account still record their order. Creation never wins a tie. Timestamps have second resolution and carry no revision, so an update tied with the last applied one may only lower privilege (never grant admin or reactivate); a same-second promotion waits for a newer update.

## Verification
- `go test -v ./internal/sso/...`

## Child DOX Index
None.
