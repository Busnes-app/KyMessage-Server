# Browser regressions

## Purpose

Exercise the built embedded frontend through Chromium with the real Go server and CSP.

## Ownership

This directory owns test setup, disposable server launch and UI assertions. The parent owns Playwright configuration and CI wiring.

## Local Contracts

- Never reuse a development or production server. Launch the compiled `.browser/server` with a minimal environment and an owned temporary data directory; remove that directory on exit.
- Use loopback only, with `KY_APP_URL` matching the test origin. Bootstrap credentials are disposable test values, not deployment defaults.
- Test light/dark at 390px and 1280px, using real authentication and API state. Do not disable service workers, relax CSP/CSRF, or substitute mocked responses.
- Screenshots and failure traces live in ignored `test-results/` and CI artifacts, not production assets.

## Work Guidance

Prefer browser-native behavior and assertions over screenshot-only checks. A passing suite covers these workflows, not every page or a complete accessibility audit.

## Verification

Run `npm run test:browser` from `web/` after rebuilding the embedded frontend and server as documented in the parent.

## Child DOX Index

None.
