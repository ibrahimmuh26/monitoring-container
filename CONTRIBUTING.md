# Contributing

This project is currently a design scaffold, not a deployable monitoring agent.

Before implementation, discuss substantial changes in an issue and consult `docs/implementation-plan.md`.

## Requirements

- Keep examples generic and recovery disabled by default.
- Never submit credentials, actual customer data, production logs, or private deployment configuration.
- Keep incident evidence separate from root-cause conclusions.
- Changes affecting recovery must include tests for target isolation, bounded retries, and failures.
- Use disposable containers for integration tests, never production applications.
- Document the actual commands run and their results in pull requests.

No application build, test, or lint commands exist yet. Establish these alongside the first Go implementation rather than claiming the scaffold has runtime verification.

The maintainer must select a license before presenting this project as licensed open-source software.
