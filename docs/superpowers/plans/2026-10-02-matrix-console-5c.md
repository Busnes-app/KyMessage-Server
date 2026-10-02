# Matrix Console 5c (Settings) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A KyMessages admin renames the product and replaces its logo from the console, and both reach Element without a shell or a restart. The admin also sees whether KyIdentity's offboarding sync works (last accepted webhook, rejected deliveries, last sweep), with a hint naming the fix.

**Architecture:**
- A new pure package `internal/branding` validates names, normalises PNGs and patches only Element's `brand` key in place (same inode).
- `internal/api` stores `brand_name` and `brand_logo` in the settings table, serves the logo at `/app-icon.png`, and owns `Server.ReconcileBrand`, the one in-process writer of Element's config, behind a mutex. The name handler calls it after each save, and `cmd/server`'s maintenance loop calls it at start and every minute.
- `matrix-init` rewrites `element/config.json` in place too. Compose gives the app a read-write bind of that one file.
- `internal/sso` records each acknowledged directory webhook and counts refused ones in memory. `internal/matrixsync` records each sweep. `GET /api/admin/matrix/sync-status` returns both.
- The web console gets Branding and KyIdentity sync panels in Settings and a sync card on the Overview. The hints are computed in the page.

**Tech Stack:** Go 1.26 (stdlib only: `image/png`, `encoding/json`), React 19 + TypeScript + Vite + vitest, Playwright, the bash acceptance harness with Docker Compose.

**Spec:** `docs/superpowers/specs/2026-10-02-matrix-console-5c-design.md` (context: `2026-10-01-matrix-platform-design.md` decisions 10 and 11, `2026-10-02-matrix-console-5a-design.md`).

## Facts this plan relies on

Each fact below was read from source in this checkout. None has run live, except where the acceptance task (Task 7) proves it.

| Fact | Where |
|---|---|
| `ServeHTTP` already wraps every request body in `http.MaxBytesReader(w, r.Body, 1<<20)`; the logo handler adds its own cap of the same size so the logo's limit does not depend on the API's | `internal/api/server.go` |
| `GET /api/settings` returns `app_name` from `cfg.Server.AppName` and gives admins every setting as `extra_settings`, dropping only keys with the prefix `kyrecovery_token` | `internal/api/settings_handlers.go` |
| `cfg.Server.AppName` is also the capsule service name, the KyRecovery pairing service name and the local-copy name; a rename must never touch it | `internal/backup/payload.go:99`, `internal/api/backup_handlers.go:264,519,582` |
| Setting values are `TEXT` (`server_settings.value`) on SQLite and Postgres; `GetSetting` returns `store.ErrNotFound` when the key is absent, and `DeleteSetting` of an absent key is not an error | `internal/store/sqlstore.go`, `internal/store/migrations/migrations.go` |
| `/app-icon.png` is today an embedded file under `web/dist`, served by `web.Handler()` through the `/` catch-all; the service worker never caches it (only `/`, `/index.html`, `/manifest.json`, `/assets/`) | `web/embed.go`, `web/public/sw.js` |
| CSP is `img-src 'self' data:`; it stays as is (no `blob:`) | `internal/api/server.go` |
| `HandleSyncWebhook` wraps every `syncauth.Verify` error as `fmt.Errorf("%w: %v", ErrSyncUnauthorized, err)`, so the cause is not `errors.Is`-reachable today; `syncauth` distinguishes `ErrShortKey`, `ErrNoSignature`, `ErrMissingFields`, `ErrBadTimestamp`, `ErrStale` and `ErrBadSignature` (`ErrReplay` cannot occur: no `Replay` option is passed). A missing secret and a signed but unusable body (`ErrSyncMalformed`) are the handler's own | `internal/sso/kyidentity.go`; `ky-primitives@v0.8.0/syncauth/syncauth.go:52-64,160-195` |
| The MAS client's errors carry method, path and status (`StatusError`), `MAS token: HTTP <n>` or `MAS token: unusable response`; credentials go in the basic-auth header, and the admin URL is validated with no userinfo. So a sweep error holds no secret | `internal/matrixsync/mas.go`, `internal/config/config.go:316-322` |
| `Syncer.Run` logs a failure once per streak with a local `failing` flag; `Sweep` is called by tests only | `internal/matrixsync/matrixsync.go` |
| `matrix-init` writes every rendered file with `replaceFile` (temp file and rename), so a re-run gives `element/config.json` a new inode | `internal/matrixinit/matrixinit.go:237-246,350-360` |
| Element mounts `./matrix/element/config.json:/app/config.json:ro`; the app mounts `./matrix/element` read-only at `/matrix/element` and `KY_MATRIX_DIR=/matrix`; `scripts/check-compose-matrix.sh` requires every app bind under `./matrix` to be read-only | `docker-compose.matrix.yml`, `scripts/check-compose-matrix.sh` |
| The app container has no `cap_drop` and runs as root, so it keeps Docker's default `CAP_DAC_OVERRIDE` and can write a 0644 file owned by `KY_MATRIX_UID`. **Unproven** until Task 7 runs | `docker-compose.yml` |
| `cfg.SSO.KyIdentityIssuer` is already returned to admins as the users page's `directory_url`; sync status reuses it as `kyidentity_url` | `internal/api/matrix_handlers.go:148` |
| Playwright runs with `workers: 1`, so the browser projects share one server in sequence | `web/playwright.config.mjs` |

**Dry run of this plan (2026-10-02).** The code blocks of Tasks 1–6 were applied to a scratch copy of this branch, and every listed check passed:
- `go vet ./...`;
- `go test -race` for `internal/branding`, `internal/matrixinit`, `internal/api`, `internal/sso`, `internal/matrixsync` and `cmd/server`;
- `scripts/check-compose-matrix.sh`, which fails before the Compose edit and passes after it, and `scripts/check-compose-proxy.sh`;
- vitest (13 files) and `npm run build`;
- `npm run test:browser`: 6 projects passed. Without the two `.app-brand` CSS lines, `dark-390` fails at `fits`.

Two mutation checks bite:
- Removing `brandMu` makes `TestConcurrentSavesAndTicksLeaveValidConfig` fail under `-race`.
- `textpng -check` rejects its own fixture.

Task 7's harness edits pass `bash -n`, `shellcheck` and `node --check`; they have **not** run live.

## Open questions for the controller

Each comes with a recommendation. The plan implements the recommendation; change the task if you decide otherwise.

1. **What counts as an "accepted" webhook.** The spec says the record is written "on each delivery that passes the signature check". A signed but malformed body passes that check, yet it is refused with 400. A signed delivery that hits a database error gets a 500.
   - **Recommend:** write `kyidentity_webhook_last` only when `HandleSyncWebhook` returns nil. That covers applied, superseded and duplicate events, the same deliveries that wake the sweep.
   - A signed but malformed body counts as a rejection with reason `malformed`.
   - A 500 is neither recorded nor counted; it is logged as today.
2. **Rejection reasons.** These are the classes the handler can actually tell apart. The spec named three: bad signature, stale, malformed.
   - `not_configured`: the secret is unset or shorter than 16 bytes.
   - `bad_signature`: no signature, or a wrong one.
   - `stale`: the timestamp is outside ±5 minutes.
   - `bad_headers`: the event type, ID or timestamp is missing or unparseable.
   - `malformed`: signed, but not a usable SCIM user.
   - This needs `ErrSyncUnauthorized` to wrap the cause with `%w: %w` (Task 4). The log line keeps the same text.
   - **Recommend:** accept the five classes.
3. **Where the reconcile lives.** The name handler must reconcile right after a save, and `internal/api` cannot import `cmd/server`.
   - **Recommend:** put `Server.ReconcileBrand(ctx) error` in `internal/api`, with the mutex and the once-per-streak log.
   - `cmd/server` keeps ownership of *when* it runs: at the start of `maintenanceLoop` and on every tick. The function is a no-op without Matrix.
4. **Key order in Element's `config.json`.** **Recommend:** splice only the bytes of the `brand` value, using the decoder's token offsets, instead of re-marshalling a map.
   - Every other byte, key order and formatting included, stays as `matrix-init` rendered it.
   - A missing `brand` key is inserted first in the object. Duplicate top-level `brand` keys are all patched, because JavaScript's `JSON.parse` keeps the last one.
5. **Name characters.** Following the ruling, the name refuses Cc and Cf characters, and also Zl and Zp (line and paragraph separators, which break a title).
   - Side effect: emoji sequences joined with ZWJ (U+200D, a Cf character) are refused. Single emoji are accepted.
   - **Recommend:** accept this.
6. **Audit of refusals.** **Recommend:** refusals are audited after authentication and freshness pass, as `outcome="refused: <reason>"`, never with the bytes. A name body that is not exactly `{"name": "..."}` is a 400 and is not audited, as for rooms.
   - Names in audit details are clipped to 40 bytes each, because `%q` can double a name's length and the details cap is 200 bytes.
7. **Branding routes run detached.** **Recommend:** `tracked`, with a 10 s context detached from the request, like the other audited changes. A dropped connection then cannot save a name or logo without an audit row.
8. **The failing streak survives a restart.** **Recommend:** `Run` seeds `failing_since` from the stored record, so a sweep that failed before a restart keeps its original start time.
9. **Where the acceptance proves 5c.**
   - **Sync status:** in the existing `console` step. It runs right after the offboarding steps, so the record comes from live deliveries. After the restore, the records would come from the capsule.
   - **Branding:** in a new `settings` step on the restored stack, after `rooms`. Its matrix-init re-run is undone by a maintenance tick.
   - **Recommend:** accept. The settings step waits up to 75 s for that tick and loads Element once more, so the run gets about 2 minutes longer.
10. **matrix-init and a broken `element/config.json`.** With the in-place write, an existing `element/config.json` that is not a regular file now makes `matrix-init` fail instead of being replaced. **Recommend:** accept; it names the file.
11. **The app writes a file it does not own.** `element/config.json` belongs to `KY_MATRIX_UID` with mode 0644. The root app can write it only because it keeps Docker's default `CAP_DAC_OVERRIDE`.
    - **Recommend:** say so in the Compose comment and in `internal/matrixinit/AGENTS.md`.
    - If the app is ever hardened with `cap_drop: [ALL]`, the write fails visibly: the reconcile logs it and Settings shows Element's value and the error.
12. **The header logo updates on the next page load.** Spec decision 3 says next page load. After a rename the header name updates at once, because the shell re-reads `/api/settings`. **Recommend:** accept; the Settings preview shows the new logo at once.

## Global Constraints

- **Name.** 1–64 characters after trimming, counted as runes, valid UTF-8, no control characters (Cc). The ruling adds format characters (Cf: bidi overrides and isolates, zero-width characters, BOM) and line or paragraph separators (Zl, Zp). An empty or blank name resets to `KY_APP_NAME`.
- **Logo.**
  - `image/png` only (SVG can carry script).
  - Body at most 1 MiB = `1<<20` bytes. 1 MiB + 1 byte is refused with 413.
  - Dimensions at most 1024×1024, checked by `png.DecodeConfig` before decoding.
  - Decoded and re-encoded, so no ancillary chunk survives.
  - Stored base64 in setting `brand_logo`. Reset deletes the setting.
- **Effective name.** `brand_name` if set, else `KY_APP_NAME`. `GET /api/settings` returns it as `app_name`. The console header, the login page and Element's `brand` use it.
  - It is display only: capsules, pairing, local copies and backup status keep `cfg.Server.AppName`.
- **Element.**
  - The app changes only the top-level `brand` key in `<KY_MATRIX_DIR>/element/config.json`, and preserves every other byte.
  - It writes only when the value differs, and in place: same inode, `O_WRONLY|O_TRUNC`, no create, no rename, no chmod.
  - It runs at startup, after each save and on each maintenance tick (every minute), only when Matrix is enabled.
  - A failure leaves the setting saved and is logged once per failure streak. Settings shows "saved, but Element shows X: <error>".
  - `matrix-init` also writes this file in place.
- **Routes:**
  - `GET /api/admin/branding` (admin) → `{name, stored_name, default_name, logo:{custom, sha256, size}, element?:{brand?, error?}}`.
  - `PUT /api/admin/branding/name` with `{"name":…}` (fresh admin; empty resets).
  - `PUT /api/admin/branding/logo` with a raw PNG body (fresh admin).
  - `DELETE /api/admin/branding/logo` (fresh admin).
  - `GET /api/admin/matrix/sync-status` (admin; with Matrix off, 404 `matrix_disabled`) → `{webhook: {at, kind}|null, rejected: {count, last_at, last_reason}, sweep: record|null, kyidentity_url}`.
- **Audit.**
  - `admin.brand_name`: details `outcome old new`.
  - `admin.brand_logo`: details `outcome sha256 size`, or `outcome="reset"`.
  - Both are written for success and failure, as strict quoted tokens through `auditFields`.
- **`/app-icon.png`.** Public. With a logo set it serves the decoded bytes with `Content-Type: image/png`, `Cache-Control: no-cache`, `ETag` set to the quoted hex SHA-256, 304 on `If-None-Match`, and `X-Content-Type-Options: nosniff`. Otherwise it serves the embedded stamp, also `no-cache`.
- **`extra_settings`** never carries `brand_logo`, `kyidentity_webhook_last` or `matrix_sweep_last`.
- **Sync records.**
  - `kyidentity_webhook_last` (`{at, kind}`) is written only by `internal/sso`, after an acknowledged delivery.
  - `matrix_sweep_last` (`{finished_at, ok, error, applied, failed, failing_since}`) is written only by `internal/matrixsync`. `error` is at most 300 bytes, and `failing_since` is null when ok.
  - Rejections live in memory only, with no database write, no audit and no change to logging. Bodies are never kept.
- **Hints** are computed in the web page from the status, never on the server.
- **Overview sync card:**
  - ok: a webhook was seen and the last sweep was ok.
  - warning: no webhook ever, no sweep yet, or a last rejection reason of `bad_signature` or `not_configured`.
  - failing: the last sweep failed.
  - No card when Matrix is off.
- **Compose.** The app's only writable source under `./matrix` is exactly `./matrix/element/config.json` (`create_host_path: false`), nested after the read-only `./matrix/element`. Element's own mount stays read-only.
- **Dependencies and design.** No new Go or npm dependencies. Use the existing design system (`panel`, `dr-*`, `badge`). No `blob:` URLs and no CSP change. Do not edit `web/src/ky-ui/`.
- **`web/dist`.** Every web task rebuilds `web/dist` and commits it, because CI diffs it.
- **Commits.** Each commit message body ends with a blank line, then a truthful `Co-Authored-By:` line naming the model that actually wrote the commit, for example `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.
- **DOX.** Every task that changes a contract updates the owning `AGENTS.md` in the same commit.

## Review Focus

1. **An admin page load carries the logo or the sync records.** `extra_settings` returns every setting, so a 1 MiB logo (about 1.4 MB base64) would ride on every `/api/settings` load, and the sync records would leak there too. Tests: Task 3 `TestSettingsCarryTheEffectiveNameButNotTheLogo`, Task 4 `TestSettingsHideTheSyncRecords`.
2. **The admin resets the logo but browsers keep showing the custom one.** The embedded stamp has no validator, and the preview reuses one URL. Expected: the next load shows the stamp. Tests: Task 3 `TestLogoUploadNormalisesServesAndResets`, which revalidates with the old ETag and must get the stamp with `no-cache`; Task 5 "uploads a PNG and previews it by its digest", where the preview switches to `?v=default`.
3. **The minute tick rewrites Element's config or logs every minute.** Expected: no write while the brand already matches (Element's nginx can read a half-written file), and one log line per failure streak. Test: Task 3 `TestReconcileWritesOnlyOnChangeAndLogsOncePerStreak`.
4. **A save and a tick write the file at the same moment.** Two in-place writers can interleave into invalid JSON, and Element then fails every page load until the next change. Expected: one writer at a time, so the file stays valid and holds the latest name. Test: Task 3 `TestConcurrentSavesAndTicksLeaveValidConfig`.
5. **A rename reaches the backup service name.** `KY_APP_NAME` is also what capsules are sealed under and what KyRecovery pins, so a rename there would break every deposit with 403 and every restore. Expected: the console name is display only. Test: Task 3 `TestBrandNameNeverRenamesTheService`.

---

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/branding/branding.go` (new) | `ValidateName`, `NormalizePNG`, `PatchElementBrand`, `ElementBrand`: pure, no store, no log | 1 |
| `internal/branding/AGENTS.md` (new) | Contract for the package | 1 |
| `internal/matrixinit/matrixinit.go` | `element/config.json` written in place (`writeInPlace`) | 2 |
| `docker-compose.matrix.yml`, `scripts/check-compose-matrix.sh` | The app's one read-write file under `./matrix`, and its check | 2 |
| `internal/api/branding_handlers.go` (new) | Branding routes, `/app-icon.png`, `effectiveName`, `ReconcileBrand` | 3 |
| `internal/api/settings_handlers.go` | Effective `app_name`; `notExtra` filter | 3, 4 |
| `cmd/server/maintenance.go`, `cmd/server/main.go` | Reconcile at loop start and every tick | 3 |
| `internal/sso/syncstatus.go` (new) | Webhook record, rejection counters and reasons | 4 |
| `internal/matrixsync/record.go` (new) | Sweep record and failing streak | 4 |
| `internal/api/sync_handlers.go` (new) | `GET /api/admin/matrix/sync-status` | 4 |
| `web/src/components/BrandingPanel.tsx` (new) | Branding panel, DTO, Element notice | 5 |
| `web/src/components/SyncPanel.tsx` (new) | Sync panel, DTO, `syncState`, `syncHints`, `syncCard` | 5 |
| `web/src/pages/Settings.tsx`, `Dashboard.tsx`, `App.tsx`, `Login.tsx`, `styles/theme.css` | Panels, Overview card, shell refresh, long-name wrapping | 5 |
| `web/browser/ui.spec.mjs` | Branding across themes, widths and keyboard | 6 |
| `scripts/matrix-acceptance/textpng/main.go` (new), `e2e.mjs`, `matrix-acceptance.sh` | Live proof | 7 |
| `README.md`, `docs/RESTORE.md`, `AGENTS.md` | Operator docs and status | 3, 7 |

---

### Task 1: `internal/branding`: name, PNG and Element patch

**Files:**
- Create: `internal/branding/branding.go`
- Create: `internal/branding/branding_test.go`
- Create: `internal/branding/AGENTS.md`
- Modify: `AGENTS.md` (root Child DOX Index)

**Interfaces:**
- Consumes: nothing.
- Produces (package `branding`, import `github.com/Busnes-app/ky_server_base/internal/branding`):
  - `const MaxNameRunes = 64`, `const MaxLogoBytes = 1 << 20`, `const MaxLogoSide = 1024`
  - `var ErrNotPNG, ErrLogoTooLarge, ErrLogoDimensions error`
  - `func ValidateName(s string) (string, error)`: returns the trimmed name.
  - `func NormalizePNG(b []byte) ([]byte, error)`
  - `func PatchElementBrand(path, name string) (changed bool, err error)`
  - `func ElementBrand(path string) (string, error)`: the top-level `brand`, `""` when absent.

- [ ] **Step 1: Write the failing tests.** Create `internal/branding/branding_test.go`:

