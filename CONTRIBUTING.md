# Contributing

This project is currently a read-only monitoring and incident-reporting agent, not a production-ready recovery system.

Before implementation, discuss substantial changes in an issue and consult `docs/implementation-plan.md`.

## Requirements

- Keep examples generic and recovery disabled by default.
- Never submit credentials, actual customer data, production logs, or private deployment configuration.
- Keep incident evidence separate from root-cause conclusions.
- Changes affecting recovery must include tests for target isolation, bounded retries, and failures.
- Use disposable containers for integration tests, never production applications.
- Document the actual commands run and their results in pull requests.

Run `make fmt lint test build check-config` before submitting changes. Tests use simulated Docker responses, a disposable Unix socket, temporary SQLite stores, and fake Telegram transports; they do not contact production Docker or send real Telegram messages. CI also builds the image and validates the example configuration inside it. Do not claim integration or recovery coverage from these checks alone.

The maintainer must select a license before presenting this project as licensed open-source software.
