# Implementation Plan

## Scope

Build independently of Watchtower. Start with one explicitly allowlisted standalone Docker container (`example-api`) on a pilot host. Do not deploy until a matching disposable test environment has validated behavior. Add Docker Swarm support separately; never assume standalone recovery semantics apply to Swarm tasks.

## Current implementation

Foundation and initial polling are implemented: strict configuration, a read-only Unix-socket Docker adapter, exact-name validation, graceful shutdown, one-shot/config-check modes, JSON observations, tests, CI, and a container image.

The adapter deliberately uses the standard-library HTTP client with fixed API 1.44 (Docker Engine 25+) instead of introducing the full Docker SDK for inspect and bounded log reads. Review this choice when event ingestion or mutation APIs are added.

SQLite incident state, per-data-directory process locking, bounded opt-in log collection, conservative redaction, poll-based thresholds, startup grace, retention, a persistent outbox, Telegram summaries, and optional diagnostic attachments are implemented. See `docs/incidents.md` for setup and limits.

Host-wide ownership across separate directories, deployment coordination, HTTP probes, resource diagnostics, Docker events, and recovery remain unimplemented. Configurations attempting to enable recovery or unsupported integrations are rejected. This is not completion of all milestones below.

## Milestones

### 1. Agent foundation

- Establish a supported Go toolchain, module, build, lint, and test commands.
- Implement strict YAML validation; reject unknown fields and unsafe recovery settings.
- Introduce interfaces for Docker, probes, storage, clock, notifier, and recovery.
- Implement graceful shutdown and a single-agent ownership mechanism per host.
- Build a minimal non-interactive container image with persistent data and mounted secrets.

### 2. Observation and diagnostics

- Exact allowlist matching; reconcile container replacement without broadening scope.
- Consume Docker events with reconnect/reconciliation and bounded worker concurrency.
- Track Docker state and health separately from application liveness/readiness.
- Open deduplicated incidents with stable IDs and container/image identity snapshots.
- Collect bounded logs, inspect state, resource snapshots, and healthcheck output.
- Persist collection failures without blocking incident handling indefinitely.
- Redact sensitive material before storage and delivery; enforce retention and disk limits.

### 3. Telegram and persistence

- Store incidents, actions, retry counters, and notification outbox in SQLite.
- Deliver summaries and bounded diagnostic attachments.
- Handle retries, rate limits, outages, and duplicate delivery attempts.
- Telegram outages must not stop monitoring or permitted recovery.
- Notification delivery is at-least-once; use incident/event IDs to minimize duplicates.

### 4. Controlled recovery

- Disabled by default and configurable per target.
- Confirm recovery ownership; avoid conflict with Docker, autoheal, panels, or deployments.
- Respect explicit maintenance inhibition; do not assume deployments are always detectable.
- Acquire a per-target lock; enforce host concurrency, cooldown, per-incident and rolling limits.
- Persist action intent before execution; reconcile ambiguous outcomes after agent restarts.
- Recheck target identity and state immediately before action.
- Capture diagnostics first within a fixed deadline.
- Do not restart for readiness failure alone.
- Verify application liveness where available; otherwise report only observed Docker state.
- Escalate instead of repeating ineffective recovery indefinitely.

### 5. Pilot rollout

- Verify the configured Docker healthcheck behavior in source or image build artifacts.
- Validate network reachability from the agent container.
- Verify the current restart policy, labels, logging driver, and deployment owner.
- Run observation-only on the selected pilot application, with Telegram test messages clearly identified.
- Enable recovery only after explicit approval and test results.
- Rollback: disable recovery or stop the agent; do not change application deployment as part of rollback.

### 6. Expansion

- Add and independently test Swarm service/task support for staging.
- Add stable Compose selectors, multiple targets, and shared-dependency suppression rules.
- Add ticketing adapters with external ticket IDs and idempotency keys.
- Treat application recovery and root-cause ticket resolution as separate states.

## Verification requirements

Run `make fmt lint test build check-config`. Tests must not contact production infrastructure. Current tests cover configuration, read-only inspect, exact target matching, unavailable/missing/unsupported targets, response bounds, timeouts, deduplication, identity replacement, output failure, and shutdown. Additional tests cover persisted threshold counters, incident deduplication, interrupted evidence, log redaction, immutable-ID log reads, notification ordering, retries, retention, and process ownership. Telegram HTTP behavior is tested with fake transports, not real credentials. The remaining requirements below are acceptance criteria for subsequent milestones.

Image checks:

```bash
docker build -t monitoring-container:local .
docker run --rm --read-only -v "$PWD/configs/pilot.example.yaml:/etc/monitoring-container/config.yaml:ro" monitoring-container:local --check-config
```

These image checks do not mount the Docker socket or mutate any application.

For an actual Engine API smoke test, run `make smoke-test` after building the image on a development Docker daemon. The script creates its own disposable fixture, mounts the socket only into the observer, verifies exact container identity and unknown application health, then stops only its own fixture. It uses a root observer for socket portability in this test only; production Compose runs non-root. Do not run this smoke test on production.

Automated tests must cover:

- Configuration validation and allowlist isolation.
- Crash, restart loops, unhealthy probes, and HTTP timeouts.
- Readiness failure without recovery.
- Startup, maintenance, deployment uncertainty, and concurrent controllers.
- Container replacement between detection and action.
- Bounded diagnostics, missing logs, redaction, disk limits, and retention.
- Telegram failure, retry, and rate limiting.
- Agent restart during collection, notification, and recovery.
- Persistent cooldown/retry limits and duplicate-event handling.
- Successful and unsuccessful post-recovery verification.

Integration tests must use disposable containers. Never intentionally crash or hang the production application for testing.

## Limitations

An on-host agent cannot reliably report its own host being down. External heartbeat monitoring is a separate requirement. Logs alone do not reliably identify a hung process or prove root cause. Liveness success does not prove every application operation is functioning.
