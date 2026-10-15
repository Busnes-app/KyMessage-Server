# SCIM

## Purpose
Adapts the `elimity-com/scim` RFC 7643/7644 server to the local user and group stores for enterprise provisioning from Okta, Azure AD / Microsoft Entra ID, OneLogin, and KyIdentity.

## Ownership
Owns local persistence adapters and bearer authentication; the library owns `/scim/v2/Users`, `/scim/v2/Groups`, discovery endpoints, schema validation, filter/PATCH parsing, pagination, and SCIM error serialization.

## Local Contracts
- Content-Type for all SCIM endpoints must be `application/scim+json`.
- Requests must be authenticated with the configured bearer token.
- User de-provisioning via `PATCH` with `active: false` updates user status to `inactive`.
- `GET /Users?filter=` accepts exactly one `eq` on `userName` or `emails[.value]` (case-insensitive) or `externalId` (exact `sso_subject`) and rejects anything else with `invalidFilter`. Clients link to what a lookup returns, so never widen this to substring or partial matches.
- Replace and Patch write through `UserStore.UpdateProfile`, never the whole row.
- Get, list, Replace, Patch and Delete act only on `sso_provider = 'scim'` rows (`UserFilter.Provider`); any other row answers 404 or is absent from the list. A SCIM write to a KyIdentity row would unlock or deactivate that person in Matrix behind KyIdentity's back; a read leaks another directory.
- Roles are exactly `admin` or `user`; any other value on create, replace or patch is 400 `invalidValue` and nothing is written. A PATCH `remove` of `roles` leaves `user`, of `active` leaves `inactive`: removing a grant never keeps it.
- Group members must be SCIM-provisioned users; a group create or replace naming any other ID is 400 `invalidValue` before any write (`roles_test.go`).
- SCIM protocol models and parsing must come from `github.com/elimity-com/scim`; do not add parallel local request/response implementations.

## Verification
- `go test -v ./internal/scim/...`

## Child DOX Index
None.