```go
package branding

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateName(t *testing.T) {
	for in, want := range map[string]string{
		"Acme Chat":             "Acme Chat",
		"  Acme  ":              "Acme",
		strings.Repeat("é", 64): strings.Repeat("é", 64),
		"Ünïcode 会話 🙂":          "Ünïcode 会話 🙂",
	} {
		if got, err := ValidateName(in); err != nil || got != want {
			t.Errorf("%q: got %q, %v", in, got, err)
		}
	}
	for name, in := range map[string]string{
		"empty":            "",
		"blank":            " \t ",
		"65 characters":    strings.Repeat("é", 65),
		"newline":          "Acme\nChat",
		"tab":              "Acme\tChat",
		"NUL":              "Acme\x00",
		"DEL":              "Acme\x7f",
		"C1 control (NEL)": "Ac\u0085me",
		"bidi override":    "\u202eAcme",
		"bidi isolate":     "Acme\u2066x",
		"zero-width space": "Ac\u200bme",
		"ZWJ":              "Ac\u200dme",
		"BOM":              "\ufeffAcme",
		"line separator":   "Acme\u2028Chat",
		"invalid UTF-8":    "Acme\xff",
	} {
		if _, err := ValidateName(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func encode(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 191, G: 63, B: 24, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// chunk is one PNG chunk: length, type, data, and the CRC over type and data.
func chunk(typ string, data []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	out = append(out, typ...)
	out = append(out, data...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(typ), data...)))
}

// afterIHDR inserts c after the signature (8 bytes) and the IHDR chunk (25 bytes).
func afterIHDR(p, c []byte) []byte {
	return append(append(append([]byte{}, p[:33]...), c...), p[33:]...)
}

// withSize rewrites IHDR's width and height and fixes its CRC; the pixels stay as they were.
func withSize(p []byte, w, h uint32) []byte {
	p = append([]byte{}, p...)
	binary.BigEndian.PutUint32(p[16:], w)
	binary.BigEndian.PutUint32(p[20:], h)
	binary.BigEndian.PutUint32(p[29:], crc32.ChecksumIEEE(p[12:29]))
	return p
}

func TestNormalizePNGStripsAncillaryChunks(t *testing.T) {
	in := afterIHDR(encode(t, 16, 16), chunk("tEXt", []byte("Comment\x00kymatrix-secret")))
	if _, err := png.Decode(bytes.NewReader(in)); err != nil {
		t.Fatalf("fixture does not decode: %v", err)
	}
	out, err := NormalizePNG(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("tEXt")) || bytes.Contains(out, []byte("kymatrix-secret")) {
		t.Fatal("the tEXt chunk survived")
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil || img.Bounds().Dx() != 16 || img.Bounds().Dy() != 16 {
		t.Fatalf("re-encoding does not decode as the same image: %v", err)
	}
	if _, err := NormalizePNG(encode(t, MaxLogoSide, MaxLogoSide)); err != nil {
		t.Fatalf("1024×1024 refused: %v", err)
	}
}

func TestNormalizePNGRefuses(t *testing.T) {
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	small := encode(t, 4, 4)
	oversize := make([]byte, MaxLogoBytes+1)
	copy(oversize, small)
	for name, tc := range map[string]struct {
		in   []byte
		want error
	}{
		"JPEG":                      {jpg.Bytes(), ErrNotPNG},
		"SVG":                       {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ErrNotPNG},
		"empty":                     {nil, ErrNotPNG},
		"signature only":            {small[:8], ErrNotPNG},
		"truncated pixels":          {small[:len(small)-20], ErrNotPNG},
		"zero width":                {withSize(small, 0, 4), ErrNotPNG},
		"1025 px wide":              {encode(t, 1025, 1), ErrLogoDimensions},
		"1025 px high":              {encode(t, 1, 1025), ErrLogoDimensions},
		"bomb: header says 50000²":  {withSize(small, 50000, 50000), ErrLogoDimensions},
		"1 MiB + 1 byte":            {oversize, ErrLogoTooLarge},
	} {
		if _, err := NormalizePNG(tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

const elementConfig = `{
  "default_server_config": {
    "m.homeserver": { "base_url": "https://matrix.example.com" }
  },
  "brand": "KyMessages",
  "branding": { "auth_header_logo_url": "https://admin.example.com/app-icon.png" },
  "setting_defaults": { "UIFeature.voip": false }
}
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPatchElementBrandChangesOnlyTheBrandInPlace(t *testing.T) {
	p := writeConfig(t, elementConfig)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	name := `Acme "Chat" <ops>`
	changed, err := PatchElementBrand(p, name)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(p)
	if want := strings.Replace(elementConfig, `"KyMessages"`, `"Acme \"Chat\" \u003cops\u003e"`, 1); string(got) != want {
		t.Fatalf("other bytes changed:\n%s", got)
	}
	after, _ := os.Stat(p)
	if !os.SameFile(before, after) {
		t.Fatal("the file was replaced, not rewritten in place")
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %04o", after.Mode().Perm())
	}
	if brand, err := ElementBrand(p); err != nil || brand != name {
		t.Fatalf("read back %q, %v", brand, err)
	}
	// Idempotent: the same name again writes nothing.
	old := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if changed, err := PatchElementBrand(p, name); err != nil || changed {
		t.Fatalf("second patch: changed=%v err=%v", changed, err)
	}
	if fi, _ := os.Stat(p); !fi.ModTime().Equal(old) {
		t.Fatal("an unchanged brand was rewritten")
	}
}

func TestPatchElementBrandAddsAMissingKeyAndPatchesDuplicates(t *testing.T) {
	for in, want := range map[string]string{
		`{"a": 1}`: `{"brand":"Acme","a": 1}`,
		`{ }`:      `{"brand":"Acme" }`,
		`{"brand": "x", "b": {"brand": "nested"}, "brand": "y"}`: `{"brand": "Acme", "b": {"brand": "nested"}, "brand": "Acme"}`,
	} {
		p := writeConfig(t, in)
		if _, err := PatchElementBrand(p, "Acme"); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	if brand, err := ElementBrand(writeConfig(t, `{"a": 1}`)); err != nil || brand != "" {
		t.Errorf("absent brand: %q, %v", brand, err)
	}
	if _, err := ElementBrand(writeConfig(t, `{"brand": 5}`)); err == nil {
		t.Error("a non-string brand was read")
	}
}

func TestPatchElementBrandRefusesAndLeavesTheFileAlone(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.json")
	if _, err := PatchElementBrand(missing, "Acme"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("a missing config was created")
	}
	if _, err := PatchElementBrand(t.TempDir(), "Acme"); err == nil {
		t.Error("a directory was accepted")
	}
	for name, body := range map[string]string{
		"unparseable": `{"brand": "KyMessages",`,
		"array":       `["brand"]`,
		"string":      `"brand"`,
		"trailing":    `{"brand":"KyMessages"} {}`,
		"empty":       ``,
	} {
		p := writeConfig(t, body)
		if _, err := PatchElementBrand(p, "Acme"); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if got, _ := os.ReadFile(p); string(got) != body {
			t.Errorf("%s: file changed to %q", name, got)
		}
	}
}
```

- [ ] **Step 2: Run it to see it fail.** Run `go test ./internal/branding/`. Expected: FAIL to build, with `undefined: ValidateName` (and the other new names).

- [ ] **Step 3: Implement.** Create `internal/branding/branding.go`:

```go
// Package branding holds the pure checks and the one file edit behind the console's product
// name and logo: name validation, PNG normalisation and Element's brand patch.
package branding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxNameRunes bounds the product name, in characters after trimming.
	MaxNameRunes = 64
	// MaxLogoBytes bounds an upload and its re-encoding.
	MaxLogoBytes = 1 << 20
	// MaxLogoSide bounds each dimension, checked before any pixel is decoded.
	MaxLogoSide = 1024
)

var (
	ErrNotPNG         = errors.New("the logo is not a PNG image")
	ErrLogoTooLarge   = errors.New("the logo is larger than 1 MiB")
	ErrLogoDimensions = fmt.Errorf("the logo must be between 1×1 and %d×%d pixels", MaxLogoSide, MaxLogoSide)
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// ValidateName trims s and accepts 1 to MaxNameRunes characters of valid UTF-8 with no
// control, format (bidi overrides, zero-width characters) or line/paragraph separator
// character: the name lands in page titles and Element's config, where those reorder or hide text.
func ValidateName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", errors.New("the name is not valid UTF-8")
	}
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		return "", errors.New("the name is empty")
	case n > MaxNameRunes:
		return "", fmt.Errorf("the name is longer than %d characters", MaxNameRunes)
	}
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return "", fmt.Errorf("the name contains an invisible or control character (U+%04X)", r)
		}
	}
	return s, nil
}

// NormalizePNG returns b decoded and re-encoded, so no ancillary chunk (text, EXIF, colour
// profile, animation) survives. The header's dimensions are refused before any pixel is
// decoded, which bounds the decode at MaxLogoSide² pixels whatever the header claims.
func NormalizePNG(b []byte) ([]byte, error) {
	if len(b) > MaxLogoBytes {
		return nil, ErrLogoTooLarge
	}
	if !bytes.HasPrefix(b, pngSignature) {
		return nil, ErrNotPNG
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotPNG
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxLogoSide || cfg.Height > MaxLogoSide {
		return nil, ErrLogoDimensions
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNotPNG
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	if out.Len() > MaxLogoBytes {
		return nil, ErrLogoTooLarge
	}
	return out.Bytes(), nil
}

// PatchElementBrand sets the top-level "brand" of Element's config.json at path to name and
// changes no other byte. It writes only when that changes the file, and through the existing
// inode (no create, rename or chmod): a single-file bind mount keeps the inode it was given.
// A missing, non-regular or unparseable file, or one that is not a JSON object, is refused
// untouched. Callers serialise writers; a reader can see the file mid-write.
func PatchElementBrand(path, name string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	patched, err := setBrand(old, name)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if bytes.Equal(patched, old) {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return false, err
	}
	_, err = f.Write(patched)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err == nil, err
}

// ElementBrand reads the top-level "brand" of Element's config.json, "" when absent.
func ElementBrand(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	spans, err := brandSpans(b)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(spans) == 0 {
		return "", nil
	}
	last := spans[len(spans)-1] // JSON.parse keeps the last duplicate
	var v string
	if err := json.Unmarshal(b[last.start:last.end], &v); err != nil {
		return "", fmt.Errorf("%s: brand is not a string", path)
	}
	return v, nil
}

type span struct{ start, end int }

// brandSpans returns the byte range of every top-level "brand" value in b, which must be one
// JSON object.
func brandSpans(b []byte) ([]span, error) {
	if !json.Valid(b) {
		return nil, errors.New("is not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("is not a JSON object")
	}
	var spans []span
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if key == "brand" {
			end := int(dec.InputOffset())
			s := span{end - len(v), end}
			if s.start < 0 || !bytes.Equal(b[s.start:s.end], v) {
				return nil, errors.New("could not locate the brand value")
			}
			spans = append(spans, s)
		}
	}
	return spans, nil
}

// setBrand replaces every top-level brand value with name, or inserts one first.
func setBrand(b []byte, name string) ([]byte, error) {
	spans, err := brandSpans(b)
	if err != nil {
		return nil, err
	}
	v, err := json.Marshal(name)
	if err != nil {
		return nil, err
	}
	if len(spans) == 0 {
		i := bytes.IndexByte(b, '{') + 1
		sep := []byte(",")
		if bytes.TrimSpace(b[i:])[0] == '}' {
			sep = nil
		}
		return slices.Concat(b[:i], []byte(`"brand":`), v, sep, b[i:]), nil
	}
	out := b
	for i := len(spans) - 1; i >= 0; i-- {
		out = slices.Concat(out[:spans[i].start], v, out[spans[i].end:])
	}
	return out, nil
}
```

- [ ] **Step 4: Run it to see it pass.** Run `go test -race ./internal/branding/ -v`. Expected: PASS, all five tests.

- [ ] **Step 5: DOX.** Create `internal/branding/AGENTS.md`:

```markdown
# Branding

## Purpose
Pure checks and the one file edit behind the console's product name and logo.

## Ownership
Owns `branding.go`: `ValidateName`, `NormalizePNG`, `PatchElementBrand`, `ElementBrand`.
`internal/api` owns the routes, storage, `/app-icon.png` and `Server.ReconcileBrand`;
`cmd/server` decides when the reconcile runs.

## Local Contracts
- `ValidateName` trims, then accepts 1–64 runes (`MaxNameRunes`) of valid UTF-8 with no
  control (Cc), format (Cf: bidi overrides and isolates, zero-width characters, BOM) or
  line/paragraph separator (Zl, Zp) character. ZWJ emoji sequences are therefore refused.
- `NormalizePNG` accepts at most `MaxLogoBytes` (1 MiB) starting with the PNG signature, reads
  the dimensions with `png.DecodeConfig` and refuses zero or more than `MaxLogoSide` (1024)
  before decoding, then decodes and re-encodes, so no ancillary chunk (text, EXIF, ICC, APNG)
  survives. Every other format is refused (SVG can carry script); so is a re-encoding over
  `MaxLogoBytes`.
- `PatchElementBrand` changes only the bytes of every top-level `brand` value (inserting one
  first when absent), writes only when that changes the file, and writes through the existing
  inode (`O_WRONLY|O_TRUNC`; no create, rename or chmod): Compose binds that single file into
  Element and the app, and a bind keeps the inode it was given. A missing, non-regular,
  invalid or non-object file is refused untouched. A reader can see the file mid-write (one
  failed Element page load); callers serialise writers.
- No logging, store access or network.

## Verification
- `go test -race ./internal/branding/`

## Child DOX Index
None.
```

In the root `AGENTS.md` Child DOX Index, after the `internal/matrixinit/AGENTS.md` line, add:

```markdown
- [internal/branding/AGENTS.md](internal/branding/AGENTS.md): Product name validation, PNG normalisation and the in-place Element `brand` patch behind console settings.
```

- [ ] **Step 6: Commit.** Run `gofmt -w internal/branding && gofmt -l internal/branding` (expected: no output). Commit `internal/branding/` and `AGENTS.md`. Message: `branding: name validation, PNG normalisation and in-place Element brand patch`.

---

### Task 2: matrix-init writes Element's config in place; the app gets that one file read-write

**Files:**
- Modify: `internal/matrixinit/matrixinit.go` (the render loop in `Run`, about lines 224-250; add `writeInPlace` after `replaceFile`)
- Modify: `internal/matrixinit/matrixinit_test.go` (append)
- Modify: `docker-compose.matrix.yml` (header comment and the app's `volumes`)
- Modify: `scripts/check-compose-matrix.sh`
- Modify: `internal/matrixinit/AGENTS.md`, root `AGENTS.md` (the `docker-compose.matrix.yml` paragraph)

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces:
  - `matrixinit.Run` keeps the inode of an existing `element/config.json` and sets it to mode 0644.
  - The app container sees `/matrix/element/config.json` read-write. Task 3's `ReconcileBrand` writes it at `<KY_MATRIX_DIR>/element/config.json`.

- [ ] **Step 1: Write the failing matrix-init tests.** Append to `internal/matrixinit/matrixinit_test.go`:

```go
// Element's config.json reaches a running Element only through its inode: Compose binds the
// single file, and a rename is invisible to the container.
func TestElementConfigIsRewrittenInPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	el := filepath.Join(dir, "element", "config.json")
	hs := filepath.Join(dir, "synapse", "homeserver.yaml")
	elBefore, _ := os.Stat(el)
	hsBefore, _ := os.Stat(hs)
	// The console set another brand, and someone narrowed the mode.
	if err := os.WriteFile(el, []byte(`{"brand":"Acme"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(el, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(goodInput(), dir); err != nil {
		t.Fatal(err)
	}
	elAfter, _ := os.Stat(el)
	hsAfter, _ := os.Stat(hs)
	if !os.SameFile(elBefore, elAfter) {
		t.Fatal("element/config.json was replaced; a running Element keeps the old inode")
	}
	if elAfter.Mode().Perm() != 0o644 {
		t.Fatalf("element/config.json mode %04o, want 0644", elAfter.Mode().Perm())
	}
	b, _ := os.ReadFile(el)
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil || cfg["brand"] != "KyMessages" || cfg["default_server_config"] == nil {
		t.Fatalf("not fully re-rendered (err %v): %s", err, b)
	}
	if os.SameFile(hsBefore, hsAfter) {
		t.Fatal("other configs must still be published by rename")
	}
}

func TestElementConfigThatIsNotAFileIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "m")
	if _, err := runWithSecret(t, goodInput(), dir, "s3cret"); err != nil {
		t.Fatal(err)
	}
	el := filepath.Join(dir, "element", "config.json")
	if err := os.Remove(el); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(el, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(goodInput(), dir); err == nil || !strings.Contains(err.Error(), "element/config.json") {
		t.Fatalf("got %v, want a refusal naming element/config.json", err)
	}
}
```

- [ ] **Step 2: Run them to see them fail.** Run `go test ./internal/matrixinit/ -run 'TestElementConfig' -v`. Expected: `TestElementConfigIsRewrittenInPlace` FAILS with "element/config.json was replaced; a running Element keeps the old inode". `TestElementConfigThatIsNotAFileIsRefused` may already pass, because rename onto a directory fails; it pins the behaviour.

- [ ] **Step 3: Implement.** In `internal/matrixinit/matrixinit.go`, replace the render loop's struct and its element entry, and choose the writer per file:

```go
	for _, r := range []struct {
		tmpl, rel string
		mode      os.FileMode
		inPlace   bool
	}{
		{"homeserver.yaml.tmpl", "synapse/homeserver.yaml", 0o600, false},
		{"mas.yaml.tmpl", "mas/config.yaml", 0o600, false},
		{"pg-init.sql.tmpl", "postgres/init.sql", 0o600, false},
		// After init.sql by name: the entrypoint runs both on a new volume; operators run it once
		// on an existing stack. Idempotent.
		{"kybackup-role.sql.tmpl", "postgres/kybackup-role.sql", 0o600, false},
		// No secrets; the Element container reads it as a different user. In place: Compose
		// binds this single file into Element and the app, and a running container keeps the
		// inode it was given.
		{"element.json.tmpl", "element/config.json", 0o644, true},
	} {
		if r.rel == "mas/config.yaml" && res.ClientSecretMissing {
			continue
		}
		var b bytes.Buffer
		if err := templates.ExecuteTemplate(&b, r.tmpl, data); err != nil {
			return Result{}, err
		}
		write := replaceFile
		if r.inPlace {
			write = writeInPlace
		}
		if err := write(filepath.Join(dir, r.rel), b.Bytes(), r.mode); err != nil {
			return Result{}, fmt.Errorf("%s: %w", r.rel, err)
		}
		res.Rendered = append(res.Rendered, r.rel)
	}
```

Add after `replaceFile`:

```go
// writeInPlace rewrites an existing regular file through its own inode (truncate, write, then
// mode), so a container holding a bind of that one file sees the new content. An absent file
// is created by replaceFile; anything but a regular file is refused.
func writeInPlace(path string, b []byte, mode os.FileMode) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return replaceFile(path, b, mode)
	}
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New("exists and is not a regular file")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
```

- [ ] **Step 4: Run them to see them pass.** Run `go test ./internal/matrixinit/ ./cmd/server/ -v -run 'TestElement|TestInit|TestFirstRun|TestCompose'`. Expected: PASS, the existing write-once and first-run tests included.

- [ ] **Step 5: Make the Compose check demand the one writable file.** In `scripts/check-compose-matrix.sh`:
  - (a) After the header line `# keep composing with the proxy and static-IP overlays.`, add:

    ```bash
    # The app's one writable ./matrix path is Element's config.json, whose brand the console sets.
    ```

  - (b) Replace the `appmx=` line with:

    ```bash
    # Element's config.json is the one exception, checked by rw_check below.
    elementcfg="$root/matrix/element/config.json"
    appmx=$(jq -c --arg r "$root/matrix" --arg e "$elementcfg" '[.services.app.volumes[] | select(.type == "bind" and .source != $e and (.source == $r or (.source | startswith($r + "/"))))]' <<<"$out")
    ```

  - (c) After the `for d in synapse mas element postgres; do ... done` loop, add:

    ```bash
    # The app's only writable bind under ./matrix: Element's config.json, nested over the read-only
    # ./matrix/element (Docker mounts the deeper target second) and never created by Docker.
    # Element's own mount of it stays read-only (the per-service loop above).
    rw_check() {
      local rw
      rw=$(jq -c --arg r "$root/matrix" '[.services.app.volumes[] | select(.type == "bind" and (.source == $r or (.source | startswith($r + "/"))) and (.read_only // false) == false)]' <<<"$1")
      jq -e --arg e "$elementcfg" --argjson nc "$nocreate" 'length == 1 and .[0].source == $e and .[0].target == "/matrix/element/config.json" and .[0].bind == $nc' <<<"$rw" >/dev/null \
        || bad "$2: the app's writable ./matrix binds must be exactly ./matrix/element/config.json at /matrix/element/config.json, create_host_path false: $rw"
    }
    rw_check "$out" "matrix overlay"
    rw_check "$both" "matrix + static-ip"
    ```

- [ ] **Step 6: Run the check to see it fail.** Run `bash scripts/check-compose-matrix.sh; echo "exit $?"`. Expected: two lines starting `matrix overlay: the app's writable ./matrix binds must be exactly` and `matrix + static-ip: ...`, each ending `: []`, then `exit 1`.

- [ ] **Step 7: Add the bind.** In `docker-compose.matrix.yml`:
  - (a) In the header comment, replace `# The app also joins matrix-db and reads ./matrix (minus the Postgres superuser password) and the` / `# media volume read-only, for server backups (docs/RESTORE.md).` with:

    ```yaml
    # The app also joins matrix-db and reads ./matrix (minus the Postgres superuser password) and the
    # media volume read-only, for server backups (docs/RESTORE.md). It writes one file there:
    # Element's config.json, whose "brand" the console sets in place.
    ```

  - (b) In the app's `volumes`, directly after `- {<<: *matrix-ro, source: ./matrix/element, target: /matrix/element}`, add:

    ```yaml
          # The one writable file under ./matrix: the console sets Element's "brand" in it, in place
          # (same inode, so Element's own bind sees it). It belongs to KY_MATRIX_UID; the root app
          # writes it through Docker's default CAP_DAC_OVERRIDE, as this service drops no capability.
          - {type: bind, source: ./matrix/element/config.json, target: /matrix/element/config.json, read_only: false, bind: {create_host_path: false}}
    ```

- [ ] **Step 8: Run the checks to see them pass.** Run `bash scripts/check-compose-matrix.sh; echo "exit $?"`. Expected: `exit 0` with no other output. Run `bash scripts/check-compose-proxy.sh; echo "exit $?"`. Expected: `exit 0`. Run `go test ./internal/matrixinit/ -run TestComposeMountsEverySecretButTheSuperusers -v`. Expected: PASS.

- [ ] **Step 9: DOX.**
  - In `internal/matrixinit/AGENTS.md`, replace `Configs are re-rendered on every run.` with:

    ```markdown
    Configs are re-rendered on every run: `element/config.json` in place (`O_TRUNC` on the
      existing inode, then `fchmod 0644`; created by rename only when absent; anything but a
      regular file is refused), because Compose binds that single file into Element and the app
      and a running container keeps the inode it was given; every other file by temp file and rename.
    ```

  - In the same file's container-contract bullet, after the sentence that ends `since nginx cannot enter the 0700 dir).` (it wraps across two lines), add:

    ```markdown
    The app's only read-write path under `./matrix` is `./matrix/element/config.json`
      (`create_host_path: false`), nested over its read-only `./matrix/element`, so the console can
      set `brand` (`internal/branding`); the file belongs to `KY_MATRIX_UID`, and the root app
      writes it through Docker's default `CAP_DAC_OVERRIDE` (a `cap_drop: [ALL]` on the app would
      make the write fail, which Settings then shows).
    ```

  - In the root `AGENTS.md`, after the line `  root-owned \`/media\` onto the empty volume at each mount and undoes \`synapse-media-owner\`.`, add:

    ```markdown
      The app's one read-write path under `./matrix` is `./matrix/element/config.json`, nested over
      its read-only `./matrix/element`: the console sets Element's `brand` there in place, and the
      check holds the app to that one file (with the static-IP overlay too).
    ```

- [ ] **Step 10: Commit.** Run `gofmt -w internal/matrixinit && gofmt -l internal/matrixinit` (expected: no output). Commit `internal/matrixinit/`, `docker-compose.matrix.yml`, `scripts/check-compose-matrix.sh` and `AGENTS.md`. Message: `matrix-init: write Element's config.json in place; compose: the app's one writable ./matrix file`.

---

### Task 3: Branding API, `/app-icon.png` and the Element reconcile

**Files:**
- Create: `internal/api/branding_handlers.go`
- Create: `internal/api/branding_test.go`
- Modify: `internal/api/server.go` (`Server` fields; `routes()`)
- Modify: `internal/api/settings_handlers.go`
- Modify: `internal/api/authz_test.go` (`TestPrivilegedEndpointsRequireAdmin` cases)
- Modify: `cmd/server/maintenance.go`, `cmd/server/maintenance_test.go`, `cmd/server/main.go` (one line)
- Modify: `internal/api/AGENTS.md`, root `AGENTS.md` (the `cmd/server` paragraph)

**Interfaces:**
- Consumes (Task 1): `branding.ValidateName`, `branding.NormalizePNG`, `branding.PatchElementBrand`, `branding.ElementBrand`, `branding.MaxLogoBytes`, `branding.ErrLogoTooLarge`, `branding.ErrNotPNG`, `branding.ErrLogoDimensions`.
- Consumes (Task 2): the app's writable `<KY_MATRIX_DIR>/element/config.json`.
- Produces:
  - `func (s *Server) ReconcileBrand(ctx context.Context) error` (exported; `cmd/server` passes it to `maintenanceLoop`).
  - Unexported constants `brandNameKey = "brand_name"` and `brandLogoKey = "brand_logo"`.
  - `var notExtra map[string]bool` in `settings_handlers.go`; Task 4 adds the sync keys.
  - `func clipTo(v string, n int) string`.
  - `maintenanceLoop(ctx context.Context, st store.Store, brand func(context.Context) error, done chan<- struct{})`.
  - JSON of `GET /api/admin/branding` and of every branding change: `{"name","stored_name","default_name","logo":{"custom","sha256","size"},"element"?:{"brand"?,"error"?}}`. Task 5 parses it.

- [ ] **Step 1: Write the failing API tests.** Create `internal/api/branding_test.go`:

```go
package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/branding"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const elementJSON = `{"default_server_config":{"m.homeserver":{"base_url":"https://matrix.example.com"}},"brand":"KyMessages","disable_guests":true}`

// withElement turns Matrix on for branding and returns Element's config path, holding body
// unless body is empty.
func withElement(t *testing.T, cfg *config.Config, body string) string {
	t.Helper()
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.Dir = t.TempDir()
	p := filepath.Join(cfg.Matrix.Dir, "element", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// elementWith is elementJSON with brand set to name, as PatchElementBrand writes it.
func elementWith(name string) string {
	return strings.Replace(elementJSON, `"KyMessages"`, fmt.Sprintf("%q", name), 1)
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 191, G: 63, B: 24, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withTextChunk adds a tEXt chunk after IHDR, as a camera or an editor would.
func withTextChunk(p []byte) []byte {
	data := []byte("Comment\x00kymatrix-secret")
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(c, "tEXt"...)
	c = append(c, data...)
	c = binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(append([]byte("tEXt"), data...)))
	return append(append(append([]byte{}, p[:33]...), c...), p[33:]...)
}

// rawDo sends body as is with contentType, as the browser's logo upload does.
func rawDo(t *testing.T, srv *api.Server, session *http.Cookie, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(session)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "test-csrf"})
	req.Header.Set(auth.HeaderCSRF, "test-csrf")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

type brandingBody struct {
	Name        string `json:"name"`
	StoredName  string `json:"stored_name"`
	DefaultName string `json:"default_name"`
	Logo        struct {
		Custom bool   `json:"custom"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	} `json:"logo"`
	Element *struct {
		Brand *string `json:"brand"`
		Error string  `json:"error"`
	} `json:"element"`
}

func TestBrandNameRenamesTheConsoleAndElement(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p := withElement(t, cfg, elementJSON)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	path := "/api/admin/branding/name"
	got := decode[brandingBody](t, adminDo(t, srv, admin, "PUT", path, map[string]string{"name": "  Acme Chat  "}))
	if got.Name != "Acme Chat" || got.StoredName != "Acme Chat" || got.DefaultName != cfg.Server.AppName ||
		got.Element == nil || got.Element.Brand == nil || *got.Element.Brand != "Acme Chat" || got.Element.Error != "" {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(p); string(b) != elementWith("Acme Chat") {
		t.Fatalf("Element config: %s", b)
	}
	if after, _ := os.Stat(p); !os.SameFile(before, after) {
		t.Fatal("Element's config was replaced, not rewritten in place")
	}
	if s := decode[map[string]any](t, do(t, srv, "GET", "/api/settings", nil)); s["app_name"] != "Acme Chat" {
		t.Fatalf("login page name: %v", s["app_name"])
	}
	rows := auditRows(t, st, "admin.brand_name")
	if want := fmt.Sprintf(`outcome="saved" old=%q new="Acme Chat"`, cfg.Server.AppName); len(rows) != 1 || rows[0].Details != want || rows[0].UserID != "usr_root" {
		t.Fatalf("audit %+v", rows)
	}
	// Blank resets to KY_APP_NAME.
	got = decode[brandingBody](t, adminDo(t, srv, admin, "PUT", path, map[string]string{"name": " "}))
	if got.Name != cfg.Server.AppName || got.StoredName != "" {
		t.Fatalf("after reset: %+v", got)
	}
	if b, _ := os.ReadFile(p); string(b) != elementWith(cfg.Server.AppName) {
		t.Fatalf("Element config after reset: %s", b)
	}
	if _, err := st.Settings().GetSetting(ctx, "brand_name"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reset left brand_name: %v", err)
	}
	if w := adminDo(t, srv, admin, "GET", "/api/admin/branding", nil); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET branding: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestBrandNameRefusals(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	withElement(t, cfg, elementJSON)
	path := "/api/admin/branding/name"
	if w := adminDo(t, srv, staleAdmin(t, st), "PUT", path, map[string]string{"name": "Acme"}); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), `"code":"reauthentication_required"`) {
		t.Fatalf("stale admin: %d %s", w.Code, w.Body.String())
	}
	admin := loginAs(t, srv, st, "root", "admin")
	for _, bad := range []string{strings.Repeat("x", 65), "Acme\u202eChat", "Ac\nme"} {
		if w := adminDo(t, srv, admin, "PUT", path, map[string]string{"name": bad}); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", bad, w.Code)
		}
	}
	for _, body := range []any{map[string]any{"name": 5}, map[string]any{"name": "A", "extra": 1}, map[string]any{}} {
		if w := adminDo(t, srv, admin, "PUT", path, body); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", body, w.Code)
		}
	}
	if _, err := st.Settings().GetSetting(ctx, "brand_name"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused name was saved: %v", err)
	}
	rows := auditRows(t, st, "admin.brand_name")
	if len(rows) != 3 {
		t.Fatalf("want the 3 invalid names audited, not the stale session or the malformed bodies: %+v", rows)
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Details, `outcome="refused: the name `) {
			t.Errorf("details %q", r.Details)
		}
	}
}

