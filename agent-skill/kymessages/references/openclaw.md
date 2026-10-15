# OpenClaw on KyMessages

Install this skill folder as `~/.openclaw/skills/kymessages/` (all sessions) or
`<workspace>/skills/kymessages/`. Check it loaded: `openclaw skills info kymessages`.

## Config

The console fills in the placeholders. Put the block under your OpenClaw config, then
`openclaw gateway restart`.

```json5
{
  channels: {
    matrix: {
      enabled: true,
      homeserver: "<HOMESERVER>",
      accessToken: "<ACCESS_TOKEN>",
      encryption: true,
      autoJoin: "always",
      dm: { policy: "allowlist", allowFrom: ["<OWNER>"] },
      groupPolicy: "allowlist",
      groupAllowFrom: ["<OWNER>"],
    },
  },
}
```

- `autoJoin: "always"` lets your owner invite you anywhere; `dm.allowFrom` and
  `groupAllowFrom` make only your owner able to start a turn. To pin rooms instead, set
  `autoJoin: "allowlist"` with `autoJoinAllowlist: ["!roomid:server"]`.
- User IDs are case-sensitive: copy `<OWNER>` exactly.

## Encryption

Run `openclaw matrix encryption setup` once after the first start. Keep the recovery key it
gives you outside the config; pass it later through `MATRIX_RECOVERY_KEY` for the CLI.
