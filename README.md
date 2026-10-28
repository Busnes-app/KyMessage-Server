# KyMessages

[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/15925/badge)](https://www.bestpractices.dev/projects/15925)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/Busnes-app/KyMessage-Server/badge)](https://scorecard.dev/viewer/?uri=github.com/Busnes-app/KyMessage-Server)
[![OctoCounts](https://api.octocounts.com/badge/Busnes-app/KyMessage-Server/branch/master)](https://octocounts.com/github/Busnes-app/KyMessage-Server/tree/master)

**Start here:** [Standalone installation](INSTALL.md). The base container is an operator console, not a chat server or embedded chat client. Team chat additionally needs the Matrix stack (Synapse, MAS, PostgreSQL and Element), HTTPS and KyIdentity. Local administrator sign-in alone does not enable member chat.

Private team conversations, on infrastructure you control.

KyMessages uses Matrix for chat, with suite identity and sealed server backups. See the
[Matrix platform design](docs/superpowers/specs/2026-10-01-matrix-platform-design.md).
It has been installed on real hardware and passes its test suite. That is not a security
audit: chat encryption is not independently audited and Synapse does not enforce it, and call
encryption and production networking still need verifying on your own deployment. Read
[Matrix chat](#matrix-chat) before relying on it for private conversations. The source-built
application provides the operator console and, once the Matrix stack below is configured,
an "Open chat" link for members; without it they see a page saying chat is not available.

- [Product definition](docs/PRODUCT.md)
- [Protocol and interoperability research](docs/KYMESSAGES-PROTOCOL-RESEARCH.md)
- [Server restore runbook](docs/RESTORE.md)
- [Repository contracts](AGENTS.md)

## What it can do

- Manage the Matrix stack through the operator console.
- Use KyIdentity for member sign-in and Element for chat: **End-to-end encrypted in Element (not independently audited)**. Synapse does not enforce encryption; members should use Element. See [Matrix chat](docs/CHAT-SETUP.md#matrix-chat).
- Add agents, federation or voice/video calling through their separate setup paths.
- Keep acceptance gates and complete Matrix/media recovery requirements explicit.

## Documentation

- [Development](docs/DEVELOPMENT.md) — Build and inspect locally, Local container build, Verification. <a id="build-and-inspect-locally"></a><a id="local-container-build"></a><a id="verification"></a>
- [Running](docs/RUNNING.md) — Container image, Identity and recovery configuration, Container-local installer integration. <a id="container-image"></a><a id="identity-and-recovery-configuration"></a><a id="container-local-installer-integration"></a>
- [Chat setup](docs/CHAT-SETUP.md) — Matrix chat. <a id="matrix-chat"></a>
- [Calls](docs/CALLS.md) — Voice and video calls. <a id="voice-and-video-calls"></a>
- [Meeting notes](docs/MEETING-NOTES.md) — An invited bot that takes notes in calls, off by default.
- [Federation](docs/FEDERATION.md) — Federation. <a id="federation"></a>
- [Chat agents](docs/CHAT-AGENTS.md) — Agents. <a id="agents"></a>
- [Operator console](docs/OPERATOR-CONSOLE.md) — Operator console. <a id="operator-console"></a>
- [Kubernetes](docs/KUBERNETES.md) — Kubernetes (Ky cluster). <a id="kubernetes-ky-cluster"></a>
- [Verifying releases](docs/VERIFYING-RELEASES.md) — Check both release images' signatures and source commit, and rebuild the server binary to the same bytes.
- [Assurance case](docs/ASSURANCE-CASE.md) — Threat model, trust boundaries, and why the security requirements are met.
- [Key strength](docs/STRONG-CRYPTO.md) — Key-size rules, the `GODEBUG=fips140=on` TLS profile and what it does not cover.
- [Contributing](CONTRIBUTING.md) — How to report bugs, propose changes, the test policy and the coding standard.