// Element's file is missing: the name is still saved, and the answer says what Element shows.
func TestBrandNameSavedWhenElementCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p := withElement(t, cfg, "")
	got := decode[brandingBody](t, adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "PUT", "/api/admin/branding/name", map[string]string{"name": "Acme"}))
	if got.Name != "Acme" || got.Element == nil || got.Element.Brand != nil || got.Element.Error == "" {
		t.Fatalf("%+v", got)
	}
	if v, err := st.Settings().GetSetting(ctx, "brand_name"); err != nil || v != "Acme" {
		t.Fatalf("name not saved: %q %v", v, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the app created Element's config")
	}
	rows := auditRows(t, st, "admin.brand_name")
	if len(rows) != 1 || !strings.HasPrefix(rows[0].Details, `outcome="saved; Element not updated: `) {
		t.Fatalf("audit %+v", rows)
	}
}

func TestLogoUploadNormalisesServesAndResets(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	saved := decode[brandingBody](t, rawDo(t, srv, admin, "PUT", "/api/admin/branding/logo", "image/png", withTextChunk(testPNG(t, 32, 32))))
	if !saved.Logo.Custom || len(saved.Logo.SHA256) != 64 || saved.Logo.Size == 0 {
		t.Fatalf("%+v", saved)
	}
	icon := do(t, srv, "GET", "/app-icon.png", nil) // public: the login page shows it
	body := icon.Body.Bytes()
	sum := sha256.Sum256(body)
	h := icon.Header()
	if icon.Code != http.StatusOK || h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "no-cache" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("ETag") != `"`+saved.Logo.SHA256+`"` ||
		hex.EncodeToString(sum[:]) != saved.Logo.SHA256 || len(body) != saved.Logo.Size {
		t.Fatalf("served logo: %d %v", icon.Code, h)
	}
	if bytes.Contains(body, []byte("tEXt")) || bytes.Contains(body, []byte("kymatrix-secret")) {
		t.Fatal("the text chunk was served")
	}
	if _, err := png.Decode(bytes.NewReader(body)); err != nil {
		t.Fatalf("served logo does not decode: %v", err)
	}
	revalidate := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/app-icon.png", nil)
		req.Header.Set("If-None-Match", `"`+saved.Logo.SHA256+`"`)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	if w := revalidate(); w.Code != http.StatusNotModified {
		t.Fatalf("revalidation with the current ETag: %d", w.Code)
	}
	if strings.Contains(do(t, srv, "GET", "/api/settings", admin).Body.String(), "brand_logo") {
		t.Fatal("the logo rides on /api/settings")
	}
	// Reset: a browser revalidating its custom copy must get the stamp back.
	if got := decode[brandingBody](t, adminDo(t, srv, admin, "DELETE", "/api/admin/branding/logo", nil)); got.Logo.Custom {
		t.Fatalf("after reset: %+v", got)
	}
	embedded, err := os.ReadFile("../../web/dist/app-icon.png")
	if err != nil {
		t.Fatal(err)
	}
	if w := revalidate(); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), embedded) || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("after reset: %d, %d bytes, Cache-Control %q", w.Code, w.Body.Len(), w.Header().Get("Cache-Control"))
	}
	rows := auditRows(t, st, "admin.brand_logo")
	if len(rows) != 2 || rows[0].Details != `outcome="reset"` ||
		rows[1].Details != fmt.Sprintf(`outcome="saved" sha256=%q size="%d"`, saved.Logo.SHA256, saved.Logo.Size) {
		t.Fatalf("audit %+v", rows)
	}
}

func TestLogoUploadRefusals(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	path := "/api/admin/branding/logo"
	if w := rawDo(t, srv, staleAdmin(t, st), "PUT", path, "image/png", testPNG(t, 4, 4)); w.Code != http.StatusForbidden {
		t.Fatalf("stale admin: %d", w.Code)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	// n bytes that start like a PNG and are not one.
	signed := func(n int) []byte {
		b := make([]byte, n)
		copy(b, "\x89PNG\r\n\x1a\n")
		return b
	}
	for name, tc := range map[string]struct {
		contentType string
		body        []byte
		code        int
		outcome     string
	}{
		"SVG":            {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 415, "refused: the logo must be a PNG image (image/png)"},
		"no type":        {"", testPNG(t, 4, 4), 415, "refused: the logo must be a PNG image (image/png)"},
		"JPEG as PNG":    {"image/png", jpg.Bytes(), 400, "refused: " + branding.ErrNotPNG.Error()},
		"1025 px":        {"image/png", testPNG(t, 1025, 1), 400, "refused: " + branding.ErrLogoDimensions.Error()},
		"exactly 1 MiB":  {"image/png", signed(1 << 20), 400, "refused: " + branding.ErrNotPNG.Error()},
		"1 MiB + 1 byte": {"image/png", signed(1<<20 + 1), 413, "refused: " + branding.ErrLogoTooLarge.Error()},
	} {
		if w := rawDo(t, srv, admin, "PUT", path, tc.contentType, tc.body); w.Code != tc.code {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if rows := auditRows(t, st, "admin.brand_logo"); len(rows) == 0 || rows[0].Details != fmt.Sprintf("outcome=%q", tc.outcome) {
			t.Errorf("%s: newest audit row %+v", name, rows)
		}
	}
	if _, err := st.Settings().GetSetting(ctx, "brand_logo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused logo was stored: %v", err)
	}
}

// Review Focus 3: the minute tick neither rewrites a matching file nor logs every minute.
func TestReconcileWritesOnlyOnChangeAndLogsOncePerStreak(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p := withElement(t, cfg, elementWith(cfg.Server.AppName))
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	old := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := srv.ReconcileBrand(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fi, _ := os.Stat(p); !fi.ModTime().Equal(old) {
		t.Fatal("an unchanged brand was rewritten")
	}
	if err := st.Settings().SetSetting(ctx, "brand_name", "Acme"); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != elementWith("Acme") {
		t.Fatalf("not reconciled: %s", b)
	}
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := srv.ReconcileBrand(ctx); err == nil {
			t.Fatal("a broken config reconciled")
		}
	}
	if err := os.WriteFile(p, []byte(elementJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatal(err)
	}
	for line, want := range map[string]int{"Element brand not updated": 1, "Element brand updated again": 1} {
		if n := strings.Count(logs.String(), line); n != want {
			t.Errorf("%q logged %d times, want %d:\n%s", line, n, want, logs.String())
		}
	}
	// Without Matrix nothing is read or written.
	cfg.Matrix.ServerName = ""
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatalf("without Matrix: %v", err)
	}
}

// Review Focus 4: a save and a tick are two writers of one file; they must not interleave.
// Run it with -race: without brandMu the writes rarely interleave visibly, but the streak
// fields race every time.
func TestConcurrentSavesAndTicksLeaveValidConfig(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p := withElement(t, cfg, elementJSON)
	admin := loginAs(t, srv, st, "root", "admin")
	names := []string{"A", strings.Repeat("Long brand ", 5) + "end", "Mid size"}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			adminDo(t, srv, admin, "PUT", "/api/admin/branding/name", map[string]string{"name": names[i%len(names)]})
		}()
		go func() {
			defer wg.Done()
			_ = srv.ReconcileBrand(ctx)
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(p)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("two writers interleaved: %v\n%s", err, b)
	}
	stored, err := st.Settings().GetSetting(ctx, "brand_name")
	if err != nil || got["brand"] != stored || got["disable_guests"] != true {
		t.Fatalf("brand %v, stored %q (%v)", got["brand"], stored, err)
	}
}

// Review Focus 5: KY_APP_NAME is the service name capsules are sealed under and KyRecovery
// pins; the console name is display only.
func TestBrandNameNeverRenamesTheService(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	decode[brandingBody](t, adminDo(t, srv, admin, "PUT", "/api/admin/branding/name", map[string]string{"name": "Acme"}))
	if got := statusOf(t, srv, admin)["app_name"]; got != cfg.Server.AppName {
		t.Fatalf("backup status names the service %v, want %q", got, cfg.Server.AppName)
	}
}

