# Browser evidence for the isolated MLS proof

Recorded 2026-09-27. These are experiment results, not a production support matrix.
The proof is excluded from deployment. Real Safari, iOS, Android and deployed HTTPS
remain unverified; Linux Playwright WebKit is not a substitute for them.

## Verified subset

Playwright 1.63.0 Chromium and Firefox pass the complete HTTP/UI suite (55 cases,
one duplicate cross-engine skip) and OIDC suite (eight cases) after installing
controlled clocks before page navigation. The manual lifecycle suite previously
passed 20 cases, including 260 encrypted messages. The added native Ed25519 test
passes 256 generations per engine without retries. Typecheck/build passes.

Install a controlled clock before the app creates timers, as required by
[Playwright's clock contract](https://playwright.dev/docs/clock). Installing it
mid-session gave undefined behavior; the helpers now install before navigation.

## Linux WebKit: open failure

Image: `mcr.microsoft.com/playwright:v1.63.0-noble`, pinned digest
`sha256:eff16c30e6f3f4af0a03fa4b706120d5e9b0891c344a27d64559aff5900a4a27`.
The host lacks this build's libraries, so tests ran in a disposable container with
2 CPUs, 2 GiB memory (no additional swap), 512 MiB shared memory and the source
copied from a read-only mount. The Go HTTP fixture was built with `CGO_ENABLED=0`
and `-tags=mlsproof`; only its scratch configuration replaced `go run` with that
binary. No product deployment or dependency changes were made.

Results on a container with a default network route:

- Manual lifecycle: 10/10 pass, before adding the native diagnostic.
- HTTP/UI: 25 pass, one intentional cross-engine skip, two failures during new-key
  creation (room-switching setup and replacement after local-vault deletion).
- OIDC: 4/4 pass against the disposable issuer, including reset and WebSocket flow.
- `tests/native-crypto.spec.ts`: fails on **generation 30/256**, calling only
  `crypto.subtle.generateKey('Ed25519', true, ['sign', 'verify'])` on a blank routed
  page, with `OperationError`. No MLS library or application script is loaded.

Earlier failures appeared in different key-creation/rejoin scenarios. Temporary
operation-only instrumentation traced a device-creation failure to native
`generateKey`; the blank-page regression reproduces it without instrumentation.
This establishes a browser-primitive failure in the tested build, not its internal
cause and not the explanation of every earlier failure. Keep WebKit out of the
verified subset. Do not retry crypto failures until a test turns green or silently
change the provider to hide them. The runnable regression remains in the ordinary
manual suite; CI runs the verified Chromium/Firefox subset.

A separate fixture pitfall: Linux WebKit reported `navigator.onLine=false` with
Docker `--network none` and with an internal network lacking a default route.
The chat therefore correctly paused polling. Poll/revocation cases passed once a
normal bridge supplied a route. Those failures were not evidence of a broken
receive loop. Fixture and test URLs stay on container loopback; no host ports are
published. Browser offline scenarios still use Playwright's explicit offline mode.

## Reproduce

On a Linux host with supported browser dependencies:

```sh
cd mls-proof
npm ci
npx playwright install --with-deps chromium firefox webkit
npm run build
npm test -- --project=webkit native-crypto.spec.ts
npm test -- --project=webkit
npm run test:delivery -- --project=webkit
npm run test:oidc -- --project=webkit
```

Run suites sequentially: their preview and fixture ports are shared. A failed native
diagnostic keeps the browser gate open even if a single lifecycle run happens to
pass. Recheck a new engine build and real target platforms before changing support.
