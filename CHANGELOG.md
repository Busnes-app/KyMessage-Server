# Changelog

## Unreleased

### Removed
- Local two-factor sign-in (TOTP and recovery codes) for the console's local administrator.
  Local admins now sign in with a password only; use KyIdentity for a second factor. An
  account that had two-factor on signs in with its password, and nobody is locked out. The
  stored two-factor data stays in the database, unused. The `/api/auth/totp/*` and
  `/api/auth/mfa/*` routes, the login's `mfa_required` step and Settings > Account security
  are gone.

### Changed
- A SCIM account with the admin role signs in to the console through KyIdentity only when
  `KY_SCIM_ALLOW_ADMIN=true`, and SCIM turning an inactive admin back on needs it too. An
  empty `roles` from SCIM now removes the admin role.
- KyIdentity's role for KyMessages applies at each single sign-on, not only when its directory
  update arrives: a demoted admin is a member from their next sign-in, and their other
  sessions end. Only an explicit KyMessages `admin` app role grants administration: empty or
  missing `roles` means ordinary user, even for a global admin or an existing console admin.
  Before upgrading, assign the KyMessages app role to intended administrators in KyIdentity,
  deploy KyIdentity's app-specific token and webhook role source, then upgrade KyMessages.
  Installer-owned identities and SCIM-owned roles keep their existing ownership rules.
  See [the upgrade sequence](docs/RUNNING.md#kyidentity-administrator-upgrade).
- A refused single sign-on returns to the sign-in page with a sentence saying why and a
  "Try again" link, instead of a raw error page.

### Fixed
- `kymessages restore` turns scheduled backups off. Turn them back on in Backup once the
  restored server is the one in service.
- People whose KyIdentity sign-in adopts their SCIM account are no longer locked out of chat.
- Deactivating such a person in KyIdentity now locks them out of chat and calls too, and
  KyIdentity no longer reports a username conflict for them. They can also own agents.
- A KyIdentity directory update makes someone admin when `admin` is any of their KyMessages
  roles, not only the first.
- Meeting notes accept a second meeting while one runs (the limit counted the invite itself).
- A SCIM rename onto a username another account holds answers 409, not a 500 with database text.