// Review Focus 1: the logo would ride on every admin page load.
func TestSettingsCarryTheEffectiveNameButNotTheLogo(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	for k, v := range map[string]string{"brand_name": "Acme", "brand_logo": strings.Repeat("A", 1<<20)} {
		if err := st.Settings().SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	w := do(t, srv, "GET", "/api/settings", loginAs(t, srv, st, "root", "admin"))
	var out struct {
		AppName string            `json:"app_name"`
		Extra   map[string]string `json:"extra_settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if _, found := out.Extra["brand_logo"]; out.AppName != "Acme" || out.Extra == nil || found || w.Body.Len() > 64<<10 {
		t.Fatalf("app_name %q, logo in extra_settings %v, %d bytes", out.AppName, found, w.Body.Len())
	}
}

func TestBrandingWithoutMatrixHasNoElement(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "GET", "/api/admin/branding", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"element"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
```

In `internal/api/authz_test.go`, add to the `cases` of `TestPrivilegedEndpointsRequireAdmin`, after `{"POST", "/api/settings/theme"},`:

```go
		{"GET", "/api/admin/branding"},
		{"PUT", "/api/admin/branding/name"},
		{"PUT", "/api/admin/branding/logo"},
		{"DELETE", "/api/admin/branding/logo"},
```

- [ ] **Step 2: Run them to see them fail.** Run `go test ./internal/api/ -run 'Brand|Logo|Reconcile|Concurrent|SettingsCarry|PrivilegedEndpoints' -v`. Expected: FAIL to build with `srv.ReconcileBrand undefined (type *api.Server has no field or method ReconcileBrand)`.

- [ ] **Step 3: Implement the handlers.** Create `internal/api/branding_handlers.go`:

```go
package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/branding"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Settings behind the console's branding; brand_logo holds the normalised PNG in base64.
const (
	brandNameKey = "brand_name"
	brandLogoKey = "brand_logo"
)

// brandTimeout bounds a branding change's store writes, Element patch and audit row. They run
// detached, so a dropped connection cannot save a change without its audit row.
const brandTimeout = 10 * time.Second

// effectiveName is the admin's brand_name, else KY_APP_NAME. Display only: capsules, pairing
// and local copies keep KY_APP_NAME, which KyRecovery and restores match byte for byte.
func (s *Server) effectiveName(ctx context.Context) (string, error) {
	v, err := s.store.Settings().GetSetting(ctx, brandNameKey)
	if errors.Is(err, store.ErrNotFound) {
		return s.config.Server.AppName, nil
	}
	return cmp.Or(v, s.config.Server.AppName), err
}

// logo returns the stored PNG, or nil when none is set.
func (s *Server) logo(ctx context.Context) ([]byte, error) {
	v, err := s.store.Settings().GetSetting(ctx, brandLogoKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

func (s *Server) elementConfig() string {
	return filepath.Join(s.config.Matrix.Dir, "element", "config.json")
}

// ReconcileBrand sets Element's brand to the effective name. brandMu makes it the file's only
// writer in this process: the name handler and cmd/server's maintenance tick both call it, and
// two in-place writers could interleave into invalid JSON. It writes only on change, never
// creates the file, and logs a failure once per streak. No-op without Matrix.
func (s *Server) ReconcileBrand(ctx context.Context) error {
	if !s.config.Matrix.Enabled() {
		return nil
	}
	s.brandMu.Lock()
	defer s.brandMu.Unlock()
	name, err := s.effectiveName(ctx)
	if err == nil {
		var changed bool
		if changed, err = branding.PatchElementBrand(s.elementConfig(), name); changed {
			log.Printf("[BRANDING] Element brand set to %q", name)
		}
	}
	switch {
	case err != nil && !s.brandFailing:
		log.Printf("[BRANDING] Element brand not updated (retrying every minute): %v", err)
	case err == nil && s.brandFailing:
		log.Printf("[BRANDING] Element brand updated again")
	}
	s.brandFailing, s.brandErr = err != nil, ""
	if err != nil {
		s.brandErr = err.Error()
	}
	return err
}

type logoView struct {
	Custom bool   `json:"custom"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// elementView is what Element's config.json says now; Error is why it differs or is unreadable.
type elementView struct {
	Brand *string `json:"brand,omitempty"`
	Error string  `json:"error,omitempty"`
}

type brandingView struct {
	Name        string       `json:"name"`
	StoredName  string       `json:"stored_name"`
	DefaultName string       `json:"default_name"`
	Logo        logoView     `json:"logo"`
	Element     *elementView `json:"element,omitempty"` // only with Matrix
}

func (s *Server) brandingState(ctx context.Context) (brandingView, error) {
	stored, err := s.store.Settings().GetSetting(ctx, brandNameKey)
	if errors.Is(err, store.ErrNotFound) {
		stored, err = "", nil
	}
	if err != nil {
		return brandingView{}, err
	}
	v := brandingView{Name: cmp.Or(stored, s.config.Server.AppName), StoredName: stored, DefaultName: s.config.Server.AppName}
	png, err := s.logo(ctx)
	if err != nil {
		return brandingView{}, err
	}
	if png != nil {
		sum := sha256.Sum256(png)
		v.Logo = logoView{Custom: true, SHA256: hex.EncodeToString(sum[:]), Size: len(png)}
	}
	if s.config.Matrix.Enabled() {
		e := &elementView{}
		if brand, err := branding.ElementBrand(s.elementConfig()); err != nil {
			e.Error = err.Error()
		} else {
			e.Brand = &brand
		}
		s.brandMu.Lock()
		if e.Brand != nil && *e.Brand != v.Name {
			e.Error = s.brandErr
		}
		s.brandMu.Unlock()
		v.Element = e
	}
	return v, nil
}

func (s *Server) writeBranding(ctx context.Context, w http.ResponseWriter) {
	v, err := s.brandingState(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read branding")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	s.writeBranding(r.Context(), w)
}

// handleBrandName saves the product name (blank resets it to KY_APP_NAME), then patches
// Element. An Element failure leaves the name saved; the answer shows what Element says.
func (s *Server) handleBrandName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
	}
	if !decodeStrict(w, r, &req) || req.Name == nil {
		s.writeError(w, http.StatusBadRequest, `Body must be {"name": "..."}`)
		return
	}
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), brandTimeout)
	defer cancel()
	old, _ := s.effectiveName(ctx) // audit detail only
	record := func(outcome, name string) {
		// 40 bytes each: %q can double a name, and details are capped at 200 bytes.
		s.audit(ctx, actor, r, "admin.brand_name", "", auditFields(outcome, "old", clipTo(old, 40), "new", clipTo(name, 40)))
	}
	name := ""
	if strings.TrimSpace(*req.Name) != "" {
		v, err := branding.ValidateName(*req.Name)
		if err != nil {
			record("refused: "+err.Error(), "")
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		name = v
	}
	var err error
	if name == "" {
		err = s.store.Settings().DeleteSetting(ctx, brandNameKey)
	} else {
		err = s.store.Settings().SetSetting(ctx, brandNameKey, name)
	}
	if err != nil {
		record("error: "+err.Error(), name)
		s.writeError(w, http.StatusInternalServerError, "Could not save the name")
		return
	}
	outcome := "saved"
	if err := s.ReconcileBrand(ctx); err != nil {
		outcome = "saved; Element not updated: " + err.Error()
	}
	record(outcome, cmp.Or(name, s.config.Server.AppName))
	s.writeBranding(ctx, w)
}

// handleBrandLogo stores an uploaded PNG, normalised: decoded and re-encoded so no ancillary
// chunk survives. Only image/png (SVG can carry script). Refusals are audited, never the bytes.
func (s *Server) handleBrandLogo(w http.ResponseWriter, r *http.Request) {
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), brandTimeout)
	defer cancel()
	record := func(outcome string, kv ...string) {
		s.audit(ctx, actor, r, "admin.brand_logo", "", auditFields(outcome, kv...))
	}
	refuse := func(status int, reason string) {
		record("refused: " + reason)
		s.writeError(w, status, reason)
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "image/png" {
		refuse(http.StatusUnsupportedMediaType, "the logo must be a PNG image (image/png)")
		return
	}
	// Its own cap, not only ServeHTTP's: this is the logo's limit, whatever the API's becomes.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, branding.MaxLogoBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		refuse(http.StatusRequestEntityTooLarge, branding.ErrLogoTooLarge.Error())
		return
	}
	if err != nil {
		refuse(http.StatusBadRequest, "the upload could not be read")
		return
	}
	png, err := branding.NormalizePNG(body)
	if errors.Is(err, branding.ErrLogoTooLarge) {
		refuse(http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		refuse(http.StatusBadRequest, err.Error())
		return
	}
	sum := sha256.Sum256(png)
	digest, size := hex.EncodeToString(sum[:]), strconv.Itoa(len(png))
	if err := s.store.Settings().SetSetting(ctx, brandLogoKey, base64.StdEncoding.EncodeToString(png)); err != nil {
		record("error: "+err.Error(), "sha256", digest, "size", size)
		s.writeError(w, http.StatusInternalServerError, "Could not save the logo")
		return
	}
	record("saved", "sha256", digest, "size", size)
	s.writeBranding(ctx, w)
}

// handleBrandLogoReset returns /app-icon.png to the embedded stamp.
func (s *Server) handleBrandLogoReset(w http.ResponseWriter, r *http.Request) {
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), brandTimeout)
	defer cancel()
	outcome := "reset"
	err := s.store.Settings().DeleteSetting(ctx, brandLogoKey)
	if err != nil {
		outcome = "error: " + err.Error()
	}
	s.audit(ctx, actor, r, "admin.brand_logo", "", auditFields(outcome))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not reset the logo")
		return
	}
	s.writeBranding(ctx, w)
}

// appIcon serves the admin's logo at /app-icon.png, else the embedded stamp. Public: the login
// page and Element's sign-in page show it. no-cache with an ETag: browsers revalidate on every
// load, so a new logo or a reset shows at once without re-downloading an unchanged one.
func (s *Server) appIcon(static http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		png, err := s.logo(r.Context())
		if err != nil || png == nil {
			static.ServeHTTP(w, r)
			return
		}
		sum := sha256.Sum256(png)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		http.ServeContent(w, r, "app-icon.png", time.Time{}, bytes.NewReader(png))
	}
}

// clipTo bounds v to n bytes of valid UTF-8.
func clipTo(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return strings.ToValidUTF8(v[:n], "")
}
```

- [ ] **Step 4: Wire the server.** In `internal/api/server.go`:
  - (a) Add to the `Server` struct, after `detached detachedCounter`:

    ```go
    	// brandMu serialises Element brand writes: the name handler and the maintenance tick both
    	// patch one file. brandFailing and brandErr describe the current failure streak.
    	brandMu      sync.Mutex
    	brandFailing bool
    	brandErr     string
    ```

  - (b) In `routes()`, after `s.mux.HandleFunc("/api/settings/theme", s.requireAdmin(s.handleSetTheme))`, add:

    ```go
    	// Branding. Any admin reads it; changes need a recent sign-in, run detached and are audited.
    	s.mux.HandleFunc("GET /api/admin/branding", s.requireAdmin(s.handleBranding))
    	s.mux.HandleFunc("PUT /api/admin/branding/name", s.tracked(s.requireFreshAdmin(s.handleBrandName)))
    	s.mux.HandleFunc("PUT /api/admin/branding/logo", s.tracked(s.requireFreshAdmin(s.handleBrandLogo)))
    	s.mux.HandleFunc("DELETE /api/admin/branding/logo", s.tracked(s.requireFreshAdmin(s.handleBrandLogoReset)))
    ```

  - (c) Replace the last two lines of `routes()` (`// Embedded React PWA Frontend` and `s.mux.Handle("/", web.Handler())`) with:

    ```go
    	// Embedded React PWA Frontend. /app-icon.png is the admin's logo when one is set; public.
    	static := web.Handler()
    	s.mux.HandleFunc("GET /app-icon.png", s.appIcon(static))
    	s.mux.Handle("/", static)
    ```

- [ ] **Step 5: The effective name and the filter in `/api/settings`.** In `internal/api/settings_handlers.go`:
  - (a) Add above `handleGetSettings`:

    ```go
    // notExtra are settings extra_settings never carries: the logo is up to 1 MiB and is served
    // at /app-icon.png.
    var notExtra = map[string]bool{brandLogoKey: true}
    ```

  - (b) At the top of `handleGetSettings`, after the theme lines, add:

    ```go
    	// The login screen must render: a read error shows the default name.
    	name, err := s.effectiveName(r.Context())
    	if err != nil {
    		name = s.config.Server.AppName
    	}
    ```

  - (c) Change `"app_name":         s.config.Server.AppName,` to `"app_name":         name,`. (`user, _, err := s.sessions.AuthenticateRequest(r)` still compiles: `user` is new, `err` is reused.)
  - (d) Change the filter condition `if strings.HasPrefix(k, "kyrecovery_token") {` to `if strings.HasPrefix(k, "kyrecovery_token") || notExtra[k] {`.

- [ ] **Step 6: Run the API tests to see them pass.** Run `go test -race ./internal/api/ -run 'Brand|Logo|Reconcile|Concurrent|SettingsCarry|SettingsExposure|PrivilegedEndpoints' -v`. Expected: PASS. Then run `go test -race ./internal/api/`. Expected: PASS, the whole package.

- [ ] **Step 7: Write the failing maintenance test.** In `cmd/server/maintenance_test.go`, change the call in `TestMaintenanceLoopClosesDoneOnCancel` to `go maintenanceLoop(ctx, nil, nil, done)` and append:

```go
// The brand is reconciled when the loop starts, under a deadline, and a failure (which the
// reconciler logs itself) does not stop the loop.
func TestMaintenanceLoopReconcilesBrandAtStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	done := make(chan struct{})
	go maintenanceLoop(ctx, nil, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("brand reconcile runs without a deadline")
		}
		called <- struct{}{}
		return errors.New("logged by the reconciler")
	}, done)
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the brand was not reconciled at start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done not closed")
	}
}
```

- [ ] **Step 8: Run it to see it fail.** Run `go test ./cmd/server/ -run Maintenance -v`. Expected: FAIL to build, `too many arguments in call to maintenanceLoop`.

- [ ] **Step 9: Implement.** Replace `maintenanceLoop` in `cmd/server/maintenance.go` (keep `sweepPairings`):

```go
// brandTimeout bounds one Element brand reconcile: a small local file.
const brandTimeout = 10 * time.Second

// maintenanceLoop reconciles Element's brand at its start, then once a minute deletes expired
// QR pairings and reconciles the brand again. brand is api.Server.ReconcileBrand (a no-op
// without Matrix, logging its own failures once per streak); nil skips it. done closes between
// ticks, so shutdown can wait for it before the store closes.
func maintenanceLoop(ctx context.Context, st store.Store, brand func(context.Context) error, done chan<- struct{}) {
	defer close(done)
	reconcileBrand(ctx, brand)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepPairings(ctx, st)
			reconcileBrand(ctx, brand)
		}
	}
}

func reconcileBrand(ctx context.Context, brand func(context.Context) error) {
	if brand == nil || ctx.Err() != nil {
		return
	}
	run, cancel := context.WithTimeout(ctx, brandTimeout)
	defer cancel()
	_ = brand(run) // the reconciler logs; the next tick retries
}
```

In `cmd/server/main.go`, change `go maintenanceLoop(ctx, st, maintenanceDone)` to `go maintenanceLoop(ctx, st, srv.ReconcileBrand, maintenanceDone)`.

- [ ] **Step 10: Run them to see them pass.** Run `go test -race ./cmd/server/ ./internal/api/`. Expected: PASS. Run `go vet ./...`. Expected: no output.

- [ ] **Step 11: DOX.**
  - In `internal/api/AGENTS.md`, replace the sentence `KyRecovery tokens are omitted in both sealed and legacy plaintext forms, dropped by the \`kyrecovery_token\` key prefix rather than by literal key name.` with:

    ```markdown
    KyRecovery tokens are omitted in both sealed and legacy plaintext forms, dropped by the `kyrecovery_token` key prefix rather than by literal key name. `extra_settings` never carries the keys in `notExtra` (`brand_logo`, served at `/app-icon.png`). `app_name` is the effective name (`brand_name`, else `KY_APP_NAME`; the default on a store error).
    ```

  - Append these bullets to its Local Contracts:

    ```markdown
    - Branding (`branding_handlers.go`): `GET /api/admin/branding` (admin, `no-store`) answers `{name, stored_name, default_name, logo:{custom, sha256, size}, element}`; `element` (`{brand?, error?}`, read from Element's file, with the last reconcile error while the brand differs) is present only with Matrix configured. `PUT /api/admin/branding/name` (`{"name"}`, strict; blank resets to `KY_APP_NAME`; 400 when `branding.ValidateName` refuses), `PUT /api/admin/branding/logo` (raw body: 415 unless `image/png`, 413 over 1 MiB through the handler's own `MaxBytesReader`, 400 when `branding.NormalizePNG` refuses) and `DELETE /api/admin/branding/logo` are `requireFreshAdmin`, `tracked`, run on a detached 10 s context and answer the same view. Settings: `brand_name`, and `brand_logo` (base64 of the normalised PNG); both travel in the capsule. The effective name is display only: capsules, pairing, local copies and backup status keep `KY_APP_NAME` (`TestBrandNameNeverRenamesTheService`). Audit `admin.brand_name` (`outcome old new`, names clipped to 40 bytes) and `admin.brand_logo` (`outcome sha256 size`, or `outcome="reset"`), success and failure after authentication and input parsing; refusals are `refused: <reason>`, never the bytes. A malformed name body is not audited.
    - `Server.ReconcileBrand` patches Element's `brand` (`<KY_MATRIX_DIR>/element/config.json`) to the effective name under `brandMu`, the file's only writer in this process: after each name save, and from `cmd/server`'s maintenance loop at start and every minute. It never creates the file, writes only on change, refuses to write on a store read error, and logs a failure once per streak; the saved name stands, and Settings shows Element's value and the error. It is a no-op without Matrix.
    - `GET /app-icon.png` (public: the login page and Element's sign-in page) serves the stored logo as `image/png` with `Cache-Control: no-cache` and an `ETag` of the quoted SHA-256 (304 on `If-None-Match`, via `http.ServeContent`), else the embedded stamp, also `no-cache`, so a new logo or a reset shows on the next load.
    ```

  - In the root `AGENTS.md`, replace `` `Shutdown`. `maintenanceLoop` sweeps expired device pairings every minute with a 30-second `` with:

    ```markdown
    `Shutdown`. `maintenanceLoop` reconciles Element's `brand` through `api.Server.ReconcileBrand` (10-second deadline; a no-op without Matrix) at its start and every minute, and sweeps expired device pairings every minute with a 30-second
    ```

- [ ] **Step 12: Commit.** Run `gofmt -w internal/api cmd/server && gofmt -l internal/api cmd/server` (expected: no output). Commit `internal/api/`, `cmd/server/` and `AGENTS.md`. Message: `api: branding routes, /app-icon.png and the Element brand reconcile`.

---

### Task 4: Sync records, rejection counters and `GET /api/admin/matrix/sync-status`

**Files:**
- Create: `internal/sso/syncstatus.go`, `internal/sso/syncstatus_test.go`
- Modify: `internal/sso/kyidentity.go` (`KyIdentityClient` struct, `HandleSyncWebhook`)
- Create: `internal/matrixsync/record.go`, `internal/matrixsync/record_test.go`
- Modify: `internal/matrixsync/matrixsync.go` (`Run`, `Sweep`)
- Create: `internal/api/sync_handlers.go`, `internal/api/sync_status_test.go`
- Modify: `internal/api/server.go` (`routes()`), `internal/api/settings_handlers.go` (`notExtra`), `internal/api/authz_test.go`, `internal/api/console_test.go` (`TestMatrixRoutesAre404WithoutMatrix`)
- Modify: `internal/sso/AGENTS.md`, `internal/matrixsync/AGENTS.md`, `internal/api/AGENTS.md`

**Interfaces:**
- Consumes: `notExtra` (Task 3); `matrixOn`, `store.ErrNotFound`.
- Produces:
  - `sso.WebhookRecordKey = "kyidentity_webhook_last"`, `type sso.WebhookRecord struct{ At time.Time "at"; Kind string "kind" }`
  - `type sso.Rejections struct{ Count int64 "count"; LastAt *time.Time "last_at"; LastReason string "last_reason" }`, `func (k *KyIdentityClient) Rejections() Rejections`
  - `sso.RejectNotConfigured|RejectBadSignature|RejectStale|RejectBadHeaders|RejectMalformed` = `"not_configured"|"bad_signature"|"stale"|"bad_headers"|"malformed"`
  - `matrixsync.SweepRecordKey = "matrix_sweep_last"`, `type matrixsync.SweepRecord struct{ FinishedAt time.Time "finished_at"; OK bool "ok"; Error string "error"; Applied, Failed int "applied","failed"; FailingSince *time.Time "failing_since" }`
  - JSON of `GET /api/admin/matrix/sync-status`: `{"webhook": {at, kind}|null, "rejected": {count, last_at|null, last_reason}, "sweep": {finished_at, ok, error, applied, failed, failing_since|null}|null, "kyidentity_url": string}`. Task 5 parses it.

- [ ] **Step 1: Write the failing sso tests.** Create `internal/sso/syncstatus_test.go`:

```go
package sso_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
)

func TestDeliveriesAreRecordedAndRejectionsCountedByReason(t *testing.T) {
	ctx := context.Background()
	d := newDirectory(t)
	if r := d.client.Rejections(); r.Count != 0 || r.LastAt != nil || r.LastReason != "" {
		t.Fatalf("fresh counters: %+v", r)
	}
	if _, err := d.st.Settings().GetSetting(ctx, sso.WebhookRecordKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a record before any delivery: %v", err)
	}
	d.must("user.created", scimUser("kid-1", "user", true, 1))
	raw, err := d.st.Settings().GetSetting(ctx, sso.WebhookRecordKey)
	var rec sso.WebhookRecord
	if err != nil || json.Unmarshal([]byte(raw), &rec) != nil || rec.Kind != "user.created" || time.Since(rec.At) > time.Minute {
		t.Fatalf("record %q: %v", raw, err)
	}
	body := scimUser("kid-eve", "admin", true, 1)
	h, err := syncauth.Sign([]byte(webhookSecret), time.Now(), "user.updated", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	malformed := []byte(`{"id":"x"}`)
	for i, tc := range []struct {
		name string
		h    syncauth.Headers
		body []byte
		want string
	}{
		{"tampered body", h, scimUser("kid-eve", "admin", true, 2), sso.RejectBadSignature},
		{"no signature", syncauth.Headers{Timestamp: h.Timestamp, EventType: h.EventType, EventID: h.EventID}, body, sso.RejectBadSignature},
		{"stale", signAt(t, time.Now().Add(-10*time.Minute), body), body, sso.RejectStale},
		{"no event ID", syncauth.Headers{Signature: h.Signature, Timestamp: h.Timestamp, EventType: h.EventType}, body, sso.RejectBadHeaders},
		{"bad timestamp", syncauth.Headers{Signature: h.Signature, Timestamp: "yesterday", EventType: h.EventType, EventID: h.EventID}, body, sso.RejectBadHeaders},
		{"signed but malformed", signAt(t, time.Now(), malformed), malformed, sso.RejectMalformed},
	} {
		if err := d.client.HandleSyncWebhook(ctx, tc.h, tc.body); err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		if r := d.client.Rejections(); r.Count != int64(i+1) || r.LastReason != tc.want || r.LastAt == nil {
			t.Errorf("%s: %+v, want reason %s", tc.name, r, tc.want)
		}
	}
	for _, secret := range []string{"", "too-short"} {
		c := sso.NewKyIdentityClient(config.SSOConfig{KyIdentityHMACSecret: secret}, d.st)
		_ = c.HandleSyncWebhook(ctx, h, body)
		if r := c.Rejections(); r.Count != 1 || r.LastReason != sso.RejectNotConfigured {
			t.Errorf("secret %q: %+v", secret, r)
		}
	}
	if again, _ := d.st.Settings().GetSetting(ctx, sso.WebhookRecordKey); again != raw {
		t.Fatalf("a refused delivery changed the record to %q", again)
	}
	d.restart()
	if r := d.client.Rejections(); r.Count != 0 {
		t.Fatalf("counters survived a restart: %+v", r)
	}
}

type countingSettings struct {
	store.SettingsStore
	mu     sync.Mutex
	writes int
}

func (c *countingSettings) SetSetting(ctx context.Context, k, v string) error {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return c.SettingsStore.SetSetting(ctx, k, v)
}

type countingStore struct {
	store.Store
	settings *countingSettings
}

func (c countingStore) Settings() store.SettingsStore { return c.settings }

// Refused deliveries are unauthenticated: they cost no database write.
func TestRejectedDeliveriesWriteNoSetting(t *testing.T) {
	ctx := context.Background()
	d := newDirectory(t)
	cs := &countingSettings{SettingsStore: d.st.Settings()}
	client := sso.NewKyIdentityClient(config.SSOConfig{KyIdentityHMACSecret: webhookSecret}, countingStore{Store: d.st, settings: cs})
	body := scimUser("kid-r", "user", true, 1)
	for range 5 {
		_ = client.HandleSyncWebhook(ctx, syncauth.Headers{Signature: "v1=bad", Timestamp: time.Now().UTC().Format(time.RFC3339),
			EventType: "user.created", EventID: uuid.NewString()}, body)
	}
	if cs.writes != 0 || client.Rejections().Count != 5 {
		t.Fatalf("after 5 refusals: %d setting writes, %+v", cs.writes, client.Rejections())
	}
	h, err := syncauth.Sign([]byte(webhookSecret), time.Now(), "user.created", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HandleSyncWebhook(ctx, h, body); err != nil {
		t.Fatal(err)
	}
	if cs.writes != 1 {
		t.Fatalf("an accepted delivery wrote %d settings, want its one record", cs.writes)
	}
}
```

