# Incidents, diagnostics, and Telegram

## Enable gradually

1. Copy `configs/incidents.example.yaml` to `configs/pilot.local.yaml`.
2. Set the server ID and exact allowlisted container name.
3. Leave Telegram and log collection disabled for the first observation run.
4. Run `--check-config` and `--once`, then start continuous mode with a persistent data volume.
5. Review application logs and add custom redaction patterns before enabling `diagnostics.collect_logs`.
6. Enable Telegram only after verifying the destination group and secret-file permissions.

The CLI validation and one-shot modes never collect evidence, write incidents, or send messages. Continuous mode is required. Changing configuration requires restarting the agent; it must keep the same data volume.

## Detection and resolution

Each poll updates persisted per-target failure/success counters. Defaults require three consecutive bad polls to open an incident, and three clear polls to close its observed condition. Docker's own unhealthy threshold has already run before our unhealthy polling threshold; these delays accumulate.

Detected conditions include unhealthy, exited, restarting, paused, missing targets, unsupported Swarm tasks, and Docker inspection unavailable. The latter is a monitoring failure, not proof that the application crashed. There is no HTTP probing, event-based crash-loop detection, dependency diagnosis, deployment detection, or automatic restart yet. An intentional stop/deployment can produce an alert: disable monitoring for that target during maintenance, retaining at least one enabled target or stopping the agent entirely.

Running containers within startup grace, or with a starting Docker healthcheck, are indeterminate: they neither open nor clear an incident. Different failure/identity fingerprints reset consecutive counters. Long observation gaps reset counters rather than interpreting old failures as consecutive. An already open incident stays open until clear evidence reaches the success threshold; a replacement container does not immediately close it. There is one open incident per target: subsequent condition changes appear in observation output, but do not yet create another notification or recollect evidence. The incident retains its original reason and opening snapshot; evolving-incident updates are a future enhancement.

A running container with no Docker healthcheck can clear a prior missing/exited condition, but its application health is still unknown. Notifications say **condition cleared**, not that every application function is healthy. A readiness-based Docker probe may generate an alert, but never a restart. Root-cause ticket resolution remains separate.

## Evidence

At incident opening, persist incident identity and the selected Docker snapshot first. Then collect a bounded, non-following log window by immutable container ID. Raw Docker error bodies and raw healthcheck output are not stored. This milestone does not collect CPU/RAM statistics or runtime dumps.

The log reader handles multiplexed stdout/stderr and TTY output, caps byte counts, and drops incomplete trailing lines when truncated. The request asks for the configured tail/window; if it still exceeds the byte limit, the captured prefix is retained, not necessarily the very last application line. Effective collection timeout is also limited by the Docker client's request timeout.

Logs are sanitized in memory before SQLite persistence. Built-in rules drop lines containing common authentication/secret indicators, credential-bearing URLs, Telegram-token-like strings, or JWT-like strings; emails, IPv4 addresses, and UUIDs receive placeholders. Additional regex rules drop matching lines. This cannot recognize arbitrary personal data, multiline secrets, or application-specific values. **Leave collection/transmission disabled if the data cannot be safely redacted.**

Evidence status is one of `disabled`, `collected`, `truncated`, `unavailable`, `no_container`, or `interrupted`. If a process dies during collection, startup marks that pending evidence interrupted instead of pretending later logs represent the original window. Log errors do not suppress incident reporting. The incident ID is printed to stderr; no collected log contents are printed by the agent.

## Telegram setup

Create a bot using Telegram BotFather and add it to the intended private group with permission to post. Obtain the numeric chat ID without exposing your bot token in a public URL, shell history, or issue.

Store the token at `secrets/telegram_bot_token` locally; this directory is ignored by Git and Docker builds. Use a private parent directory and ensure the non-root container UID can read the mounted file. Docker Compose file-backed secrets do not necessarily apply requested UID/mode remapping: verify permissions on your host. Do not commit the file or print its contents.

Configure locally:

```yaml
telegram:
  enabled: true
  token_file: /run/secrets/telegram_bot_token
  chat_id: "-1234567890123" # Example only; replace with your numeric chat ID.
  send_logs: false
  timeout: 10s
```

Run with the secret overlay:

```bash
export DOCKER_SOCKET_GID=$(stat -c '%g' /var/run/docker.sock)
docker compose -f compose.yaml -f compose.telegram.yaml build
docker compose -f compose.yaml -f compose.telegram.yaml run --rm --no-deps agent --check-config
docker compose -f compose.yaml -f compose.telegram.yaml up -d agent
```

The container image includes CA certificates and sends HTTPS requests to `api.telegram.org`. No inbound Telegram webhook or bot commands are provided. TLS verification stays enabled; redirect responses are not followed. Transport errors and Telegram response bodies are not printed, because request URLs include the bot token.

Summaries contain incident ID, server/target, detection time, reason, evidence status, and recovery action status. With recovery disabled they explicitly state no action was taken. With the constrained restart policy enabled, `requested` means Docker accepted a restart request; it does not prove post-restart application health. See [recovery.md](recovery.md). `telegram.send_logs: true` adds sanitized evidence as a JSON document on the opening event, with a summary caption. It requires `diagnostics.collect_logs: true`. Resolution events always use a summary, including the final observed Docker state and timestamp.

## Durable delivery

Opening and condition-cleared events have unique IDs in a transactional SQLite outbox. The opening event is queued only after evidence finalization. Resolution cannot overtake an undelivered opening event. A separate worker sends at most one event per four seconds, retries with backoff up to one hour, and honors bounded Telegram retry-after delays globally.

Delivery is **at-least-once**, not exactly-once: a crash after Telegram accepts a message but before SQLite records acknowledgement can duplicate it. Incident IDs make duplicates recognizable. Failed delivery stays queued across agent restarts. Telegram outages do not block monitoring; database errors stop the agent visibly rather than silently losing incident state.

When Telegram is disabled at incident opening, events are recorded as disabled and are not retroactively sent when Telegram is enabled. Queued events retain the original chat ID; changing chat configuration does not reroute old incidents. Changing the bot token may affect delivery permissions for those existing destinations.

## Storage and retention

Use one dedicated data directory per agent/server. SQLite and lock files use mode 0600; initialize the directory with restrictive permissions and do not share it with untrusted users. A lock prevents two incident-enabled processes from using the same directory, but separate volumes can still cause duplicate monitoring.

The database stores only incident snapshots, sanitized logs, target counters, and outbox metadata. Default retention is seven days after condition closure. Resolved incident deletion also deletes associated events, including still-undelivered notifications: retention takes precedence over indefinite queue retention. Active incidents are retained. Removed-target counters expire after retention if they have no active incident.

SQLite page count limits the main database (64 MiB by default). DELETE journaling can temporarily consume additional disk space up to roughly another database-size budget; this is not a hard directory quota. Auto-vacuum reclaims deleted pages. Provision disk headroom and external disk monitoring. If active incidents fill the database, writes fail and the agent exits rather than overwriting evidence. Do not remove the live database to silence the error.

The first incident schema is experimental. Back up and review data before changing versions; cross-version migrations are not guaranteed yet. There is no remote dashboard/export API. Authorized operators can inspect the SQLite file using a local SQLite client or a safe backup; it contains potentially sensitive diagnostics and must not be attached to public issues.
