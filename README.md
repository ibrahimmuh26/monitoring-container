# Monitoring Container

Docker diagnostics, monitoring, and policy-controlled recovery with Telegram incident reports.

## Status

Design scaffold only. The agent, configuration parser, integrations, and recovery controller are not implemented yet. Nothing in this repository currently monitors or modifies Docker containers.

## Initial scope

- One explicitly allowlisted standalone Docker application per pilot.
- Generic example target: `example-api`.
- Optional application liveness and readiness probes.
- Confirm any Docker probe checks liveness before treating `unhealthy` as recovery evidence.
- Recheck deployment ownership, restart policy, and other recovery controllers after deployment changes.
- Validate behavior in a disposable environment before enabling production recovery.

## Architecture

One containerized Go agent per host:

1. Discover allowlisted Docker targets.
2. Consume Docker events and periodically reconcile state.
3. Confirm incidents, accounting for startup, deployments, and existing recovery.
4. Collect bounded diagnostics before any action.
5. Persist incidents and notification events in SQLite on a persistent volume.
6. Send sanitized reports to Telegram through a retryable outbox.
7. Recover only when explicitly enabled and supported by evidence.
8. Verify results and report recovery or escalation.

No UI, inbound management endpoint, or central database is required for the initial version. Future ticketing adapters consume persisted incident events. Service recovery must not automatically close a root-cause ticket.

## Configuration

`configs/pilot.example.yaml` documents a proposed schema, not an executable configuration. Recovery is disabled by default. Do not put Telegram tokens in tracked configuration; use mounted secret files.

An agent container cannot reach host services through its own `127.0.0.1`. Before deployment, validate a shared Docker network or another explicit host-routing method. Do not assume a host-local curl URL is usable from the agent.

## Safety

Docker socket access grants powerful host capabilities. A read-only socket mount does not make Docker API operations read-only. Do not expose Docker API publicly.

Readiness failure alone must not trigger restart. Without a meaningful application probe, report application health as unknown. Container running is not equivalent to application healthy.

Log evidence can contain personal information and credentials. Apply redaction and size limits before persistence and transmission; redaction cannot guarantee removal of every sensitive value. Prefer bounded summaries and controlled diagnostic retention.

See `docs/implementation-plan.md` for milestones and verification requirements.

## Contributing and security

Read [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md) before submitting changes or reports. Never submit deployment secrets or production diagnostics.

A license has not been selected yet. Public source visibility alone does not grant an open-source license.