- [ ] **Step 2: Run them to see them fail.** Run `go test ./internal/sso/ -run 'Deliveries|RejectedDeliveries' -v`. Expected: FAIL to build, `d.client.Rejections undefined` and `undefined: sso.WebhookRecordKey`.

- [ ] **Step 3: Implement the webhook record and the counters.** Create `internal/sso/syncstatus.go`:

```go
package sso

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
)

// WebhookRecordKey is the setting written after each acknowledged directory delivery, by this
// package alone; the console reads it.
const WebhookRecordKey = "kyidentity_webhook_last"

// WebhookRecord is the last acknowledged delivery: when, and its signed event type.
type WebhookRecord struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

// Why a delivery was refused, as the console shows it.
const (
	RejectNotConfigured = "not_configured" // no secret, or one shorter than syncauth.MinKeyBytes
	RejectBadSignature  = "bad_signature"  // no signature, or one that does not match
	RejectStale         = "stale"          // timestamp outside the ±5-minute window
	RejectBadHeaders    = "bad_headers"    // event type, ID or timestamp missing or unparseable
	RejectMalformed     = "malformed"      // signed, but not a usable SCIM user
)

// Rejections counts refused deliveries since this process started. Memory only: their senders
// are unauthenticated, so nothing about them reaches the database or the audit log.
type Rejections struct {
	Count      int64      `json:"count"`
	LastAt     *time.Time `json:"last_at"`
	LastReason string     `json:"last_reason"`
}

type rejectCounter struct {
	mu   sync.Mutex
	last Rejections
}

// Rejections returns the refusal counters.
func (k *KyIdentityClient) Rejections() Rejections {
	k.rejects.mu.Lock()
	defer k.rejects.mu.Unlock()
	return k.rejects.last
}

func (k *KyIdentityClient) reject(err error) {
	now := time.Now().UTC()
	k.rejects.mu.Lock()
	defer k.rejects.mu.Unlock()
	k.rejects.last = Rejections{Count: k.rejects.last.Count + 1, LastAt: &now, LastReason: rejectReason(err)}
}

func rejectReason(err error) string {
	switch {
	case errors.Is(err, ErrSyncMalformed):
		return RejectMalformed
	case errors.Is(err, errNoSecret), errors.Is(err, syncauth.ErrShortKey):
		return RejectNotConfigured
	case errors.Is(err, syncauth.ErrStale):
		return RejectStale
	case errors.Is(err, syncauth.ErrNoSignature), errors.Is(err, syncauth.ErrBadSignature):
		return RejectBadSignature
	default: // syncauth.ErrMissingFields, syncauth.ErrBadTimestamp
		return RejectBadHeaders
	}
}

// recordDelivery notes an acknowledged delivery. A failed write is logged, never a failed
// delivery: the event is already applied.
func (k *KyIdentityClient) recordDelivery(ctx context.Context, kind string) {
	if len(kind) > 64 { // signed by KyIdentity; bounded anyway
		kind = strings.ToValidUTF8(kind[:64], "")
	}
	b, err := json.Marshal(WebhookRecord{At: time.Now().UTC(), Kind: kind})
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := k.store.Settings().SetSetting(wctx, WebhookRecordKey, string(b)); err != nil {
		log.Printf("[SSO] directory webhook applied; its time was not recorded: %v", err)
	}
}
```

In `internal/sso/kyidentity.go`:
  - (a) Add to `KyIdentityClient`, after `syncMu sync.Mutex`:

    ```go
    	// rejects counts refused deliveries since start, in memory only.
    	rejects rejectCounter
    ```

  - (b) Add to the `var (...)` block with `ErrSyncUnauthorized`:

    ```go
    	// errNoSecret: the receiver has no key, so nothing can be verified.
    	errNoSecret = errors.New("KY_KYIDENTITY_HMAC_SECRET is not set")
    ```

  - (c) Replace the whole `HandleSyncWebhook` function, from its doc comment through `return nil // other event types (groups) are not for this product` and the closing brace, with:

```go
// HandleSyncWebhook applies one signed directory event from KyIdentity. Superseded and
// duplicate events succeed without effect, so the sender's outbox stops retrying them. An
// acknowledged delivery is recorded under WebhookRecordKey; a refused one is only counted, in
// memory (Rejections).
func (k *KyIdentityClient) HandleSyncWebhook(ctx context.Context, headers syncauth.Headers, body []byte) error {
	kind, err := k.applySyncWebhook(ctx, headers, body)
	switch {
	case err == nil:
		k.recordDelivery(ctx, kind)
	case errors.Is(err, ErrSyncUnauthorized), errors.Is(err, ErrSyncMalformed):
		k.reject(err)
	}
	return err
}

// applySyncWebhook verifies and applies one event and returns its signed type. The cause of
// ErrSyncUnauthorized stays reachable with errors.Is, so refusals can be told apart.
func (k *KyIdentityClient) applySyncWebhook(ctx context.Context, headers syncauth.Headers, body []byte) (string, error) {
	if k.config.KyIdentityHMACSecret == "" {
		return "", fmt.Errorf("%w: %w", ErrSyncUnauthorized, errNoSecret)
	}
	event, err := syncauth.Verify([]byte(k.config.KyIdentityHMACSecret), headers, body, syncauth.Options{})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSyncUnauthorized, err)
	}
	var user DirectoryUser
	if err := json.Unmarshal(body, &user); err != nil || user.ID == "" || user.Meta == nil {
		return "", ErrSyncMalformed
	}
	match := revisionPattern.FindStringSubmatch(user.Meta.Version)
	if match == nil {
		return "", ErrSyncMalformed
	}
	revision, _ := strconv.ParseInt(match[1], 10, 64)
	ev := store.DirectoryEvent{ID: event.ID, Revision: revision}

	k.syncMu.Lock()
	defer k.syncMu.Unlock()
	switch event.Type {
	case "user.created", "user.updated", "user.mfa_reset":
		return event.Type, k.upsertDirectoryUser(ctx, user, ev)
	case "user.deleted":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kyidentity", user.ID)
		if errors.Is(err, store.ErrNotFound) {
			// Record the deletion anyway, so an older creation delivered later cannot
			// bring the account into existence. The delete matches no row.
			existing, err = &store.User{SSOProvider: "kyidentity", SSOSubject: user.ID}, nil
		}
		if err != nil {
			return event.Type, err
		}
		_, err = k.store.Users().DeleteDirectoryUser(ctx, existing, ev)
		return event.Type, err
	}
	return event.Type, nil // other event types (groups) are not for this product
}
```

- [ ] **Step 4: Run the sso tests.** Run `go test -race ./internal/sso/ -v`. Expected: PASS, the existing webhook tests included: refusals still match `ErrSyncUnauthorized`.

- [ ] **Step 5: Write the failing sweep-record tests.** Create `internal/matrixsync/record_test.go`:

```go
package matrixsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

func readRecord(t *testing.T, st store.Store) (SweepRecord, bool) {
	t.Helper()
	raw, err := st.Settings().GetSetting(context.Background(), SweepRecordKey)
	if errors.Is(err, store.ErrNotFound) {
		return SweepRecord{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var rec SweepRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("record %q: %v", raw, err)
	}
	return rec, true
}

// waitRecord polls until the stored record satisfies ok.
func waitRecord(t *testing.T, st store.Store, ok func(SweepRecord) bool) SweepRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if rec, found := readRecord(t, st); found && ok(rec) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatal("no matching sweep record")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunRecordsEverySweep(t *testing.T) {
	f := &fakeMAS{failOn: "lock U2"}
	s, st := newSyncer(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go s.Run(ctx, time.Hour, done)
	first := waitRecord(t, st, func(r SweepRecord) bool { return !r.OK })
	if first.Applied != 3 || first.Failed != 1 || first.FailingSince == nil || !strings.Contains(first.Error, "lock bob") {
		t.Fatalf("failed sweep: %+v", first)
	}
	f.mu.Lock()
	f.failOn = ""
	f.mu.Unlock()
	s.Wake()
	second := waitRecord(t, st, func(r SweepRecord) bool { return r.OK })
	if second.Applied != 4 || second.Failed != 0 || second.FailingSince != nil || second.Error != "" {
		t.Fatalf("ok sweep: %+v", second)
	}
	cancel()
	<-done
}

func TestSweepRecordKeepsTheStreakAndBoundsTheError(t *testing.T) {
	ctx := context.Background()
	s, st := newSyncer(t, &fakeMAS{})
	since := s.record(ctx, 0, 1, errors.New(strings.Repeat("é", 400)), nil)
	rec, _ := readRecord(t, st)
	if since == nil || rec.FailingSince == nil || !rec.FailingSince.Equal(*since) || rec.Error == "" ||
		len(rec.Error) > 300 || !utf8.ValidString(rec.Error) {
		t.Fatalf("first failure: %+v", rec)
	}
	if again := s.record(ctx, 0, 1, errors.New("still failing"), since); again != since {
		t.Fatal("a second failure restarted the streak")
	}
	// A restart carries the streak on from the stored record.
	if got := New(&fakeMAS{}, st, "example.com").storedStreak(ctx); got == nil || !got.Equal(*since) {
		t.Fatalf("restart lost the streak: %v", got)
	}
	if s.record(ctx, 4, 0, nil, since) != nil {
		t.Fatal("a success kept the streak")
	}
	if rec, _ := readRecord(t, st); !rec.OK || rec.FailingSince != nil || rec.Error != "" || rec.Applied != 4 {
		t.Fatalf("after success: %+v", rec)
	}
	if New(&fakeMAS{}, st, "example.com").storedStreak(ctx) != nil {
		t.Fatal("an ok record seeded a streak")
	}
}

type brokenSettings struct{ store.SettingsStore }

func (brokenSettings) SetSetting(context.Context, string, string) error { return errors.New("disk full") }

type brokenSettingsStore struct{ store.Store }

func (b brokenSettingsStore) Settings() store.SettingsStore {
	return brokenSettings{b.Store.Settings()}
}

func TestSweepRecordWriteFailureIsOnlyLogged(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	s, st := newSyncer(t, &fakeMAS{})
	s.st = brokenSettingsStore{st}
	if since := s.record(context.Background(), 0, 1, errors.New("boom"), nil); since == nil {
		t.Fatal("a failed write lost the streak")
	}
	if !strings.Contains(logs.String(), "sweep result not recorded: disk full") {
		t.Fatalf("not logged: %q", logs.String())
	}
}
```

- [ ] **Step 6: Run them to see them fail.** Run `go test ./internal/matrixsync/ -run 'Record' -v`. Expected: FAIL to build, `undefined: SweepRecordKey`.

- [ ] **Step 7: Implement the sweep record.** Create `internal/matrixsync/record.go`:

```go
package matrixsync

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"
)

// SweepRecordKey is the setting the syncer alone writes after each sweep; the console reads it.
const SweepRecordKey = "matrix_sweep_last"

// sweepErrorMax bounds the recorded error. MAS client errors carry method, path and status,
// never a token or a response body (mas.go).
const sweepErrorMax = 300

// SweepRecord is the last sweep's outcome. FailingSince is the start of the current failing
// streak, nil when the sweep succeeded.
type SweepRecord struct {
	FinishedAt   time.Time  `json:"finished_at"`
	OK           bool       `json:"ok"`
	Error        string     `json:"error"`
	Applied      int        `json:"applied"`
	Failed       int        `json:"failed"`
	FailingSince *time.Time `json:"failing_since"`
}

// record writes one sweep's outcome and returns the failing streak's start for the next one.
// A failed write is logged: the record is for the console; the sweep already happened.
func (s *Syncer) record(ctx context.Context, applied, failed int, err error, since *time.Time) *time.Time {
	now := time.Now().UTC()
	rec := SweepRecord{FinishedAt: now, OK: err == nil, Applied: applied, Failed: failed}
	if err == nil {
		since = nil
	} else {
		if since == nil {
			since = &now
		}
		rec.Error, rec.FailingSince = clipError(err.Error()), since
	}
	b, merr := json.Marshal(rec)
	if merr != nil {
		return since
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if werr := s.st.Settings().SetSetting(wctx, SweepRecordKey, string(b)); werr != nil {
		log.Printf("[MATRIX] sweep result not recorded: %v", werr)
	}
	return since
}

// storedStreak is the failing streak a previous process recorded, so a restart keeps its start.
func (s *Syncer) storedStreak(ctx context.Context) *time.Time {
	raw, err := s.st.Settings().GetSetting(ctx, SweepRecordKey)
	var rec SweepRecord
	if err != nil || json.Unmarshal([]byte(raw), &rec) != nil || rec.OK {
		return nil
	}
	return rec.FailingSince
}

func clipError(v string) string {
	if len(v) <= sweepErrorMax {
		return v
	}
	return strings.ToValidUTF8(v[:sweepErrorMax], "")
}
```

In `internal/matrixsync/matrixsync.go`, replace `Run` and `Sweep` with the following. `Sweep` keeps its signature; the loop body that applies each action is unchanged except for the two counters.

```go
// Run sweeps at start, every interval and on Wake. done closes between sweeps, so shutdown
// can wait for it before the store closes. A failure is logged once per streak. Every sweep
// that shutdown did not interrupt is recorded under SweepRecordKey.
func (s *Syncer) Run(ctx context.Context, interval time.Duration, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failing := false
	since := s.storedStreak(ctx)
	for {
		run, cancel := context.WithTimeout(ctx, 2*time.Minute)
		applied, failed, err := s.sweep(run)
		cancel()
		if ctx.Err() == nil {
			since = s.record(ctx, applied, failed, err, since)
		}
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			log.Printf("[MATRIX] offboarding sweep failing (retrying every %s): %v", interval, err)
			failing = true
		case err == nil && failing:
			log.Printf("[MATRIX] offboarding sweep recovered")
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// Sweep applies Plan once. Every action is audited, success or failure; one failed action
// does not stop the rest.
func (s *Syncer) Sweep(ctx context.Context) error {
	_, _, err := s.sweep(ctx)
	return err
}

// sweep is Sweep, also counting the MAS actions applied and failed.
func (s *Syncer) sweep(ctx context.Context) (applied, failed int, err error) {
	users, err := s.mas.Users(ctx)
	if err != nil {
		return 0, 0, err
	}
	dir, err := s.st.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for _, a := range Plan(users, dir) {
		var err error
		switch a.Kind {
		case Lock:
			err = s.mas.Lock(ctx, a.User.ID)
		case Unlock:
			err = s.mas.Unlock(ctx, a.User.ID)
		case Deactivate:
			err = s.mas.Deactivate(ctx, a.User.ID)
		}
		outcome := "ok"
		if err != nil {
			failed++
			outcome = "error: " + err.Error()
			errs = append(errs, fmt.Errorf("%s %s: %w", a.Kind, a.User.Username, err))
		} else {
			applied++
		}
		// The MAS action happened; record it even if shutdown cancelled the sweep.
		actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		aerr := s.st.Audit().LogAudit(actx, &store.AuditRecord{
			Action:   "matrix." + string(a.Kind),
			Resource: "@" + a.User.Username + ":" + s.serverName,
			Details:  fmt.Sprintf("subject=%q reason=%q outcome=%q", a.User.Subject, a.Reason, outcome),
		})
		acancel()
		if aerr != nil {
			errs = append(errs, aerr)
		}
	}
	return applied, failed, errors.Join(errs...)
}
```

- [ ] **Step 8: Run the matrixsync tests.** Run `go test -race ./internal/matrixsync/ -v`. Expected: PASS, the existing sweep and Run tests included.

- [ ] **Step 9: Write the failing route tests.** Create `internal/api/sync_status_test.go`:

