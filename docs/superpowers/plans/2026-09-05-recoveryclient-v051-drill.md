**Repo:** ky_server_base
**PR:** baseline #22 — https://github.com/Busness-app/ky_server_base/pull/22 (merged); implementation PR not created
**Worktree:** /home/yoshi/busness.app/ky_server_base (master)

# Plan: recoveryclient v0.5.1 drill migration

Prepared 2026-09-05 from MySlop post 287. Planning only; implementation and tests have not run. Mirror this document verbatim to `ky-server-base-sealed-token`.

## Verified starting point

- Fetched `origin/master` is `95c7bcc98e81ba34cdd99813d0c29f7608f4aee1`; working tree was clean before this plan.
- `go.mod` pins ky-primitives v0.5.0. The local library's v0.5.1 source confirms the callback is `func(dir string, opened capsule.Manifest) []recoveryclient.Check`.
- `internal/backup/drill.go` captures the original payload recipe. Both HTTP and CLI callers use `Checks(cfg, payload)`; cfg is unused by the checks.
- Existing tests construct scratch files directly. They do not prove checks survive manifest JSON decoding. Wrong recipe types currently skip checks silently.
- SQLite opens with a writable filename DSN; a missing database can be created and pass integrity checking. Required-file failure currently catches the normal missing-database case, but the SQLite check must itself fail for an absent file.
- Existing sealer testing proves a new round trip, not compatibility with a pre-upgrade pairing record.
- v0.5.1 creates separate 0700 scratch directories and sweeps directories older than one hour. HTTP and CLI currently share `<data dir>/drill` without product serialization.
- Post 287 remains the latest post read; the folder was open and unclaimed. Check again before implementing.

## 1. Establish an isolated implementation branch

Read the parent and root AGENTS.md and each affected child again. Refresh the board claim and remote state, then create a worktree from current `origin/master` on `fix/recoveryclient-v051-drill`. Preserve all existing worktrees, especially Sable's locked `.claude/worktrees/recoveryclient`. Record the exact base SHA.

Use one implementation PR. Do not reopen the completed recovery integration or create a separate plan PR. Reconfirm the v0.5.1 tag and API before changing dependencies.

## 2. Upgrade and validate the opened recipe

Files: `go.mod`, `go.sum`, `internal/backup/drill.go`, `internal/backup/drill_test.go`, `internal/api/backup_handlers.go`, `cmd/server/main.go`.

- Pin `github.com/Busness-app/ky-primitives@v0.5.1`, then tidy and verify modules.
- Make `Checks(dir string, opened capsule.Manifest) []recoveryclient.Check` the direct callback. Remove the cfg/payload closure and update both callers and tests.
- Read only `opened.VerificationRecipe`. Normalize decoded `[]any` into strings at this boundary; retain `[]string` support only if direct fixtures need it. Reject non-string or empty members instead of dropping them.
- Require the scaffold's recipe fields: nonempty `required_files`, boolean `check_sqlite_integrity` set to true for its SQLite payload, nonempty `sqlite_paths`, and a well-typed `expected_env` list containing the scaffold's required environment names. Missing, null, incorrectly typed or emptied required checks produce failed `Check` entries. Ensure the database, settings and encryption-key members cannot be omitted from required-file validation; include the recovery public key when carried by the manifest. Extra descriptive fields such as `expected_ports` remain informational.
- Validate file paths before filesystem access: reject absolute, empty, non-clean and escaping paths. Reuse the existing path helper, tightened to reject rather than silently normalize invalid inputs. Require referenced paths to identify extracted manifest members.
- Require regular, nonempty required files. Open SQLite read-only using a correctly escaped URI DSN so a missing database cannot be created and reported healthy. Preserve the existing environment-presence semantics; do not print values.
- Add no parser framework or new dependency; keep product validation in this adapter.

Acceptance: real `recoveryclient.Drill` with a collected SQLite fixture runs named required-file, SQLite-integrity and environment checks after decoding, and succeeds. Real drills with a malformed recipe fail even though sealing/opening succeeds. Table-driven cases cover absent/null/wrong-type fields, mixed lists, unsafe paths, omitted mandatory members, missing/empty files, corrupt or absent SQLite and missing environment variables. Explicitly assert each check group ran; a passing overall result alone is insufficient.

## 3. Protect scratch ownership and pairing compatibility

Files: backup adapter/tests, both drill entry points, and existing API/CLI tests as needed.

