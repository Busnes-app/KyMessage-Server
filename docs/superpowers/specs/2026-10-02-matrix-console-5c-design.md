# Sub-project 5c: console — settings

Date: 2026-10-02. Parent: `2026-10-01-matrix-platform-design.md` (decisions 10, 11), split in
`2026-10-02-matrix-console-5a-design.md`. Builds on 5a (step-up, Overview) and 5b.

## Intent

A KyMessages admin renames the product and replaces its logo from the console, and both reach
Element without a shell or a restart. The admin also sees whether KyIdentity's offboarding
sync is working: when the last webhook arrived, whether deliveries are being rejected, and how
the last sweep went, with a hint naming the fix.

## Decisions (owner-approved 2026-10-02)

1. Branding is the name and a logo. Busnes Light/Dark stay as shipped; no colour editing.
2. The app writes Element's `config.json` itself (option A): only the `brand` key, in place,
   through a read-write bind of that one file. A compromised app could repoint Element; it
   already holds the MAS admin secret, so this adds little.
3. No restart flow: name and logo take effect on the next page load.

## Evidence

- `internal/matrixinit` renders `element/config.json` from `element.json.tmpl` on every run
  with `replaceFile` (temp file plus rename); `brand` is hard-coded `"KyMessages"`; the sign-in
  logo is `<AdminHost>/app-icon.png`, served by KyMessages.
- `docker-compose.matrix.yml` binds `./matrix/element/config.json` alone into Element at
  `/app/config.json`. A single-file bind keeps the inode it was given, so a rename is invisible
  to a running Element: rewrites must be in place.
- Element fetches `/config.json` on each page load. The app has no Docker access.
- The Element image's start script (`18-load-element-modules.sh`) copies `/app/config.json` to
  `/tmp/element-web-config`, and nginx serves that copy, which a later write never reaches.
  Compose therefore binds the file at `/tmp/element-web-config/config.json` and masks the
  script with `/dev/null` (Element modules are unused); the acceptance run proves the rename
  reaches Element live.
- Nothing records webhook or sweep times today; rejected webhooks are only logged.

## Section 1: branding

- **Settings.** `brand_name` (1–64 characters after trimming, no control characters) and
  `brand_logo` (normalised PNG) in the existing settings table; both travel in the capsule.
- **Effective name** = `brand_name` if set, else `KY_APP_NAME`. `GET /api/settings` returns it as
  `app_name`; the console header, login page and Element `brand` use it.
- **Element.** The app reconciles `brand` in `matrix/element/config.json` to the effective name
  when Matrix is enabled: at startup, after each save and on each maintenance tick (every minute).
  It changes only that key, preserves the others, writes only when the value differs, and
  writes in place (same inode). `matrix-init` also writes this file in place, so its re-run
  reaches a running Element too; the next tick restores the effective name.
- **Logo.** Upload: `image/png` only (SVG can carry script), body at most 1 MiB, dimensions at
  most 1024×1024 checked by `DecodeConfig` before decoding, then decoded and re-encoded so no
  ancillary chunk survives. Served at `/app-icon.png` in place of the embedded stamp, with
  `Cache-Control: no-cache`, an ETag and `X-Content-Type-Options: nosniff`. That URL already
  serves the console, login page and Element's sign-in logo. Reset deletes the setting.
- **Routes** (admin; changes need a fresh sign-in): `PUT /api/admin/branding/name` `{"name":…}`
  (empty resets), `PUT /api/admin/branding/logo` (raw PNG body), `DELETE
  /api/admin/branding/logo`. Audit `admin.brand_name` (old and new name, outcome) and
  `admin.brand_logo` (SHA-256 and size of the stored PNG, or reset; outcome).
- **Element write failure** (file missing, unparseable, unwritable): the setting stays saved;
  Settings shows "saved, but Element shows X: <error>"; the next tick retries. The app never
  creates or replaces the file wholesale.

