# Browser regressions

## Purpose

Exercise the built embedded frontend through Chromium and Firefox with the real Go server and CSP.

## Ownership

This directory owns test setup, disposable server launch and UI assertions. The parent owns Playwright configuration and CI wiring.

## Local Contracts

- Never reuse a development or production server. Launch the compiled `.browser/server` with a minimal environment and an owned temporary data directory; remove that directory on exit.
- Use loopback only, with `KY_APP_URL` matching the test origin. Bootstrap credentials are disposable test values, not deployment defaults.
- Test light/dark at 390px and 1280px on Chromium, plus Firefox at light/1280 and dark/390, using real authentication and API state. Do not disable service workers, relax CSP/CSRF, or substitute mocked responses.
- The overview verifies real admin storage totals/limits and explicit refresh at
  both widths/themes, including preserving the opened details disclosure. It uses
  the empty disposable database; nonempty, retired and error cases have UI/store/API
  tests. Keep that evidence scope explicit.
- The backup flow pins the repository's synthetic public-key fixture and makes
  local sealed copies through the real API; no private recovery key or shares are
  present. Check the persisted result and layout at both widths/themes.
- The flow ends on "My account": the not-yet-available notice, the KySignOn-account message for the local bootstrap admin and the browser line, with layout checked. Firefox must report "can run"; Playwright's Chromium is not Chrome or Edge, so it must report feature-complete but unverified.
- Login allows 10 attempts per account per 15 minutes and the harness spends one in setup plus one per project: only `dark-390` and `firefox-light-1280` also try a wrong password. Do not relax the limiter; budget attempts before adding projects.
- Screenshots and failure traces live in ignored `test-results/` and CI artifacts, not production assets.

## Work Guidance

Prefer browser-native behavior and assertions over screenshot-only checks. A passing suite covers these workflows, not every page or a complete accessibility audit.

## Verification

Run `npm run test:browser` from `web/` after rebuilding the embedded frontend and server as documented in the parent.

## Child DOX Index

None.
