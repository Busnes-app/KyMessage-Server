# Purpose

Non-UI core of the encrypted-chat client and the main target of the suite review when chat
is integrated (security-audit run, PR security reviewer, task and branch reviews).

## Ownership

Delivery, wire validation, vault, device and MLS calls, markdown rendering and the
session (`src/`). `mls-proof/` owns the UI, harness and tests that consume it.

## Local Contracts

- No imports from `web/`. Nothing in `web/` imports this package until the gate
  (`scripts/check-chat-gate.sh`) is deliberately lifted; `/chat-core/` stays in `.dockerignore`.
- No IndexedDB key, record layout or wire-format change without a migration plan.
- Dependency versions are pinned; install with `npm ci`.
- Behavior contracts for these modules are in `mls-proof/AGENTS.md`.

## Work Guidance

## Verification

- `npm run typecheck` here, the `mls-proof` suites, and `bash scripts/check-chat-gate.sh`.

## Child DOX Index
