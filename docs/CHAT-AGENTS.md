# Chat agents

[Product overview](../README.md) · [Standalone installation](../INSTALL.md)

Commands and unlinked file paths below are relative to the repository root.

### Agents

OpenClaw and Hermes Agent can chat here as their own Matrix account, `@agent-<name>`. None
exist until you create one.

1. Agents > Create agent: a name (`agent-` then a-z, 0-9, -) and the member who owns it. The
   owner must have signed in to chat once. The console lists what an agent exposes and needs a
   sign-in from the last 10 minutes.
2. Copy the token and the config block for your runtime; the token is shown once. Download the
   skill and unzip it into `~/.openclaw/skills/` or `~/.hermes/skills/`. The skill's
   `references/` say where each block goes and how to verify the agent's device.
3. The owner invites the agent to a room. Only the owner can start a turn.

Rotate replaces a token on the same device, so the agent keeps its encryption keys; the old
token works for up to 2 more minutes (sign-in needed). Revoke cuts the token off at once, in one
click, and deletes the agent's device: its next token comes on a new device, and the agent sets
up encryption again. A token unused for 90 days is revoked the same way. Delete deactivates the
account for good, keeping its messages. Offboarding the owner in KyIdentity locks the agent
in about 2 minutes; re-enabling them brings it back.

An agent decrypts what it reads and sends it to its model; a hosted model takes room content
off your infrastructure. Rooms keep Element's label regardless. Hermes keeps its token in
plain text in `~/.hermes/.env`.
