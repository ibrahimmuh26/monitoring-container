# Security

## Development status

No supported production release exists yet. The agent reads Docker state and optionally collects logs, persists incidents, sends Telegram reports, and—only with an explicit narrow per-target policy—can restart an exited/dead standalone container. It is not a supported production release. Log collection and transmission are separately opt-in; redaction cannot guarantee that arbitrary application data is safe to transmit.

## Reporting vulnerabilities

Do not post exploit details, tokens, or production diagnostics in public issues.

If GitHub private vulnerability reporting is enabled, use the repository Security tab to report privately. If it is unavailable, open an issue containing only a request for a private reporting channel, without sensitive details. The maintainer still needs to configure a private reporting channel before the first release.

## Operational boundaries

- Docker socket access can grant host-level control. A read-only socket mount does not restrict Docker API methods.
- Never expose an unauthenticated Docker API to the internet.
- Keep tokens and actual deployment configuration outside version control and build contexts.
- Ignore rules are defense in depth, not a secret scanner. Review staged files and Git history before publishing.
- Diagnostics may contain credentials and personal information; apply redaction, access restrictions, and retention limits.
- Readiness failures alone must not trigger restart.
- Recovery requires explicit target authorization, persistent attempt limits, audit evidence, and a documented maintenance inhibition procedure.
- A read-only Docker socket mount does not prevent API mutations. Enabling recovery materially changes the agent's host-control capability.
