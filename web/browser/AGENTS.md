# Browser regressions

## Purpose

Exercise the built embedded frontend through Chromium and Firefox with the real Go server and CSP.

## Ownership

This directory owns test setup, disposable server launch and UI assertions. The parent owns Playwright configuration and CI wiring.

## Local Contracts

- Never reuse a development or production server. Launch the compiled `.browser/server` with a minimal environment and an owned temporary data directory; remove that directory on exit. `server.mjs [port] [captcha]` serves 5391 with captcha `none` for the main projects and 5392 with `pow` for the `captcha-pow` project (`captcha.spec.mjs`, Chromium), which `globalSetup` never touches.
- `captcha.spec.mjs` signs in through the real proof-of-work: one challenge fetch while idle, exactly one more after a wrong password, then the bootstrap password reaches "Change your password".
- Find sign-in fields by label (`getByLabel('Username')`, `getByLabel('Password')`), not placeholder. The pairing dialog must poll with a POST that has no query string and gets 200.
- Use loopback only, with `KY_APP_URL` matching the test origin. Bootstrap credentials are disposable test values, not deployment defaults.
- Test light/dark at 390px and 1280px on Chromium, plus Firefox at light/1280 and dark/390, using real authentication and API state. Do not disable service workers, relax CSP/CSRF, or substitute mocked responses.
- The backup flow pins the repository's synthetic public-key fixture and makes
  local sealed copies through the real API; no private recovery key or shares are
  present. Check the persisted result and layout at both widths/themes.
- Login allows 10 failed attempts per account and client per 15 minutes (successes do not count); only `dark-390` and `firefox-light-1280` try a wrong password. Do not relax the limiter; budget failures before adding projects.
- The suite also covers Users and Rooms (Matrix off; Rooms by keyboard from Users), Health, the Audit kind filter with keyboard navigation and the Overview, at every project.
- Settings Branding at every project: reset to defaults first (a failed project must not break the next on the shared server), a 64-character name typed by keyboard (the header wraps it), a PNG upload previewed by digest with `/app-icon.png` served `no-cache`, then both resets. With Matrix off there is no KyIdentity sync panel.
- Screenshots and failure traces live in ignored `test-results/` and CI artifacts, not production assets.

## Work Guidance

Prefer browser-native behavior and assertions over screenshot-only checks. A passing suite covers these workflows, not every page or a complete accessibility audit.

## Verification

Run `npm run test:browser` from `web/` after rebuilding the embedded frontend and server as documented in the parent.

## Child DOX Index

None.
