# Agent skill

## Purpose
Ships the `kymessages` skill that chat agents (OpenClaw, Hermes Agent) load to connect to
KyMessages and behave in its rooms, and fills each runtime's config block for the console.

## Ownership
Owns `kymessages/` (the skill: `SKILL.md` and `references/<runtime>.md`) and `skill.go`
(`go:embed`, `Config`, `Zip`). The agent routes in `internal/api` serve both.

## Local Contracts
- One folder, the agentskills `SKILL.md` format both runtimes load. Frontmatter `name` stays
  `kymessages`, equal to the folder name: Hermes skips a skill whose name differs.
- The skill holds no secret and no install-specific value; it advises the agent, and nothing
  security-relevant depends on the agent following it (scope, lock, revoke and expiry are
  server-side).
- Each `references/<runtime>.md`'s first fenced block is that runtime's config block with the
  placeholders `<HOMESERVER>`, `<ACCESS_TOKEN>`, `<OWNER>`. `Config` fills them and is the only
  source of the console's config blocks; it refuses values holding quotes, whitespace, `#`,
  `\`, `` ` `` or `$`. `Runtimes` lists the runtimes with a reference file.
- Blocks require encryption (`encryption: true`; `MATRIX_E2EE_MODE=required`) and serve only
  the owner (`dm.allowFrom` and `groupAllowFrom`; `MATRIX_ALLOWED_USERS`).

## Verification
- `go test ./agent-skill/`

## Child DOX Index
None.