```go
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/google/uuid"
)

const syncSecret = "4f1c2a9e8b7d6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e"

type syncStatusBody struct {
	Webhook       *sso.WebhookRecord      `json:"webhook"`
	Rejected      sso.Rejections          `json:"rejected"`
	Sweep         *matrixsync.SweepRecord `json:"sweep"`
	KyIdentityURL string                  `json:"kyidentity_url"`
}

func TestSyncStatusShowsRecordsAndRejections(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.SSO.KyIdentityHMACSecret = syncSecret
	cfg.SSO.KyIdentityIssuer = "https://id.example.com"
	srv := api.NewServer(cfg, st) // the KyIdentity client copies the secret at construction
	srv.SetMatrixAdmin(&fakeMAS{finished: map[string]bool{}})
	admin := loginAs(t, srv, st, "root", "admin")
	get := func() syncStatusBody {
		t.Helper()
		w := adminDo(t, srv, admin, "GET", "/api/admin/matrix/sync-status", nil)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control %q", w.Header().Get("Cache-Control"))
		}
		return decode[syncStatusBody](t, w)
	}
	if s := get(); s.Webhook != nil || s.Sweep != nil || s.Rejected.Count != 0 || s.KyIdentityURL != "https://id.example.com" {
		t.Fatalf("before any delivery: %+v", s)
	}
	body := []byte(`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"id":"kid-9","userName":"nine","roles":[],"active":true,"meta":{"resourceType":"User","version":"W/\"1\""}}`)
	post := func(h syncauth.Headers) int {
		req := httptest.NewRequest("POST", "/api/sso/kyidentity/sync", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/scim+json")
		h.Apply(req)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	signed, err := syncauth.Sign([]byte(syncSecret), time.Now(), "user.created", uuid.NewString(), body)
	if err != nil {
		t.Fatal(err)
	}
	if code := post(signed); code != http.StatusOK {
		t.Fatalf("signed delivery: %d", code)
	}
	bad := signed
	bad.Signature = "v1=" + strings.Repeat("0", 64)
	if code := post(bad); code != http.StatusUnauthorized {
		t.Fatalf("badly signed delivery: %d", code)
	}
	if s := get(); s.Webhook == nil || s.Webhook.Kind != "user.created" || s.Rejected.Count != 1 ||
		s.Rejected.LastReason != sso.RejectBadSignature || s.Rejected.LastAt == nil {
		t.Fatalf("after one accepted and one refused delivery: %+v", s)
	}
	since := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	rec, _ := json.Marshal(matrixsync.SweepRecord{FinishedAt: time.Now().UTC(), Error: "MAS GET /api/admin/v1/users: HTTP 500", Failed: 1, FailingSince: &since})
	if err := st.Settings().SetSetting(ctx, matrixsync.SweepRecordKey, string(rec)); err != nil {
		t.Fatal(err)
	}
	if s := get(); s.Sweep == nil || s.Sweep.OK || s.Sweep.FailingSince == nil || !s.Sweep.FailingSince.Equal(since) {
		t.Fatalf("failing sweep: %+v", s.Sweep)
	}
	// An unreadable record shows as none, never as a 500.
	if err := st.Settings().SetSetting(ctx, matrixsync.SweepRecordKey, "{"); err != nil {
		t.Fatal(err)
	}
	if s := get(); s.Sweep != nil {
		t.Fatalf("unreadable record shown: %+v", s.Sweep)
	}
}

// Review Focus 1: the sync records have their own route and stay out of every page load.
func TestSettingsHideTheSyncRecords(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	for _, k := range []string{sso.WebhookRecordKey, matrixsync.SweepRecordKey} {
		if err := st.Settings().SetSetting(ctx, k, `{"marker":"sync-record"}`); err != nil {
			t.Fatal(err)
		}
	}
	body := do(t, srv, "GET", "/api/settings", loginAs(t, srv, st, "root", "admin")).Body.String()
	if strings.Contains(body, "sync-record") || !strings.Contains(body, "extra_settings") {
		t.Fatalf("settings: %s", body)
	}
}

// Each record has one writer: the webhook's in internal/sso, the sweep's in internal/matrixsync.
func TestSyncRecordsHaveOneWriterEach(t *testing.T) {
	owners := map[string]string{
		"WebhookRecordKey":            "internal/sso/syncstatus.go",
		"SweepRecordKey":              "internal/matrixsync/record.go",
		`"kyidentity_webhook_last"`: "internal/sso/syncstatus.go",
		`"matrix_sweep_last"`:       "internal/matrixsync/record.go",
	}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "web") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(b), "\n") {
			for needle, owner := range owners {
				literal := strings.HasPrefix(needle, `"`)
				if strings.Contains(line, needle) && (literal || strings.Contains(line, "SetSetting(")) && rel != owner {
					t.Errorf("%s:%d writes or names %s; only %s may", rel, i+1, needle, owner)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

In `internal/api/authz_test.go`, add `{"GET", "/api/admin/matrix/sync-status"},` to the cases after `{"GET", "/api/admin/audit"},`. In `internal/api/console_test.go`, add `{"GET", "/api/admin/matrix/sync-status"},` to the route list of `TestMatrixRoutesAre404WithoutMatrix`, after the `.../delete"` entry.

- [ ] **Step 10: Run them to see them fail.** Run `go test ./internal/api/ -run 'SyncStatus|SyncRecords|HideTheSyncRecords|MatrixRoutesAre404|PrivilegedEndpoints' -v`. Expected: `TestSyncStatusShowsRecordsAndRejections` FAILS ("got 404": the route answers `Unknown API endpoint`), `TestSettingsHideTheSyncRecords` FAILS (the marker is in `extra_settings`), and `TestMatrixRoutesAre404WithoutMatrix` FAILS for `/api/admin/matrix/sync-status` (no `matrix_disabled` code). `TestSyncRecordsHaveOneWriterEach` already PASSES: it pins the rule for later changes.

- [ ] **Step 11: Implement the route.** Create `internal/api/sync_handlers.go`:

```go
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// handleSyncStatus reports KyIdentity's directory sync as KyMessages sees it: the last
// acknowledged webhook and the last sweep, each written only by its owner, and the deliveries
// refused since this process started (memory only: their senders are unauthenticated). The
// page derives the hints.
func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	var webhook *sso.WebhookRecord
	var sweep *matrixsync.SweepRecord
	for _, rec := range []struct {
		key  string
		into any
	}{{sso.WebhookRecordKey, &webhook}, {matrixsync.SweepRecordKey, &sweep}} {
		raw, err := s.store.Settings().GetSetting(r.Context(), rec.key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "Could not read the sync status")
			return
		}
		_ = json.Unmarshal([]byte(raw), rec.into) // an unreadable record shows as none
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{
		"webhook":        webhook,
		"rejected":       s.kyidentity.Rejections(),
		"sweep":          sweep,
		"kyidentity_url": s.config.SSO.KyIdentityIssuer,
	})
}
```

In `internal/api/server.go` `routes()`, after `s.mux.HandleFunc("GET /api/admin/audit", s.requireAdmin(s.handleAudit))`, add:

```go
	s.mux.HandleFunc("GET /api/admin/matrix/sync-status", s.requireAdmin(s.handleSyncStatus))
```

In `internal/api/settings_handlers.go`, replace the `notExtra` declaration and its comment with the following, and add the `matrixsync` and `sso` imports:

```go
// notExtra are settings extra_settings never carries: the logo is up to 1 MiB and is served at
// /app-icon.png; the sync records have their own route.
var notExtra = map[string]bool{brandLogoKey: true, sso.WebhookRecordKey: true, matrixsync.SweepRecordKey: true}
```

- [ ] **Step 12: Run everything touched.** Run `go test -race ./internal/sso/ ./internal/matrixsync/ ./internal/api/ ./cmd/server/`. Expected: PASS. Run `go vet ./...`. Expected: no output.

- [ ] **Step 13: DOX.**
  - In `internal/sso/AGENTS.md` Ownership, append: ` \`syncstatus.go\` owns the webhook record and the rejection counters.` Add to Local Contracts:

    ```markdown
    - An acknowledged delivery (applied, superseded or duplicate; the same set that wakes the sweep) writes `kyidentity_webhook_last` (`WebhookRecordKey`, `WebhookRecord{at, kind}`) on a detached 5 s context. `syncstatus.go` is its only writer; a failed write is logged, never a failed delivery. Refused deliveries (`ErrSyncUnauthorized`, `ErrSyncMalformed`) are counted in memory only (`Rejections()`: count since start, last time, last reason), never written to the database or the audit log; bodies are never kept, and logging is unchanged. Reasons: `not_configured` (secret unset or shorter than `syncauth.MinKeyBytes`), `bad_signature` (missing or not matching), `stale` (outside ±5 minutes), `bad_headers` (event type, ID or timestamp missing or unparseable), `malformed` (signed, but not a usable SCIM user). `ErrSyncUnauthorized` wraps its cause with `%w`, so reasons are told apart with `errors.Is`. A delivery that fails on the database (500) is neither recorded nor counted.
    ```

  - In `internal/matrixsync/AGENTS.md` Ownership, after `Owns \`Plan\` and \`Syncer\` (\`matrixsync.go\`)`, add `, the sweep record (\`record.go\`)`. Add to Local Contracts:

    ```markdown
    - After every sweep that shutdown did not interrupt, `Run` writes `matrix_sweep_last` (`SweepRecordKey`, `SweepRecord`: `finished_at`, `ok`, `error` at most 300 bytes of valid UTF-8, `applied` and `failed` MAS actions, `failing_since`, the start of the current failing streak, null when ok). The streak is carried across restarts from the stored record. `record.go` is the only writer; a failed write is logged and never fails the sweep. MAS client errors hold method, path and status, never a token, so the error text is safe to show admins.
    ```

  - In `internal/api/AGENTS.md`, append to Local Contracts:

    ```markdown
    - `GET /api/admin/matrix/sync-status` (`sync_handlers.go`; admin, `no-store`; 404 `matrix_disabled` without Matrix) answers `{webhook: sso.WebhookRecord|null, rejected: sso.Rejections, sweep: matrixsync.SweepRecord|null, kyidentity_url}`. It only reads the records (each has one writer, pinned by `TestSyncRecordsHaveOneWriterEach`); an unreadable record shows as null. `kyidentity_url` is `KY_KYIDENTITY_ISSUER`. Hints are the page's. `notExtra` also holds both record keys.
    ```

- [ ] **Step 14: Commit.** Run `gofmt -w internal/sso internal/matrixsync internal/api && gofmt -l internal/sso internal/matrixsync internal/api` (expected: no output). Commit `internal/sso/`, `internal/matrixsync/` and `internal/api/`. Message: `sync status: webhook and sweep records, in-memory rejections, sync-status route`.

---

### Task 5: Web: Branding and KyIdentity sync panels, the Overview card

**Files:**
- Create: `web/src/components/BrandingPanel.tsx`, `web/src/components/BrandingPanel.test.tsx`
- Create: `web/src/components/SyncPanel.tsx`, `web/src/components/SyncPanel.test.tsx`
- Modify: `web/src/pages/Settings.tsx`, `web/src/App.tsx` (one line), `web/src/pages/Dashboard.tsx`, `web/src/pages/Dashboard.test.tsx`
- Modify: `web/src/styles/theme.css` (two lines after `.app-brand img`), `web/src/pages/Login.tsx` (the `<h1>` style)
- Modify: `web/dist/` (rebuilt)
- Modify: `web/AGENTS.md`

**Interfaces:**
- Consumes: the JSON of `GET/PUT/DELETE /api/admin/branding*` (Task 3) and of `GET /api/admin/matrix/sync-status` (Task 4); `adminFetch`, `errorMessage`, `isMatrixDisabled` (`web/src/api.ts`); `obj`, `str`, `bool`, `count`, `iso` (`web/src/dto.ts`).
- Produces:
  - `BrandingPanel: React.FC<{ onChanged: () => void }>`, `parseBranding(v: unknown): Branding`, `elementNotice(b: Branding): string | null`, `MAX_LOGO_BYTES`
  - `SyncPanel: React.FC`, `parseSyncStatus(v: unknown): SyncStatus`, `syncState(s): 'ok' | 'warning' | 'failing'`, `syncHints(s): string[]`, `syncCard(s): { text: string; tone: 'success' | 'warning' | 'danger' }`, `UNCERTAIN`
  - `Settings` takes `onBrandingChanged: () => void`.
  - Accessible names Task 6 relies on: region "Branding", label "Product name", buttons "Save name", "Use default (<name>)", "Upload logo", "Reset logo", label starting "Logo", image alt "Current logo", region "KyIdentity sync", and the status texts "Name saved…" and "Logo saved…".

- [ ] **Step 1: Write the failing panel tests.** Create `web/src/components/BrandingPanel.test.tsx`:

```tsx
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { BrandingPanel, elementNotice, parseBranding } from './BrandingPanel';

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
const SHA = 'a'.repeat(64);
const STATE = {
  name: 'KyMessages', stored_name: '', default_name: 'KyMessages',
  logo: { custom: false, sha256: '', size: 0 }, element: { brand: 'KyMessages' },
};

function serve(route: (url: string, init?: RequestInit) => Response | undefined) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    calls.push({ url, init });
    const res = route(url, init);
    if (!res) throw new Error(`unexpected fetch ${init?.method ?? 'GET'} ${url}`);
    return res;
  }));
  return calls;
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('parseBranding and elementNotice', () => {
  it('reads Element only when present and refuses a malformed body', () => {
    expect(parseBranding({ ...STATE, element: undefined }).element).toBeNull();
    expect(parseBranding({ ...STATE, element: { error: 'open: no such file' } }).element).toEqual({ brand: null, error: 'open: no such file' });
    expect(() => parseBranding({ ...STATE, logo: { custom: 'yes', sha256: '', size: 0 } })).toThrow();
  });
  it('names what Element shows and why, and is quiet when it matches', () => {
    expect(elementNotice(parseBranding({ ...STATE, name: 'Acme', element: { brand: 'KyMessages', error: 'permission denied' } })))
      .toBe('Saved, but Element shows "KyMessages": permission denied. It is retried every minute.');
    expect(elementNotice(parseBranding({ ...STATE, name: 'Acme', element: { error: 'open: no such file' } })))
      .toBe("Saved, but Element's config could not be read: open: no such file");
    expect(elementNotice(parseBranding(STATE))).toBeNull();
    expect(elementNotice(parseBranding({ ...STATE, element: undefined }))).toBeNull();
  });
});

describe('BrandingPanel', () => {
  it('saves a name and tells the shell to re-read the settings', async () => {
    const onChanged = vi.fn();
    const calls = serve((url, init) => {
      if (url === '/api/admin/branding') return json(STATE);
      if (url === '/api/admin/branding/name' && init?.method === 'PUT') return json({ ...STATE, name: 'Acme', stored_name: 'Acme', element: { brand: 'Acme' } });
      return undefined;
    });
    render(<BrandingPanel onChanged={onChanged} />);
    fireEvent.change(await screen.findByLabelText('Product name'), { target: { value: 'Acme' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    expect(await screen.findByText(/^Name saved/)).toBeTruthy();
    expect(onChanged).toHaveBeenCalledTimes(1);
    expect(calls.find((c) => c.init?.method === 'PUT')?.init?.body).toBe(JSON.stringify({ name: 'Acme' }));
    expect(screen.getByRole('button', { name: 'Use default (KyMessages)' })).toBeTruthy();
  });

  it('says what Element still shows when its config could not be written', async () => {
    serve((url, init) => {
      if (url === '/api/admin/branding') return json(STATE);
      if (init?.method === 'PUT') return json({ ...STATE, name: 'Acme', stored_name: 'Acme', element: { brand: 'KyMessages', error: 'open /matrix/element/config.json: permission denied' } });
      return undefined;
    });
    render(<BrandingPanel onChanged={() => {}} />);
    fireEvent.change(await screen.findByLabelText('Product name'), { target: { value: 'Acme' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save name' }));
    expect(await screen.findByText('Saved, but Element shows "KyMessages": open /matrix/element/config.json: permission denied. It is retried every minute.')).toBeTruthy();
  });

  it('resets the name to the default', async () => {
    const calls = serve((url, init) => {
      if (url === '/api/admin/branding') return json({ ...STATE, name: 'Acme', stored_name: 'Acme', element: { brand: 'Acme' } });
      if (init?.method === 'PUT') return json(STATE);
      return undefined;
    });
    render(<BrandingPanel onChanged={() => {}} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Use default (KyMessages)' }));
    await waitFor(() => expect(calls.find((c) => c.init?.method === 'PUT')?.init?.body).toBe(JSON.stringify({ name: '' })));
    expect(await screen.findByText('Name reset to KyMessages.')).toBeTruthy();
  });

  it('uploads a PNG and previews it by its digest, never through a blob URL', async () => {
    const calls = serve((url, init) => {
      if (url === '/api/admin/branding') return json(STATE);
      if (url === '/api/admin/branding/logo' && init?.method === 'PUT') return json({ ...STATE, logo: { custom: true, sha256: SHA, size: 1234 } });
      if (url === '/api/admin/branding/logo' && init?.method === 'DELETE') return json(STATE);
      return undefined;
    });
    render(<BrandingPanel onChanged={() => {}} />);
    expect((await screen.findByAltText('Current logo')).getAttribute('src')).toBe('/app-icon.png?v=default');
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'logo.png', { type: 'image/png' });
    fireEvent.change(screen.getByLabelText(/^Logo/), { target: { files: [file] } });
    fireEvent.click(screen.getByRole('button', { name: 'Upload logo' }));
    await waitFor(() => expect(screen.getByAltText('Current logo').getAttribute('src')).toBe(`/app-icon.png?v=${SHA}`));
    const put = calls.find((c) => c.init?.method === 'PUT');
    expect(new Headers(put?.init?.headers).get('Content-Type')).toBe('image/png');
    expect(put?.init?.body).toBe(file);
    fireEvent.click(screen.getByRole('button', { name: 'Reset logo' }));
    await waitFor(() => expect(screen.getByAltText('Current logo').getAttribute('src')).toBe('/app-icon.png?v=default'));
  });

  it('refuses a file over 1 MiB without sending it', async () => {
    const calls = serve((url) => (url === '/api/admin/branding' ? json(STATE) : undefined));
    render(<BrandingPanel onChanged={() => {}} />);
    const big = new File([new Uint8Array((1 << 20) + 1)], 'big.png', { type: 'image/png' });
    fireEvent.change(await screen.findByLabelText(/^Logo/), { target: { files: [big] } });
    fireEvent.click(screen.getByRole('button', { name: 'Upload logo' }));
    expect(await screen.findByText('The logo must be at most 1 MiB.')).toBeTruthy();
    expect(calls.filter((c) => c.init?.method === 'PUT')).toHaveLength(0);
  });
});
```

Create `web/src/components/SyncPanel.test.tsx`:

```tsx
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { SyncPanel, UNCERTAIN, parseSyncStatus, syncCard, syncHints, syncState } from './SyncPanel';

const NOW = '2026-10-02T10:00:00Z';
const OK = {
  webhook: { at: NOW, kind: 'user.updated' },
  rejected: { count: 0, last_at: null, last_reason: '' },
  sweep: { finished_at: NOW, ok: true, error: '', applied: 1, failed: 0, failing_since: null },
  kyidentity_url: 'https://id.example.com',
};
const SECRETS_DIFFER = 'Deliveries fail the signature check: the secret in KyIdentity and KY_KYIDENTITY_HMAC_SECRET differ.';
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('syncState, syncHints and syncCard', () => {
  it('is ok when a webhook was accepted and the last sweep passed', () => {
    const s = parseSyncStatus(OK);
    expect(syncState(s)).toBe('ok');
    expect(syncHints(s)).toEqual([UNCERTAIN]);
    expect(syncCard(s).tone).toBe('success');
    expect(syncCard(s).text).toMatch(/^Last webhook .*; last sweep ok$/);
  });
  it('warns when no webhook was ever accepted, naming both settings', () => {
    const s = parseSyncStatus({ ...OK, webhook: null });
    expect(syncState(s)).toBe('warning');
    expect(syncHints(s)[0]).toBe('No webhook has been accepted yet: check the suite_webhook system in KyIdentity and KY_KYIDENTITY_HMAC_SECRET.');
    expect(syncCard(s)).toEqual({ text: `Needs attention: ${syncHints(s)[0]}`, tone: 'warning' });
  });
  it('warns that the secrets differ after bad signatures', () => {
    const s = parseSyncStatus({ ...OK, rejected: { count: 3, last_at: NOW, last_reason: 'bad_signature' } });
    expect(syncState(s)).toBe('warning');
    expect(syncHints(s)).toContain(SECRETS_DIFFER);
  });
  it('treats a stale delivery as a hint, not a warning', () => {
    const s = parseSyncStatus({ ...OK, rejected: { count: 1, last_at: NOW, last_reason: 'stale' } });
    expect(syncState(s)).toBe('ok');
    expect(syncHints(s).some((h) => h.includes('check both clocks'))).toBe(true);
  });
  it('warns before the first sweep', () => {
    expect(syncState(parseSyncStatus({ ...OK, sweep: null }))).toBe('warning');
  });
  it('fails while the sweep fails, saying since when and why', () => {
    const s = parseSyncStatus({ ...OK, sweep: { ...OK.sweep, ok: false, error: 'MAS token: HTTP 401', failing_since: '2026-10-02T09:00:00Z' } });
    expect(syncState(s)).toBe('failing');
    expect(syncHints(s)[0]).toMatch(/^The offboarding sweep has failed since .+: MAS token: HTTP 401$/);
    expect(syncCard(s).tone).toBe('danger');
    expect(syncCard(s).text).toMatch(/^Offboarding sweep failing since /);
  });
  it('refuses a malformed status', () => {
    expect(() => parseSyncStatus({ ...OK, rejected: { count: -1, last_at: null, last_reason: '' } })).toThrow();
    expect(() => parseSyncStatus({ ...OK, sweep: { ...OK.sweep, finished_at: 'yesterday' } })).toThrow();
  });
});

describe('SyncPanel', () => {
  it('renders nothing when chat is not set up', async () => {
    const fetchMock = vi.fn(async () => json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404));
    vi.stubGlobal('fetch', fetchMock);
    const { container } = render(<SyncPanel />);
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(container.innerHTML).toBe('');
  });
  it('shows the records, the hints and the KyIdentity link', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...OK, rejected: { count: 2, last_at: NOW, last_reason: 'bad_signature' } })));
    render(<SyncPanel />);
    const panel = await screen.findByRole('region', { name: 'KyIdentity sync' });
    expect(within(panel).getByText(/\(user\.updated\)$/)).toBeTruthy();
    expect(within(panel).getByText(/\(bad signature\)$/)).toBeTruthy();
    expect(within(panel).getByText(SECRETS_DIFFER)).toBeTruthy();
    expect(within(panel).getByText(UNCERTAIN)).toBeTruthy();
    expect(within(panel).getByText('Needs attention')).toBeTruthy();
    expect(within(panel).getByRole('link', { name: /Open KyIdentity/ }).getAttribute('href')).toBe('https://id.example.com');
  });
  it('links KyIdentity only over https', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ ...OK, kyidentity_url: 'javascript:alert(1)' })));
    render(<SyncPanel />);
    const panel = await screen.findByRole('region', { name: 'KyIdentity sync' });
    expect(within(panel).queryByRole('link')).toBeNull();
  });
});
```

- [ ] **Step 2: Run them to see them fail.** Run `cd web && npm test -- src/components/BrandingPanel.test.tsx src/components/SyncPanel.test.tsx`. Expected: FAIL, `Failed to resolve import "./BrandingPanel"` and `"./SyncPanel"`.

- [ ] **Step 3: Implement the Branding panel.** Create `web/src/components/BrandingPanel.tsx`:

```tsx
import React, { useCallback, useEffect, useState } from 'react';
import { Type } from 'lucide-react';
import { adminFetch, errorMessage } from '../api';
import { bool, count, obj, str } from '../dto';

export interface Branding {
  name: string;
  stored_name: string;
  default_name: string;
  logo: { custom: boolean; sha256: string; size: number };
  /** What Element's config.json says; null without Matrix. */
  element: { brand: string | null; error: string } | null;
}

export const MAX_LOGO_BYTES = 1 << 20;

export function parseBranding(v: unknown): Branding {
  const b = obj(v);
  const logo = obj(b.logo);
  let element: Branding['element'] = null;
  if (b.element !== undefined) {
    const e = obj(b.element);
    element = { brand: e.brand === undefined ? null : str(e.brand, 1024), error: e.error === undefined ? '' : str(e.error, 2048) };
  }
  return {
    name: str(b.name, 256), stored_name: str(b.stored_name, 256), default_name: str(b.default_name, 256),
    logo: { custom: bool(logo.custom), sha256: str(logo.sha256, 64), size: count(logo.size) },
    element,
  };
}

/** The warning while Element does not show the saved name; null when it does, or without Matrix. */
export function elementNotice(b: Branding): string | null {
  if (!b.element || b.element.brand === b.name) return null;
  if (b.element.brand === null) return `Saved, but Element's config could not be read: ${b.element.error}`;
  return `Saved, but Element shows "${b.element.brand}"${b.element.error ? `: ${b.element.error}` : ''}. It is retried every minute.`;
}

