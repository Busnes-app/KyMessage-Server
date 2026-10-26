# Fleet Appearance evidence — 2026-10-26

Staged screenshots for the suite Appearance picker (ky-ui 0.4.0 theme catalog, canonical source `4f83f34`). These are
scratch captures of the real application with synthetic data. They are implementation
evidence for the council's independent rating, not a rating: nothing here claims a score.

- `manifest.json`: base commit, OS, toolchain, browser, fixture, every stage's source patch,
  server binary and built-asset hashes, and one record per PNG (viewport, OS colour scheme,
  theme on screen, checked radio, radio count, horizontal overflow, `h1`, SHA-256).
- `captures.csv`: the same per-PNG records as a spreadsheet.
- `checks.csv`: the required checks run for this branch, with exit codes.
- `patches/`: the exact `web/src` + `web/browser` source of each after stage, against the base
  commit (vendored ky-ui included). Built `web/dist` is committed separately.
- `drivers/`: the scratch capture and build scripts, archived as text. They need the recorded
  product harness, a built `.browser`-style server and Playwright from `web/node_modules`.

## Conditions

Disposable SQLite server built from the worktree, real `init-admin`, real login and password
change; administrator only, Matrix not configured. Members sign in only through KyIdentity SSO,
so the member Home/Appearance navigation is covered by unit tests (`AppHeader.test.tsx`,
`Appearance.test.tsx`), not by captures. Clock fixed at 2026-10-27 10:15 America/Chicago.
1280×900 and 390×844, OS light and dark, nothing saved (OS-following Busnes) unless the file name
starts with a theme chosen through the Appearance radios. No auth, CSRF or CSP bypass and no
mocked responses.

## Stages

| Stage | Before | After | What changed |
| --- | --- | --- | --- |
| 01 | `00-before` | `01-after` | ky-ui 0.4.0; the Settings theme section gives way to an Appearance page reachable by every role (members get Home and Appearance rows); 23 native radio cards; saved `ky_theme` IDs kept. |
| 02 | `01-after` | `02-after` | Owner correction: the header theme select is removed (the Appearance row stays); shared description, preview hairline and wrapping from ky-ui `4f83f34`. |
| 03 | `02-after` | `03-after` | Appearance only: a bare `h1` so the title-to-description gap is the shared one, identical in KyPost, KyCalendar and KyMessages. |

Compare `overview` (main view) and `settings` across 00/01/02, and `appearance` from 01.
`ocean-*` and `sky-*` show named themes chosen through the radios. Stage 03 recaptured only the
Appearance views.

## Limitations

- Chromium only for captures; the browser suite covers the picker in Chromium and Firefox.
- No member captures (SSO-only accounts); no Matrix-on views.
- Chromium gives each native radio an empty `style=""`; there are no inline declarations.