## Section 2: KyIdentity sync status

- **Webhook.** On each acknowledged delivery (applied, superseded or duplicate), the handler
  writes `kyidentity_webhook_last` (time, event kind). It is the key's only writer. A delivery
  that fails with a server error is neither recorded nor counted.
- **Rejections.** Unauthenticated, so never written to the database or audit: an in-memory
  count since start, plus the time and reason class of the last one: `not_configured` (secret
  unset or short), `bad_signature`, `stale`, `bad_headers`, `malformed` (signed, but not a
  usable SCIM user). Bodies are never kept.
- **Sweep.** The syncer writes `matrix_sweep_last` after every sweep: finished at, ok or failed,
  error text (bounded, no secrets), actions applied and failed, start of the current failing
  streak. It is the key's only writer. The streak survives a restart: it is seeded from the
  stored record.
- **Route.** `GET /api/admin/matrix/sync-status` (admin; Matrix off: 404) returns both records
  and the rejection counters.
- **Settings panel "KyIdentity sync"**: last accepted webhook, rejections since restart and the
  last reason, last sweep and result, and hints: never received → check the `suite_webhook`
  system and `KY_KYIDENTITY_HMAC_SECRET`; bad signatures → the secrets differ; sweep failing →
  the error and since when; always → "A change made while KyMessages was down waits in
  KyIdentity as an uncertain write; resume it there", linking KyIdentity.
- **Overview** gains a sync card (ok, warning, failing) linking to the panel. A
  `bad_signature` or `not_configured` rejection warns only while it is not older than the last
  accepted webhook.

## Section 3: build, failures, proof

- **Server.** New `internal/branding` (pure: name validation, `NormalizePNG`,
  `PatchElementBrand(path, name)`); the routes, `/app-icon.png` handler and the reconcile
  (`Server.ReconcileBrand`, one writer at a time) in `internal/api`, which `cmd/server` runs at
  the start of its maintenance loop and on every tick; the webhook record and rejection
  counters in the KyIdentity sync handler; the sweep record in `internal/matrixsync`.
- **Compose.** The app's mount of `./matrix/element` becomes a read-write bind of its one file,
  `config.json` (not nested in a read-only directory bind, which breaks `docker cp`);
  `scripts/check-compose-matrix.sh` asserts it is the only read-write path under `./matrix`.
- **Web.** Settings gains Branding (name, logo upload with preview, resets) and KyIdentity
  sync panels; Overview the sync card. Existing design system, confirm prompt, no new
  dependencies.
- **Known limit.** Element's nginx can read the file mid-write; that page load fails and the
  next succeeds. Writes happen only on change. A crash mid-write can leave the file truncated;
  re-running `matrix-init` restores it and the next tick re-applies the name.
- **Tests.** Name validation (empty, 65 characters, control characters); PNG refusals (JPEG,
  SVG, 1025 px, 1 MiB + 1 byte, a decompression bomb) and a `tEXt` chunk stripped; the patch
  keeps every other key and the inode (`os.SameFile`) and is idempotent; refuses a missing or
  unparseable file; `matrix-init` keeps the inode; sync records written by their owner only;
  rejections cause no database write; routes (role, freshness, audit rows, Matrix off 404);
  vitest for both panels and the card.
- **Acceptance.** A console rename changes `brand` in the chat host's `/config.json` and
  Element's title; an uploaded logo is served at `/app-icon.png` as its re-encoding; after the
  offboarding steps sync status shows an accepted webhook and an ok sweep; a badly signed
  delivery is counted as rejected.
- **Browser regressions.** Settings across themes, widths and keyboard use.

## Out of scope

Colour or theme editing; favicons and the PWA manifest icons; a "sweep now" action; sync
history or alerting; Element X branding.

## Risks

- The app can now write one file Element trusts; decision 2 accepts this.
- A partially written `config.json` can fail one page load (known limit above).