export const BrandingPanel: React.FC<{ onChanged: () => void }> = ({ onChanged }) => {
  const [state, setState] = useState<Branding | null>(null);
  const [name, setName] = useState('');
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  const apply = useCallback((b: Branding) => {
    setState(b);
    setName(b.stored_name);
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/branding', { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not load branding'));
        apply(parseBranding(await res.json()));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Could not load branding');
      });
    return () => controller.abort();
  }, [apply]);

  const change = async (path: string, init: RequestInit, done: string) => {
    setBusy(true);
    setMessage('');
    setError('');
    try {
      const res = await adminFetch(path, init);
      if (!res.ok) throw new Error(await errorMessage(res, 'The change was refused'));
      apply(parseBranding(await res.json()));
      setMessage(done);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'The change was refused');
    } finally {
      setBusy(false);
    }
  };
  const saveName = (value: string, done: string) =>
    void change('/api/admin/branding/name', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: value }) }, done);
  const upload = () => {
    if (!file) return;
    if (file.size > MAX_LOGO_BYTES) {
      setMessage('');
      setError('The logo must be at most 1 MiB.');
      return;
    }
    void change('/api/admin/branding/logo', { method: 'PUT', headers: { 'Content-Type': 'image/png' }, body: file }, 'Logo saved. It shows on the next page load.');
  };

  const notice = state ? elementNotice(state) : null;
  return (
    <section className="panel dr-section" aria-label="Branding">
      <div className="panel-header">
        <h3><Type size={16} /> Branding</h3>
      </div>
      <p className="dr-hint">
        The name and logo of this console, its sign-in page and Element. They show on the next page load. Changes need a sign-in from the last 10 minutes.
      </p>
      {state && (
        <>
          <form className="dr-row" onSubmit={(e) => { e.preventDefault(); saveName(name, 'Name saved. Element shows it on its next page load.'); }}>
            <label className="dr-field">
              <span>Product name</span>
              <input value={name} placeholder={state.default_name} autoComplete="off" onChange={(e) => setName(e.target.value)} />
            </label>
            <button type="submit" disabled={busy}>Save name</button>
            <button type="button" className="btn-secondary" disabled={busy || !state.stored_name}
              onClick={() => saveName('', `Name reset to ${state.default_name}.`)}>
              Use default ({state.default_name})
            </button>
          </form>
          {notice && <div className="dr-alert dr-alert-warn" role="alert">{notice}</div>}
          <div className="dr-row">
            {/* By digest, never a blob: URL, which the CSP does not allow. */}
            <img src={`/app-icon.png?v=${state.logo.custom ? state.logo.sha256 : 'default'}`} width={56} height={56} alt="Current logo" />
            <label className="dr-field">
              <span>Logo (PNG, at most 1 MiB and 1024×1024)</span>
              <input type="file" accept="image/png" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
            </label>
            <button type="button" disabled={busy || !file} onClick={upload}>Upload logo</button>
            <button type="button" className="btn-secondary" disabled={busy || !state.logo.custom}
              onClick={() => void change('/api/admin/branding/logo', { method: 'DELETE' }, 'Logo reset to the KyMessages stamp.')}>
              Reset logo
            </button>
          </div>
        </>
      )}
      {message && <div className="dr-alert dr-alert-success" role="status">{message}</div>}
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
    </section>
  );
};
```

- [ ] **Step 4: Implement the sync panel.** Create `web/src/components/SyncPanel.tsx`:

```tsx
import React, { useEffect, useState } from 'react';
import { ExternalLink, RefreshCw } from 'lucide-react';
import { errorMessage, isMatrixDisabled } from '../api';
import { bool, count, iso, obj, str } from '../dto';

export interface SyncStatus {
  webhook: { at: string; kind: string } | null;
  rejected: { count: number; last_at: string | null; last_reason: string };
  sweep: { finished_at: string; ok: boolean; error: string; applied: number; failed: number; failing_since: string | null } | null;
  kyidentity_url: string;
}
export type SyncState = 'ok' | 'warning' | 'failing';

export const UNCERTAIN = 'A change made while KyMessages was down waits in KyIdentity as an uncertain write; resume it there.';
const REASON: Record<string, string> = {
  not_configured: 'secret not set', bad_signature: 'bad signature', stale: 'outside the 5-minute window',
  bad_headers: 'missing or malformed headers', malformed: 'signed, but not a usable user',
};
// Refusals that mean the secret is wrong or missing, not one odd delivery.
const SECRET_REASONS = new Set(['bad_signature', 'not_configured']);

const nullable = <T,>(v: unknown, read: (x: unknown) => T): T | null => (v === null ? null : read(v));
const when = (t: string) => new Date(t).toLocaleString();

export function parseSyncStatus(v: unknown): SyncStatus {
  const s = obj(v);
  const r = obj(s.rejected);
  return {
    webhook: nullable(s.webhook, (x) => {
      const w = obj(x);
      return { at: iso(w.at), kind: str(w.kind, 64) };
    }),
    rejected: { count: count(r.count), last_at: nullable(r.last_at, iso), last_reason: str(r.last_reason, 32) },
    sweep: nullable(s.sweep, (x) => {
      const w = obj(x);
      return {
        finished_at: iso(w.finished_at), ok: bool(w.ok), error: str(w.error, 512),
        applied: count(w.applied), failed: count(w.failed), failing_since: nullable(w.failing_since, iso),
      };
    }),
    kyidentity_url: str(s.kyidentity_url, 512),
  };
}

export function syncState(s: SyncStatus): SyncState {
  if (s.sweep && !s.sweep.ok) return 'failing';
  if (!s.webhook || !s.sweep || (s.rejected.count > 0 && SECRET_REASONS.has(s.rejected.last_reason))) return 'warning';
  return 'ok';
}

/** What to fix, most urgent first; the last hint always applies. */
export function syncHints(s: SyncStatus): string[] {
  const hints: string[] = [];
  if (s.sweep && !s.sweep.ok) hints.push(`The offboarding sweep has failed since ${when(s.sweep.failing_since ?? s.sweep.finished_at)}: ${s.sweep.error}`);
  if (!s.webhook) hints.push('No webhook has been accepted yet: check the suite_webhook system in KyIdentity and KY_KYIDENTITY_HMAC_SECRET.');
  if (s.rejected.count > 0 && s.rejected.last_reason === 'bad_signature') hints.push('Deliveries fail the signature check: the secret in KyIdentity and KY_KYIDENTITY_HMAC_SECRET differ.');
  if (s.rejected.count > 0 && s.rejected.last_reason === 'not_configured') hints.push('KY_KYIDENTITY_HMAC_SECRET is not set, or is shorter than 16 bytes.');
  if (s.rejected.count > 0 && s.rejected.last_reason === 'stale') hints.push("Deliveries arrive more than 5 minutes off this server's clock: check both clocks.");
  if (!s.sweep) hints.push('No offboarding sweep has finished yet.');
  hints.push(UNCERTAIN);
  return hints;
}

/** The Overview card: one line and a tone. */
export function syncCard(s: SyncStatus): { text: string; tone: 'success' | 'warning' | 'danger' } {
  switch (syncState(s)) {
    case 'failing':
      return { text: `Offboarding sweep failing since ${when(s.sweep?.failing_since ?? s.sweep?.finished_at ?? '')}`, tone: 'danger' };
    case 'warning':
      return { text: `Needs attention: ${syncHints(s)[0]}`, tone: 'warning' };
    default:
      return { text: `Last webhook ${s.webhook ? when(s.webhook.at) : ''}; last sweep ok`, tone: 'success' };
  }
}

const LABEL: Record<SyncState, string> = { ok: 'Working', warning: 'Needs attention', failing: 'Failing' };
const BADGE: Record<SyncState, string> = { ok: 'badge badge-success', warning: 'badge badge-accent', failing: 'badge badge-danger' };

/** Settings' "KyIdentity sync" panel; nothing at all when chat is not set up. */
export const SyncPanel: React.FC = () => {
  const [status, setStatus] = useState<SyncStatus | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    fetch('/api/admin/matrix/sync-status', { signal: controller.signal, cache: 'no-store' })
      .then(async (res) => {
        if (await isMatrixDisabled(res)) return;
        if (!res.ok) throw new Error(await errorMessage(res, 'Could not load the sync status'));
        setStatus(parseSyncStatus(await res.json()));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Could not load the sync status');
      });
    return () => controller.abort();
  }, []);
  if (!status && !error) return null;
  const state = status ? syncState(status) : null;
  return (
    <section className="panel dr-section" aria-label="KyIdentity sync">
      <div className="panel-header">
        <h3><RefreshCw size={16} /> KyIdentity sync</h3>
        {state && <span className={BADGE[state]}>{LABEL[state]}</span>}
      </div>
      {error && <div className="dr-alert dr-alert-error" role="alert">{error}</div>}
      {status && (
        <>
          <div className="dr-facts">
            <div className="dr-fact">
              <div className="dr-fact-label">Last accepted webhook</div>
              <div className="dr-fact-value">{status.webhook ? `${when(status.webhook.at)} (${status.webhook.kind})` : 'None yet'}</div>
            </div>
            <div className="dr-fact">
              <div className="dr-fact-label">Rejected since restart</div>
              <div className="dr-fact-value">
                {status.rejected.last_at
                  ? `${status.rejected.count}, last ${when(status.rejected.last_at)} (${REASON[status.rejected.last_reason] ?? status.rejected.last_reason})`
                  : String(status.rejected.count)}
              </div>
            </div>
            <div className="dr-fact">
              <div className="dr-fact-label">Last sweep</div>
              <div className="dr-fact-value">
                {status.sweep
                  ? `${when(status.sweep.finished_at)}: ${status.sweep.ok ? 'ok' : 'failed'}, ${status.sweep.applied} applied, ${status.sweep.failed} failed`
                  : 'None yet'}
              </div>
            </div>
          </div>
          <ul className="dr-hint">{syncHints(status).map((h) => <li key={h}>{h}</li>)}</ul>
          {status.kyidentity_url.startsWith('https://') && (
            <a href={status.kyidentity_url} target="_blank" rel="noopener noreferrer">Open KyIdentity <ExternalLink size={12} /></a>
          )}
        </>
      )}
    </section>
  );
};
```

- [ ] **Step 5: Run the panel tests to see them pass.** Run `cd web && npm test -- src/components/BrandingPanel.test.tsx src/components/SyncPanel.test.tsx`. Expected: PASS.

- [ ] **Step 6: Write the failing Overview tests.** In `web/src/pages/Dashboard.test.tsx`:
  - (a) Add `waitFor` to the `@testing-library/react` import.
  - (b) Add below `const up = ...`:

    ```tsx
    const SYNC = {
      webhook: { at: '2026-10-02T03:00:00Z', kind: 'user.updated' }, rejected: { count: 0, last_at: null, last_reason: '' },
      sweep: { finished_at: '2026-10-02T03:01:00Z', ok: true, error: '', applied: 0, failed: 0, failing_since: null },
      kyidentity_url: 'https://id.example.com',
    };
    ```

  - (c) In the first test's `serve({...})`, add `'/api/admin/matrix/sync-status': json(SYNC),`. Before its `fireEvent.click`, add:

    ```tsx
      expect(await screen.findByText(/^Last webhook .*; last sweep ok$/)).toBeTruthy();
      fireEvent.click(within(screen.getByRole('region', { name: 'KyIdentity sync' })).getByRole('button'));
      expect(onNavigate).toHaveBeenCalledWith('settings');
    ```

  - (d) In the second test's `serve({...})`, add `'/api/admin/matrix/sync-status': json({ error: 'Chat (Matrix) is not set up on this server', code: 'matrix_disabled' }, 404),`. At its end, add:

    ```tsx
      await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).startsWith('/api/admin/matrix/sync-status'))).toBe(true));
      expect(screen.queryByRole('region', { name: 'KyIdentity sync' })).toBeNull();
    ```

  - (e) Append:

    ```tsx
    it('shows a failing sweep on the sync card', async () => {
      serve({
        '/api/admin/health': json({ matrix: true, components: [up('kymessages')] }),
        '/api/admin/matrix/users': json({ users: [], total: 0, offset: 0, limit: 1, directory_url: '' }),
        '/api/admin/matrix/sync-status': json({ ...SYNC, sweep: { ...SYNC.sweep, ok: false, error: 'MAS token: HTTP 401', failing_since: '2026-10-02T02:00:00Z' } }),
        '/api/backup/status': json({}),
      });
      render(<Dashboard settings={null} user={null} onNavigate={() => {}} />);
      const text = await within(await screen.findByRole('region', { name: 'KyIdentity sync' })).findByText(/^Offboarding sweep failing since /);
      expect(text.className).toBe('dr-danger');
    });
    ```

- [ ] **Step 7: Run them to see them fail.** Run `cd web && npm test -- src/pages/Dashboard.test.tsx`. Expected: FAIL, `Unable to find role="region" and name "KyIdentity sync"`.

- [ ] **Step 8: Implement the card, the Settings panels and the shell refresh.**
  - In `web/src/pages/Dashboard.tsx`:
    - (a) Add `RefreshCw` to the `lucide-react` import, and `import { parseSyncStatus, syncCard } from '../components/SyncPanel';`.
    - (b) Change `interface Card { text: string; tone: 'success' | 'danger' | 'muted' }` to `interface Card { text: string; tone: 'success' | 'warning' | 'danger' | 'muted' }`.
    - (c) After `const [backup, setBackup] = useState<Card>(checking);`, add:

      ```tsx
        // null until known, and stays null without Matrix: no sync, no card.
        const [sync, setSync] = useState<Card | null>(null);
      ```

    - (d) Inside the effect, before `return () => controller.abort();`, add:

      ```tsx
          get('/api/admin/matrix/sync-status')
            .then(async (res) => {
              if (await isMatrixDisabled(res)) return;
              if (!res.ok) throw new Error(`HTTP ${res.status}`);
              setSync(syncCard(parseSyncStatus(await res.json())));
            })
            .catch((err: unknown) => {
              if (!controller.signal.aborted) setSync({ text: `Unavailable: ${err instanceof Error ? err.message : 'request failed'}`, tone: 'danger' });
            });
      ```

    - (e) In `cards`, after the `Backups` entry, add:

      ```tsx
          ...(sync ? [{ title: 'KyIdentity sync', card: sync, Icon: RefreshCw, tab: 'settings', action: 'Open Settings' }] : []),
      ```

  - Replace `web/src/pages/Settings.tsx`'s imports, props and signature, and add the panels after the existing grid:
    - (a) After `import { ThemeSwitcher } from '../components/ThemeSwitcher';`, add:

      ```tsx
      import { BrandingPanel } from '../components/BrandingPanel';
      import { SyncPanel } from '../components/SyncPanel';
      ```

    - (b) Replace the props and signature with:

      ```tsx
      interface SettingsProps {
        settings: any;
        /** Re-reads /api/settings so the shell shows a new name. */
        onBrandingChanged: () => void;
      }

      export const Settings: React.FC<SettingsProps> = ({ settings, onBrandingChanged }) => {
      ```

    - (c) Replace the file's last four lines (`      </div>` / `    </div>` / `  );` / `};`) with:

      ```tsx
            </div>

            <div style={{ display: 'grid', gap: '20px', marginTop: '20px' }}>
              <BrandingPanel onChanged={onBrandingChanged} />
              <SyncPanel />
            </div>
          </div>
        );
      };
      ```

  - In `web/src/App.tsx`, change `<Settings settings={settings} />` to `<Settings settings={settings} onBrandingChanged={() => void loadSettings()} />`.
  - In `web/src/styles/theme.css`, after `.app-brand img { border-radius: .45rem; flex: none; }`, add:

    ```css
    /* Admin-chosen names run to 64 characters, possibly without spaces: wrap, never widen the shell. */
    .app-brand { min-width: 0; }
    .app-brand span { min-width: 0; overflow-wrap: anywhere; }
    ```

  - In `web/src/pages/Login.tsx`, change `<h1 style={{ fontSize: '24px', fontWeight: 'bold' }}>` to `<h1 style={{ fontSize: '24px', fontWeight: 'bold', overflowWrap: 'anywhere' }}>`.

- [ ] **Step 9: Run the whole web suite and rebuild.** Run `cd web && npm test`. Expected: PASS, all files. Run `npm run build`. Expected: PASS (strict TypeScript, no unused imports), and `git -C .. status --short web/dist` lists the rebuilt bundle. Run `grep -rn "blob:" src --include='*.tsx' | grep -v test`. Expected: no output.

- [ ] **Step 10: DOX.** In `web/AGENTS.md`:
  - (a) Replace `- Product names, document title and manifest use KyMessages.` with:

    ```markdown
    - The header, login page and Overview show `/api/settings` `app_name` (the admin's name, else `KY_APP_NAME`); a 64-character name wraps (`.app-brand`, the login `<h1>`) and never widens the shell. Document title and manifest use KyMessages.
    ```

  - (b) Replace the `Dashboard.tsx` bullet with:

    ```markdown
    - `Dashboard.tsx` (Overview) has the cards Chat health, Matrix users and Backups, and with Matrix a fourth, KyIdentity sync (`syncCard`: ok, "Needs attention: <first hint>", or failing since). Each comes from its own admin route and fails alone; its button calls `onNavigate('health'|'users'|'backup'|'settings')`. With Matrix off the users card says chat is not set up and there is no sync card.
    ```

  - (c) Add after it:

    ```markdown
    - `Settings.tsx` holds `BrandingPanel` and `SyncPanel`. Branding: name (Save, "Use default (<KY_APP_NAME>)"), logo upload (`image/png`, refused over 1 MiB before sending) and reset, all through `adminFetch`; the preview is `/app-icon.png?v=<sha256>` or `?v=default`, never a `blob:` URL (CSP `img-src 'self' data:`); `elementNotice` says what Element shows while it differs; `onBrandingChanged` makes the shell re-read `/api/settings`. Sync: nothing with Matrix off; last accepted webhook, rejections since restart, last sweep, hints and an https-only KyIdentity link. `syncState`, `syncHints` and `syncCard` are pure and vitest-tested; hints are computed here, never on the server.
    ```

- [ ] **Step 11: Commit** (including `web/dist`). Message: `web: Branding and KyIdentity sync panels in Settings, sync card on the Overview`.

---

### Task 6: Browser regressions for Settings

**Files:**
- Modify: `web/browser/ui.spec.mjs`
- Modify: `web/browser/AGENTS.md`, root `AGENTS.md` (Verification, the browser bullet)

**Interfaces:**
- Consumes: Task 5's accessible names; the built `web/dist` and the server binary.
- Produces: no code interface.

- [ ] **Step 1: Write the regression.** In `web/browser/ui.spec.mjs`, directly after `await page.screenshot({ path: testInfo.outputPath('settings.png'), fullPage: true });`, add:

```js
  // Branding, by keyboard, with a 64-character name that must wrap rather than widen the shell.
  // Matrix is off here: Settings has no KyIdentity sync panel. The sign-in above is recent, so
  // there is no step-up prompt. Every change is undone below: the projects share one server.
  const branding = page.getByRole('region', { name: 'Branding' });
  await expect(branding).toBeVisible();
  await expect(page.getByRole('region', { name: 'KyIdentity sync' })).toHaveCount(0);
  const brandName = `Browser-${testInfo.project.name}-`.padEnd(64, 'x');
  await branding.getByLabel('Product name').focus();
  await page.keyboard.type(brandName);
  await page.keyboard.press('Enter');
  await expect(branding.getByRole('status')).toContainText('Name saved');
  await expect(page.locator('.app-brand')).toContainText(brandName);
  await fits(page);
  const icon = await readFile(new URL('../public/app-icon-192.png', import.meta.url));
  await branding.getByLabel(/^Logo/).setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: icon });
  await branding.getByRole('button', { name: 'Upload logo' }).click();
  await expect(branding.getByRole('status')).toContainText('Logo saved');
  await expect(branding.getByAltText('Current logo')).toHaveAttribute('src', /^\/app-icon\.png\?v=[0-9a-f]{64}$/);
  expect(await page.evaluate(async () => (await fetch('/app-icon.png')).headers.get('cache-control'))).toBe('no-cache');
  await fits(page);
  await page.screenshot({ path: testInfo.outputPath('branding.png'), fullPage: true });
  await branding.getByRole('button', { name: 'Reset logo' }).click();
  await expect(branding.getByAltText('Current logo')).toHaveAttribute('src', '/app-icon.png?v=default');
  await branding.getByRole('button', { name: /^Use default/ }).focus();
  await page.keyboard.press('Enter');
  await expect(branding.getByRole('status')).toContainText('Name reset to KyMessages');
  await expect(page.locator('.app-brand')).toHaveText('KyMessages');
  expect(violations).toEqual([]);
