# Purpose

Disposable loopback API fixture for the browser MLS integration proof.

## Ownership

Owns synthetic SSO account/session provisioning, the disposable OIDC issuer and
fixture process lifetime.

## Local Contracts

- Compile only with the `mlsproof` build tag; keep this subtree out of production.
- Use a temporary SQLite database and loopback listener. No external identity
  service, production credentials, test bypass in the real API, or persistent data.
- Serve the actual API handler. Direct fixture session provisioning is a separate
  route and is not evidence of OIDC. `MLS_PROOF_OIDC=1` disables that route and
  configures the actual suite callback against `oidc.go`'s local test issuer.
- Bearer mode exposes `POST /proof-fixture/expire-room/{room}` only on the
  disposable loopback fixture. Validate the room UUID, age its event deadlines in
  the temporary database, then run the real store cleanup for retention drills.
  OIDC mode does not register this synthetic aging route.
- Both modes expose `POST /proof-fixture/suspend-devices/{user}`, which clears the
  token hashes of that account's approved devices in the temporary database, as
  `restore-messages` does, so the proof can exercise resume.
- The fixture reissues sessions for an existing synthetic account so the chat
  prototype can unlock after reload. Anyone on the fixture can name any test
  account; this is deliberately not authentication and must remain loopback-only.
- The issuer accepts disposable names without passwords. It signs short-lived
  RS256 ID tokens, validates the fixed callback/client and S256 verifier, and
  consumes authorization codes once. Ordinary login and recovery use separate fixed
  callbacks bound into each code. `auth_time` records the explicit fixture form POST,
  never token issuance. Only OIDC fixture mode enables the reset gate. Its signing key and code records live only
  for the fixture process. Never inherit a live issuer from the host environment.
- Cancel upgraded messaging streams before HTTP shutdown and wait for detached
  handlers before closing the temporary store.
- Cookie Secure is disabled and CookieDomain cleared only for the loopback fixture.
  Production cookie configuration and auth handlers retain their normal behavior.

## Work Guidance

## Verification

- `go vet -tags=mlsproof ./mls-proof/server`
- The proof's `npm run test:delivery` starts this server and exercises browser traffic.
- `npm run test:oidc` tests the real callback and messaging API with the local issuer,
  including bad nonce/PKCE rejection and session-cookie/CSRF boundaries.

## Child DOX Index

None.
