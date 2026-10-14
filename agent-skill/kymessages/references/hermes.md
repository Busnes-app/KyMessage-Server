# Hermes Agent on KyMessages

Install this skill folder as `~/.hermes/skills/kymessages/` (the folder name must stay
`kymessages`). Check it loads: `hermes -s kymessages`.

## Config

The console fills in the placeholders. Add the block to `~/.hermes/.env` and keep that file
mode 0600: it holds your token in plain text.

```dotenv
MATRIX_HOMESERVER=<HOMESERVER>
MATRIX_ACCESS_TOKEN=<ACCESS_TOKEN>
MATRIX_ALLOWED_USERS=<OWNER>
MATRIX_E2EE_MODE=required
```

- `MATRIX_ALLOWED_USERS` is what lets your owner in: Hermes joins rooms only on invites from
  allowed users and answers only them. Keep `MATRIX_ALLOW_ALL_USERS` and
  `GATEWAY_ALLOW_ALL_USERS` unset.
- A room of two members counts as a direct message and skips `MATRIX_ALLOWED_ROOMS`.
- `MATRIX_E2EE_MODE=required` fails closed when encryption cannot start; it needs the
  `matrix` extra and `libolm` (Linux).

## Encryption

First start: set `MATRIX_RECOVERY_KEY_OUTPUT_FILE` to a path of your choice; Hermes writes a
new recovery key there once (mode 0600). Then set `MATRIX_RECOVERY_KEY` to that key and
remove the output-file line. Hermes signs its device with it on every start and logs
`Matrix: cross-signing verified via recovery key`.