- Add one shared product drill entry function used by HTTP and CLI. Because both processes can use the same data directory, an in-memory mutex alone is insufficient. Inspect existing file-lock facilities first; use an existing cross-process advisory lock if available, held across scratch preparation and the library call, released on every return and process exit. Define a busy error and map it to HTTP 409 and a clear CLI failure. Avoid stale sentinel-file locks and library code copies.
- Preserve scratch placement under the data directory, enforce owner-only permissions, and prove cleanup on success and failed checks. Exercise contention with separate processes against the same data directory and verify the active scratch files survive. Different data directories must remain independent.
- Capture a synthetic pairing fixture produced by the v0.5.0 implementation before upgrading. Under v0.5.1, load the same sealed token, URL and key-pin data without rewriting or re-pairing. Use only synthetic credentials. Preserve the exact label `ky_server_base:setting:kyrecovery_token`, deployment key derivation, settings keys and ciphertext format. A test that seals and opens exclusively with the new version is insufficient.
- Retain the existing decrypt guard. Plant a temporary forbidden decrypt call outside `restore`, demonstrate failure naming the call, remove it and demonstrate success. Do not widen exemptions.

Acceptance: compatibility fixture loads unchanged; simultaneous HTTP/CLI drill attempts cannot overlap on one data directory; scratch stays private and is removed; forbidden decryption is detected.

## 4. Verify regressions and update DOX

Preserve #22's final audit hardening: trusted outcome/local-copy facts precede bounded, quoted remote text; remote text cannot introduce fields. Retain admin/CSRF boundaries, local-only backups, no-destination failure, write-once pinning, schedule validation, and unpair retaining the pin, receipt and local copies. Keep backup fixtures explicitly SQLite even when running the suite with PostgreSQL; PostgreSQL backup collection remains unsupported and must fail honestly.

Run focused backup/API/CLI tests first, then:

1. `make ci` (gofmt/vet, SQLite race suite, binary and smoke tests).
2. `go mod tidy`, inspect the intended module diff, `go mod verify`, and `go build ./...`.
3. Under `web`: `npm ci`, `npm test`, `npm run build`, `npm audit --audit-level=high`; verify `git diff --exit-code -- web/dist`.
4. PostgreSQL 17: run `make test-postgres` with a disposable instance, plus the CI-equivalent `KY_TEST_POSTGRES_DSN=... go test -race -count=1 ./...`.
5. `shellcheck scripts/*.sh`, the workflow's govulncheck command, and Docker build/container HTTP and login checks.
6. All GitHub CI jobs and reviewer clearance on the final implementation SHA. Fix findings and recheck affected tests; record the exact tested SHA for human merge.

`make ci` alone does not cover all GitHub checks. Report unavailable checks explicitly; do not treat them as passed.

Update `internal/backup/AGENTS.md` for opened-manifest validation and the shared drill entry point. Update API ownership docs if adding the busy response; update restore documentation only where operator behavior changes. Perform the full DOX pass and correct the parent document's stale scaffold status/copy guidance with verified facts, without asserting unverified progress for other products.

## 5. Keep live proof distinct from code completion

After the implementation is reviewed and the deployment step is authorized, record the deployed SHA and preserve its volume, issuer, encryption key and pairing. Use `KY_BACKUP_ALLOW_PRIVATE_RECOVERY` only for the intended homelab, and `KY_DNS=192.168.1.1` with `docker-compose.lan-dns.yml`. Source deployment requires `up -d --build`.

Verify readiness, unchanged pinned key ID and successful deposit without re-pairing. Record capsule ID, digest, receipt time and local-copy result. If no pairing existed, label the result first-pair proof and compare the KyRecovery fingerprint out of band. Preserve HTTPS and redirect/loopback refusal.

On disposable fixtures, prove local-only delivery, refused replacement key, schedule changes and restore using synthetic 2-of-3 shares. A real custodian-card restore is a separate operator exercise: shares go locally to stdin, never chat, argv or shared files. Neither the library's throwaway-key drill nor the reported KySignOn deposit proves the scaffold's real-card restore.

After merge, mirror exact base and merged SHAs, PR URL, verification results and any remaining live-proof gap to `gridlock-kyrecovery-deposit`. Gridlock may start from `95c7bcc` now; its port must preserve its own identity and sealer label. Update the scaffold folder without marking operational proof complete unless it was actually performed.

## Planning closeout

This document is the only repository change. Existing AGENTS.md files are intentionally unchanged in this planning turn: no implementation contract has changed yet; the required contract corrections are part of step 4. Implementation, CI, reviewer clearance, merge and live proof remain future work.
