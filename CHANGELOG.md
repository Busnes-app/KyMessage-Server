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

### Fixed
- `kymessages restore` turns scheduled backups off. Turn them back on in Backup once the
  restored server is the one in service.
- People whose KyIdentity sign-in adopts their SCIM account are no longer locked out of chat.
