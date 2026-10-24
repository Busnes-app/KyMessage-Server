# Contributing to KyMessages

[Product overview](README.md) · [Development](docs/DEVELOPMENT.md) · [Assurance case](docs/ASSURANCE-CASE.md)

## Reporting bugs and asking for features

Open a [GitHub issue](https://github.com/Busnes-app/KyMessage-Server/issues). Say which
version you run (the image tag, or `internal/appversion/VERSION` for a source build), whether the
Matrix overlay is on, what you did, what you expected and what happened. Leave passwords, tokens,
keys and message contents out of the issue.

**Security problems are not issues.** Report them privately through the repository's
**Security** tab → **Report a vulnerability**, as the
[Busnes-app security policy](https://github.com/Busnes-app/.github/blob/main/SECURITY.md)
describes.

## Making a change

1. Branch from `master` and open a pull request against `master`. `master` takes changes only
   through pull requests.
2. Read [AGENTS.md](AGENTS.md) and every AGENTS.md on the way to the files you change. They are
   binding: a change that alters a contract, workflow or verification updates the nearest owning
   AGENTS.md (and its parents' indexes) in the same pull request, and a changed procedure updates
   its topic guide under `docs/`. Product scope changes start from [docs/PRODUCT.md](docs/PRODUCT.md).
3. Every CI job in `.github/workflows/ci.yml` must pass before a pull request can merge;
   `release-image.yml` will not publish a commit on which one failed. Run `make ci` and the checks
   in [Development → Verification](docs/DEVELOPMENT.md#verification) locally first.
4. If you change `web/`, commit the rebuilt `web/dist`: it is embedded in the binary, and CI fails
   when it does not match a fresh `npm run build`.
5. Describe what changed and how you checked it in the pull request.

## Tests

New behaviour comes with a test that exercises it: Go tests next to the code (they run on SQLite
and PostgreSQL), Vitest for web logic, Playwright browser regressions for console flows, and
`node --test` for the meeting-notes bot. A security change also needs a test for the attack it
stops, not only the happy path; the tests named in the
[assurance case](docs/ASSURANCE-CASE.md#common-weaknesses-and-how-they-are-countered) show the
pattern.

## Coding standard

Contributions follow these style guides. CI enforces the parts a tool can check, so a style
failure is a red build, not a review comment.

| Language | Style guide | Enforced by |
|---|---|---|
| Go | [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) | `gofmt` (must list nothing), `go vet ./...` and `go mod tidy` with no diff, in the `Go build, vet & tests (SQLite)` job (`make lint` and `make tidy-check` locally) |
| TypeScript (`web/`) | The TypeScript compiler in `strict` mode | `tsc -b` in `npm run build`, `Frontend typecheck & build` job |
| Shared browser UI (`web/src/ky-ui/`) | Generated from Busnes-app/ky-ui; never edited here | `node src/ky-ui/check-vendor.mjs`, run before `npm test` and `npm run build` |
| Shell (`scripts/`) | Code that passes [ShellCheck](https://www.shellcheck.net/) | `shellcheck` in the `End-to-end smoke test` job |
| Compose files | The hardening in [AGENTS.md](AGENTS.md) (non-root, no capabilities, read-only, explicit binds) | `scripts/check-compose-proxy.sh` and `scripts/check-compose-matrix.sh` in the Go job |

The meeting-notes bot's JavaScript (`notes/`) and the Python acceptance scripts have no linter;
keep to the style of the file you are editing. Prefer the standard library and the shared
`ky-primitives` packages to new dependencies.