```

- [ ] **Step 2: Build what the suite runs, then run it.** From the repo root, run `cd web && npm run build && cd .. && go build -o .browser/server ./cmd/server && cd web && npx playwright install chromium firefox && npm run test:browser`. Expected: 6 passed (light-390, light-1280, dark-390, dark-1280, firefox-light-1280, firefox-dark-390), each well inside the 45 s timeout (about 4–5 s in a dry run). To see the regression bite, temporarily remove the two `.app-brand` lines from `theme.css`, rebuild, and run `npx playwright test --project dark-390`. Expected: FAIL at `fits(page)` (`scrollWidth <= innerWidth`). Restore the lines and rebuild before committing.

- [ ] **Step 3: DOX.**
  - In `web/browser/AGENTS.md` Local Contracts, after the bullet starting `- The suite also covers Users and Rooms`, add:

    ```markdown
    - Settings Branding at every project: a 64-character name typed by keyboard (the header wraps it), a PNG upload previewed by digest with `/app-icon.png` served `no-cache`, then both resets, which every project performs because the projects share one server. With Matrix off there is no KyIdentity sync panel.
    ```

  - In the root `AGENTS.md` Verification, change `the Users and Rooms (Matrix off; Rooms reached from Users by keyboard), Health, Audit and Overview pages;` to `the Users and Rooms (Matrix off; Rooms reached from Users by keyboard), Health, Audit, Overview and Settings branding (rename by keyboard, logo upload and resets) pages;`.

- [ ] **Step 4: Commit.** Message: `browser: Settings branding across themes, widths and keyboard`.

---

### Task 7: Live proof in the acceptance harness, operator docs and status

**Files:**
- Create: `scripts/matrix-acceptance/textpng/main.go`
- Modify: `scripts/matrix-acceptance/e2e.mjs` (usage comment, a `title` scenario)
- Modify: `scripts/matrix-acceptance.sh` (header comment, the `console` step, a new `settings` step)
- Modify: `README.md` (Operator console), `docs/RESTORE.md` (step 6), root `AGENTS.md` (status line, Verification)

**Interfaces:**
- Consumes: every route above; the running stack the harness builds; `app_api`, `app_login`, `hcurl`, `status`, `expect`, `eventually`, `ok`, `pass`, `step`, `matrix_init`, `e2e`, `$jar`, `$admin_pass`, `$repo`, `$scratch`, `$state`, `$KY_APP_URL`, `$KY_KYIDENTITY_ISSUER` (all defined in `scripts/matrix-acceptance.sh`).
- Produces:
  - `go run ./scripts/matrix-acceptance/textpng` writes a PNG fixture with a `tEXt` chunk to stdout.
  - `go run ./scripts/matrix-acceptance/textpng -check FILE` exits 0 only for a decodable PNG with no text chunk and no marker.
  - `node e2e.mjs title BRAND` passes once Element's tab title contains BRAND.

- [ ] **Step 1: The logo fixture tool.** Create `scripts/matrix-acceptance/textpng/main.go`:

```go
// Command textpng makes and checks the acceptance harness's logo fixture. With no argument it
// writes to stdout a 64x64 PNG carrying a tEXt chunk whose text holds a marker; with -check FILE
// it fails unless FILE decodes as a PNG and holds no text chunk and no marker. Harness only.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
)

const marker = "kymatrix-logo-comment"

func main() {
	switch {
	case len(os.Args) == 1:
		if _, err := os.Stdout.Write(fixture()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case len(os.Args) == 3 && os.Args[1] == "-check":
		if err := check(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: textpng [-check FILE]")
		os.Exit(2)
	}
}

func fixture() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range 64 {
		img.Set(i, i, color.NRGBA{R: 191, G: 63, B: 24, A: 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img) // a valid in-memory image always encodes
	p := b.Bytes()
	data := []byte("Comment\x00" + marker)
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(c, "tEXt"...)
	c = append(c, data...)
	c = binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(append([]byte("tEXt"), data...)))
	// After the signature (8 bytes) and IHDR (25 bytes).
	return append(append(append([]byte{}, p[:33]...), c...), p[33:]...)
}

func check(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%s does not decode as a PNG: %w", path, err)
	}
	for _, bad := range []string{"tEXt", "zTXt", "iTXt", marker} {
		if bytes.Contains(b, []byte(bad)) {
			return fmt.Errorf("%s still carries %q", path, bad)
		}
	}
	fmt.Printf("%dx%d\n", img.Bounds().Dx(), img.Bounds().Dy())
	return nil
}
```

Run `go run ./scripts/matrix-acceptance/textpng > /tmp/kymatrix-logo.png && go run ./scripts/matrix-acceptance/textpng -check /tmp/kymatrix-logo.png; echo "exit $?"`. Expected: `/tmp/kymatrix-logo.png still carries "tEXt"`, go run's `exit status 1`, then `exit 1`: the check catches the fixture's own chunk. Run `go vet ./scripts/...`. Expected: no output.

- [ ] **Step 2: Element's title scenario.** In `scripts/matrix-acceptance/e2e.mjs`:
  - (a) Change the second usage line to `//        node e2e.mjs room|token|disabled|reread|reads USER` followed by a new line `//        node e2e.mjs title BRAND`.
  - (b) Add before `const scenarios = ...`:

    ```js
    // Element's tab title carries the brand the console set: each page load fetches config.json.
    async function title() {
      const page = await launch('element');
      await page.goto(`${CHAT}/#/login`);
      await page.waitForFunction((brand) => document.title.includes(brand), user, { timeout: 30000 });
      console.log(`  ok: Element's title is "${await page.title()}"`);
    }
    ```

  - (c) Add `title` to the `scenarios` object: `const scenarios = { prove, refused, compat, noclaim, room, token, disabled, reread, reads, media, restored, title };`.

- [ ] **Step 3: Sync status in the console step.** In `scripts/matrix-acceptance.sh`, directly after the line `	already_ended,ended "the audit API shows both session-end rows, newest first"` (the last check of `step console`, before its `pass`), add:

```bash
# KyIdentity sync status after the offboarding steps: their deliveries were accepted and the
# sweep ran clean. A badly signed delivery is refused and counted in memory only.
app_api GET /api/admin/matrix/sync-status >"$state/sync.json"
expect "$(jq -r '.webhook.kind // "none" | startswith("user.")' "$state/sync.json")" true "sync status shows an accepted KyIdentity webhook"
expect "$(jq -r '"\(.sweep.ok) \(.sweep.failing_since)"' "$state/sync.json")" "true null" "sync status shows an ok sweep"
expect "$(jq -r .kyidentity_url "$state/sync.json")" "$KY_KYIDENTITY_ISSUER" "sync status links KyIdentity"
rejected=$(jq -r .rejected.count "$state/sync.json")
expect "$(status POST "$KY_APP_URL/api/sso/kyidentity/sync" -H 'Content-Type: application/scim+json' \
	-H 'X-KySignOn-Signature: v1=0000000000000000000000000000000000000000000000000000000000000000' \
	-H "X-KySignOn-Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" -H 'X-KySignOn-Event-Type: user.updated' \
	-H 'X-KySignOn-Event-ID: kymatrix-bad-signature' -d '{"id":"nobody"}')" 401 "a badly signed delivery is refused"
expect "$(app_api GET /api/admin/matrix/sync-status | jq -r '"\(.rejected.count) \(.rejected.last_reason)"')" \
	"$((rejected + 1)) bad_signature" "sync status counts it as a bad signature"
```

- [ ] **Step 4: The settings step on the restored stack.** In `scripts/matrix-acceptance.sh`, insert this block between the `rooms` step's closing `pass` and the separator line above `# Runs last: it changes MAS's config and routing, which the steps above must not see.`:

```bash
# ---------------------------------------------------------------------------------------
# Console settings on the restored stack. A rename reaches Element's /config.json and title with
# no restart, through the app's one writable file under ./matrix (same inode). An uploaded logo
# is served as its re-encoding, its text chunk gone. A matrix-init re-run is undone by the next
# maintenance tick. Resets return the defaults. Before reproduce and no-username, which change
# sign-in for everyone.
step settings
element_cfg=$scratch/matrix/element/config.json
chat_brand() { hcurl -fsS https://chat.kymatrix.test/config.json | jq -r .brand; }
inode() { stat -c %i "$element_cfg"; }
ino=$(inode)
brand="Kymatrix Chat $$"
app_login "$admin_pass"
app_api PUT /api/admin/branding/name "$(jq -n --arg n "$brand" '{name: $n}')" >"$state/branding.json"
expect "$(jq -r '[.name, .element.brand, (.element.error // "-")] | join("|")' "$state/branding.json")" "$brand|$brand|-" \
	"the console saved the name and patched Element"
expect "$(chat_brand)" "$brand" "Element's /config.json carries the new brand"
expect "$(inode)" "$ino" "the app rewrote Element's config in place"
expect "$(hcurl -fsS "$KY_APP_URL/api/settings" | jq -r .app_name)" "$brand" "the login page shows the name"
e2e title "$brand"
ok "Element's tab title shows the brand"

go run -C "$repo" ./scripts/matrix-acceptance/textpng >"$state/logo.png"
if go run -C "$repo" ./scripts/matrix-acceptance/textpng -check "$state/logo.png" >/dev/null 2>&1; then
	echo "  FAILED: the fixture's text chunk went unnoticed" >&2
	false
fi
ok "the logo fixture carries a tEXt chunk"
app_login "$admin_pass"
csrf=$(awk '$6 == "ky_csrf" { print $7 }' "$jar")
hcurl -fsS -b "$jar" -X PUT -H "Origin: $KY_APP_URL" -H 'Content-Type: image/png' -H "X-CSRF-Token: $csrf" \
	--data-binary "@$state/logo.png" "$KY_APP_URL/api/admin/branding/logo" >"$state/logo.json"
hcurl -fsS -D "$state/icon.headers" -o "$state/icon.png" "$KY_APP_URL/app-icon.png"
expect "$(sha256sum "$state/icon.png" | awk '{print $1}')" "$(jq -r .logo.sha256 "$state/logo.json")" "/app-icon.png serves the stored logo"
go run -C "$repo" ./scripts/matrix-acceptance/textpng -check "$state/icon.png" >/dev/null
ok "the served logo decodes and its text chunk is gone"
grep -qi '^cache-control: no-cache' "$state/icon.headers" || { echo "  FAILED: /app-icon.png is cacheable" >&2; false; }
ok "/app-icon.png is revalidated on every load"

# matrix-init renders the default brand in place, so a running Element sees it at once; the next
# maintenance tick (every minute) puts the console's name back.
matrix_init >"$state/init4.out"
expect "$(inode)" "$ino" "matrix-init rewrote Element's config in place"
expect "$(stat -c %a "$element_cfg")" 644 "and left it readable by Element's nginx"
eventually 75 "$brand" "the maintenance tick restored the console's name after matrix-init" chat_brand

app_login "$admin_pass"
app_api DELETE /api/admin/branding/logo >/dev/null
app_api PUT /api/admin/branding/name '{"name": ""}' >/dev/null
expect "$(chat_brand)" KyMessages "resetting the name restores Element's default brand"
hcurl -fsS -o "$state/icon-default.png" "$KY_APP_URL/app-icon.png"
cmp -s "$state/icon-default.png" "$repo/web/dist/app-icon.png" || { echo "  FAILED: the stamp did not come back after the logo reset" >&2; false; }
ok "resetting the logo serves the embedded stamp again"
expect "$(app_api GET '/api/admin/audit?limit=100' | jq -r '[.records[] | select(.action | startswith("admin.brand_")) | "\(.action) \(.outcome)"] | join(",")')" \
	"admin.brand_name saved,admin.brand_logo reset,admin.brand_logo saved,admin.brand_name saved" "the audit shows every branding change, newest first"
pass
```

Also, in the header comment, after `# permanently deletes a throwaway room, audited.`, add:

```bash
# It then renames the product and replaces its logo from the console: Element's /config.json
# and title and /app-icon.png follow with no restart, and a matrix-init re-run is undone by the
# next maintenance tick. After offboarding, sync status shows an accepted webhook and an ok
# sweep, and counts a badly signed delivery.
```

- [ ] **Step 5: Run the harness.** Run `bash -n scripts/matrix-acceptance.sh`. Expected: no output. Run `make matrix-acceptance` (needs Docker, node, Playwright Chromium in `scripts/matrix-acceptance` and a KyIdentity checkout at `KYIDENTITY_SRC`). Expected: the summary lists `PASS console` and `PASS settings` among every other step, and the run exits 0. If the environment lacks these, say "acceptance not run: <missing piece>" in the hand-off instead of claiming it passed: the CI job `matrix-acceptance` is then the proof.

- [ ] **Step 6: Operator docs.**
  - In `README.md`, under `### Operator console`, after the `- **Audit** ...` bullet, add:

    ```markdown
    - **Settings → Branding** sets the product name (1–64 characters; blank returns to
      `KY_APP_NAME`) and the logo (PNG only, at most 1 MiB and 1024×1024 pixels, re-encoded so
      no embedded text or metadata survives). Both show on the next page load of the console,
      its sign-in page and Element, with no restart: the app changes only the `brand` key of
      `matrix/element/config.json`, in place, through the one read-write file it mounts under
      `./matrix`, and puts it back within a minute if `matrix-init` re-renders it. If it cannot
      write that file, the name is still saved and Settings shows what Element says and why.
      Changes need a sign-in from the last 10 minutes and are audited (`admin.brand_name`,
      `admin.brand_logo`). `KY_APP_NAME` stays the backup service name: capsules and
      KyRecovery pairing never see the console name.
    - **Settings → KyIdentity sync** (with Matrix) shows the last directory webhook KyMessages
      accepted, deliveries refused since it started (and why: bad signature, stale clock,
      missing secret, malformed), and the last offboarding sweep, with what to fix. The
      Overview shows the same as a card. A change made while KyMessages was down waits in
      KyIdentity as an uncertain write; resume it there.
    ```

  - In `README.md`, replace `Settings are not built yet. \`make matrix-acceptance\` proves encrypted` / `storage in Element, a closed server, offboarding, room close and delete, and backup then restore of a lost host (needs` with:

    ```markdown
    `make matrix-acceptance` proves encrypted
    storage in Element, a closed server, offboarding and its sync status, room close and delete, branding reaching Element, and backup then restore of a lost host (needs
    ```

  - In `docs/RESTORE.md` step 6, change `root and refuses key files it does not own. Then \`docker compose up -d\`.` to:

    ```markdown
    root and refuses key files it does not own. Then `docker compose up -d`. On start the app
       sets Element's `brand` back to the restored console name (step 2's `matrix-init` rendered
       the default).
    ```

- [ ] **Step 7: Status and verification in the root `AGENTS.md`.**
  - Replace `settings (5c) are open.` with `settings (5c: branding and KyIdentity sync status, with \`internal/branding\`) are shipped.`
  - In Verification, change `within the same 30s, and the audit API shows the \`matrix.session_end\` rows.` to:

    ```markdown
    within the same 30s, and the audit API shows the `matrix.session_end` rows. Sync status then shows an accepted webhook and an ok sweep, and counts a badly signed delivery as `bad_signature`.
    ```

  - In Verification, change `  leaves \`kymessages-console\` unlocked.` to:

    ```markdown
      leaves `kymessages-console` unlocked. Settings follows on the restored stack: a console rename
      reaches Element's `/config.json` (same inode) and title; a PNG carrying a `tEXt` chunk is
      served at `/app-icon.png` re-encoded without it, `no-cache`; a `matrix-init` re-run is undone
      within one maintenance tick; resets restore the defaults; every change is audited.
    ```

- [ ] **Step 8: Final DOX pass and full checks.** Re-read the DOX chain for every changed path: root, `internal/api`, `internal/sso`, `internal/matrixsync`, `internal/matrixinit`, `internal/branding`, `web`, `web/browser`. Confirm that no text still says settings are open or unbuilt. Run `grep -rn "Settings are not built\|settings (5c) are open" --include='*.md' . | grep -v node_modules | grep -v docs/superpowers/`. Expected: no output. Run `make ci`. Expected: `==> Local CI checks passed`. With a Postgres at hand, also run `make test-postgres`. Expected: PASS.
  - `docs/superpowers/specs/2026-10-01-matrix-platform-design.md` decision 11 already points to 5c; leave it unchanged.
  - The spec needs no amendment: it says what the plan implements. The open-question rulings are recorded in the AGENTS.md contracts.

- [ ] **Step 9: Commit.** Message: `acceptance: branding and sync status proven live; docs: console settings shipped`.

---

## Self-review

- **Spec coverage.**
  - Section 1:
    - settings and the effective name: Task 3;
    - the Element reconcile at startup, after save and on each tick: Tasks 1 and 3;
    - matrix-init in place: Task 2;
    - logo rules and `/app-icon.png`: Tasks 1 and 3;
    - routes and audit: Task 3;
    - the Element write failure: Tasks 3 and 5.
  - Section 2:
    - webhook record, rejections, sweep record and route: Task 4;
    - panel and hints: Task 5;
    - Overview card: Task 5.
  - Section 3:
    - Compose: Task 2;
    - the known limit: `internal/branding/AGENTS.md`;
    - unit tests: Tasks 1–5;
    - acceptance: Task 7;
    - browser regressions: Task 6.
  - Out of scope stays out: no colours, no favicon or manifest, no "sweep now", no history, no Element X.
- **Placeholders.** None: every code step carries its code, and every run step has its command and expected result.
- **Type consistency.**
  - `ReconcileBrand(ctx) error` and `maintenanceLoop(ctx, st, brand func(context.Context) error, done)`.
  - `brandNameKey` and `brandLogoKey`; `notExtra` (Task 3, extended in Task 4).
  - `sso.WebhookRecordKey`, `sso.WebhookRecord`, `sso.Rejections`, `sso.Reject*`.
  - `matrixsync.SweepRecordKey`, `matrixsync.SweepRecord`; `Syncer.record`, `storedStreak`, `sweep`.
  - `branding.ValidateName`, `NormalizePNG`, `PatchElementBrand`, `ElementBrand`, `MaxLogoBytes`, `ErrNotPNG`, `ErrLogoTooLarge`, `ErrLogoDimensions`.
  - Web: `parseBranding`, `elementNotice`, `parseSyncStatus`, `syncState`, `syncHints`, `syncCard`, `UNCERTAIN`.
  - The JSON field names match between the Go views and the TypeScript parsers.
- **Review Focus.** Each of the five lines has its test in the owning task: Tasks 3 and 4 (1), Tasks 3 and 5 (2), Task 3 (3, 4 and 5).
