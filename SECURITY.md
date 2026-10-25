# Security Policy

KyMessages is a self-hosted team chat server. People and groups come from KyIdentity; KyMessages scopes each conversation to its members.

## Reporting vulnerabilities

Report security vulnerabilities privately through GitHub Security Advisories:
<https://github.com/Busnes-app/KyMessage-Server/security/advisories>. Do not open a public issue.
Include the affected area, reproduction steps, impact, and affected versions.
We aim to acknowledge reports within two business days and will coordinate a fix
and disclosure.

## Trust boundaries and limitations

- The server stores messages and membership metadata; keep the data directory and backups private.
- Access is per-identity and per-conversation membership. Membership changes apply to future content; removed members retain content already delivered.
- KyIdentity owns people and groups; a deactivated identity must lose access.

## Deployment requirements

- Run behind HTTPS; never add a default that downgrades to cleartext.
- Keep the database and backups private; stop the server before copying the data directory.
- Configure rate limits for login and message-send endpoints.

## Security-sensitive changes

Changes to authentication, conversation membership and authorization, message storage, or logging require tests for both the expected path and the relevant
failure or attack path, plus a description of the trust boundary in the pull
request.

## Related documents

- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution and review requirements
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — community expectations
- [README.md](README.md) — what this project is and how to run it

