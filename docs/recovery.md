# Constrained automatic restart

## Scope

This is an initial, deliberately narrow recovery policy. It can issue **one Docker restart request** only when all conditions hold:

1. The configured target is an exact-name, standalone Docker container.
2. Monitoring has opened an incident after the configured consecutive failure threshold.
3. The captured Docker state is `exited` or `dead`.
4. `recovery.enabled` is explicitly true for that target.
5. Persistent cooldown and hourly limits allow the action.
6. Immediately before the action, Docker re-inspection by the immutable 64-character container ID proves the same exact name/ID is still `exited` or `dead` and is not a Swarm task.

The action is `POST /v1.44/containers/{id}/restart?t=10`. It is requested after evidence is collected and sanitized, but before the opening Telegram event is queued. The report records `requested`, `not_needed`, `not_found`, `unsupported`, `failed`, or `interrupted`; `requested` means Docker accepted the request, **not** that the application is verified healthy.

## Explicit non-goals

This version never restarts:

- a `running` container, even if Docker marks it `unhealthy`;
- readiness failures, missing targets, Docker API outages, paused/restarting containers, or Swarm tasks;
- a replacement that reused the configured name after the original incident snapshot;
- an incident more than once.

There is no retry worker. `max_attempts_per_incident` must be exactly `1`. Docker event ingestion, HTTP liveness verification, deployment awareness, dependency suppression, rolling restart, and recovery of multiple replicas are future work.

## Risks and maintenance

An explicit `docker stop` creates an `exited` state. With recovery enabled, the agent can restart it after the failure threshold. **Before intentional maintenance, disable recovery for the target and recreate the agent, or stop the agent.** Re-enable only after maintenance is complete. Do not rely on Docker's restart policy and this agent as independent concurrent recovery controllers without testing their interaction.

The agent persists a reservation before calling Docker. If it crashes after reservation and before it records the response, the action is marked `interrupted` on next startup and is not repeated. This avoids duplicate action at the cost of manual investigation for ambiguous outcomes.

A `dead`/`exited` container can be a symptom of a shared database, disk, image, configuration, or host problem. Restart can mask the cause. Diagnostics and the incident remain for developer investigation. Keep recovery disabled for stateful databases and message brokers unless there is an application-specific, reviewed policy.

## Enable only after a disposable test

Start from `configs/recovery.example.yaml`. First use a disposable non-critical container on staging/development. Do not stop a production application merely to test this feature.

```yaml
recovery:
  enabled: true
  timeout: 20s
  cooldown: 5m
  max_attempts_per_incident: 1
  max_attempts_per_hour: 2
```

`timeout` bounds the agent's Docker restart request. Docker itself receives a 10-second stop grace request; applications with a longer graceful shutdown may need a different future policy rather than a larger agent timeout.

Run the local development smoke test before rollout:

```bash
docker build -t monitoring-container:local .
make smoke-test
```

It creates, stops, and verifies restart of its own random disposable fixture. It must never be run on production.

## Production rollout

1. Update the agent code/image while keeping `recovery.enabled: false`.
2. Verify continuous observation, incident storage, diagnostics, and Telegram for the selected target.
3. Review `docker inspect` ownership, healthcheck, restart policy, panel behavior, and maintenance procedure.
4. Enable recovery for one stateless target only.
5. Recreate the agent and verify its config. Do not force a production failure.
6. Watch incident/recovery reports and keep rollback ready.

Rollback is configuration-only:

```yaml
recovery:
  enabled: false
```

Then recreate or stop the agent. Do not delete the data volume: it contains the recovery reservation and incident evidence.
