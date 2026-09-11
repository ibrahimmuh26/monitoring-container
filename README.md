# Monitoring Container

A read-only Docker monitoring agent with persistent incidents, opt-in log diagnostics, and Telegram reports. Controlled recovery is planned next.

## Status: early development

The current implementation is **read-only monitoring with optional incident reporting**, not an auto-recovery system. Do not rely on it as your sole production monitor.

| Capability | Status |
| --- | --- |
| Strict YAML configuration and exact container allowlist | Implemented |
| Poll Docker state, health, restart count, and container identity | Implemented |
| JSON output on initial observation and changes | Implemented |
| One-shot inspection and offline config validation | Implemented |
| Docker image and Compose example | Implemented |
| Incident thresholds and startup grace | Implemented (poll-based) |
| Bounded Docker log diagnostics and best-effort redaction | Implemented, opt-in |
| SQLite incidents, retention, and persistent notification outbox | Implemented |
| Telegram summaries and opt-in JSON diagnostic attachments | Implemented |
| Docker events, HTTP probes, resource snapshots, crash-loop detection | Planned |
| Ticketing adapters | Planned |
| Automatic recovery and deployment coordination | Planned |
| Stable Compose selectors and Swarm service/task monitoring | Planned |

Unknown configuration keys and `recovery.enabled: true` are rejected. The earlier design-only configuration schema has been reduced to supported fields; do not use old scaffold examples.

## Requirements

- Go 1.24+ for local builds; CI uses Go 1.25 and 1.26. Prefer a supported Go release.
- Docker Engine 25+ (API 1.44+) via a local Unix socket.
- An existing standalone container selected by exact name.

This adapter intentionally uses a small standard-library HTTP client rather than the full Docker SDK. It exposes container inspect and bounded log reads only, disables redirects, limits response size, and does not use Docker environment variables or TCP endpoints. Logs are fetched using the immutable container ID captured by exact-name inspection, never by a potentially reused name.

## Quick start: local binary

```bash
make build
cp configs/pilot.example.yaml configs/pilot.local.yaml
# Edit server.id and selector.container_name in the local configuration.
./bin/monitoring-container --config configs/pilot.local.yaml --check-config
./bin/monitoring-container --config configs/pilot.local.yaml --once
./bin/monitoring-container --config configs/pilot.local.yaml
```

`--check-config` validates configuration syntax/constraints without contacting Docker, opening SQLite, or checking Telegram credentials. `--once` only inspects: it does not create incidents, collect logs, or send Telegram, even when those options are enabled. It returns 0 when every enabled target was inspected, 1 for incomplete inspection, and 2 for invalid configuration/arguments. A successfully inspected unhealthy or exited container still returns 0: inspect success is not application health success.

The continuous mode emits JSON to stdout and operational messages to stderr. Initial observations, container replacement, state changes, and inspection failures are emitted; unchanged observations are suppressed in memory. Restarting the agent emits initial observations again. Observation output itself is not persistent. With incident mode enabled, threshold counters and incidents survive restarts; no external heartbeat is implemented.

A Docker connection failure produces `observation_status: unavailable`, not a false application-down conclusion. Missing targets produce `not_found`; Swarm tasks produce `unsupported`. `application_health` remains `unknown`: a Docker probe cannot prove all application functions are healthy. `docker_health` preserves the actual Docker health status, and `probe_meaning` records your configured interpretation only.

## Run as a container on Linux

Prepare `configs/pilot.local.yaml` first. Ensure it is readable by the agent user; do not put secret values in this configuration. Start with the observation-only example or follow the [incident/Telegram setup](docs/incidents.md).

```bash
export DOCKER_SOCKET_GID=$(stat -c '%g' /var/run/docker.sock)
docker compose build
docker compose run --rm --no-deps agent --check-config
docker compose run --rm --no-deps agent --once
# Start continuous observation only after reviewing one-shot output.
docker compose up -d agent
docker compose logs -f agent
```

The image runs as a non-root user. Compose grants its group access to the socket using the host socket GID. Never make the Docker socket world-writable. On hosts using ACLs or security labeling, additional host-specific configuration may be required.

The example uses a read-only root filesystem, no capabilities, no published ports, bounded Docker log rotation, and no agent restart policy. The named data volume is writable and used only when incidents are enabled. The image initializes it for UID 65532. Mount the same volume when recreating the agent; never delete it as part of routine updates. The image has no shell and no built-in Docker healthcheck; process existence alone would not prove observation is progressing.

Docker Desktop uses a different host socket location in some setups. Configure `docker.socket` for native binary usage, or adapt the bind source for container usage.

## Configuration

See [`configs/pilot.example.yaml`](configs/pilot.example.yaml). Defaults: scan delay 30s and inspect timeout 3s. Targets require explicit `monitoring.enabled: true`; duplicates and wildcard/path selectors are rejected. The maximum is 100 configured targets.

Scans are sequential, with per-target deadlines and a delay after the completed scan. In incident mode, evidence collection is bounded and happens only when an incident first opens; a separate worker sends Telegram so network outages do not block scanning. They never overlap. A slow/unavailable daemon increases total scan time as targets grow; bounded parallel workers are planned. Docker health transitions and transient restarts between polls may be missed until event ingestion is implemented.

Container lookup is verified against the returned exact name, preventing Docker ID-prefix fallback from selecting a different container. Name reuse is observed as a replacement; no mutation follows it. Docker API response bodies, environment variables, raw healthcheck output, and daemon error bodies are not emitted.

`docker_health.meaning` accepts `unknown`, `liveness`, or `readiness`. Review the application's probe before setting this metadata. No value enables recovery. Readiness failure alone must never become an automatic restart rule.

## Safety and future architecture

Docker socket access can grant host-level control even though the current adapter only issues read requests. A read-only socket mount is **not** an API authorization boundary. Keep the image trusted and do not expose Docker API publicly.

Observation output still contains infrastructure metadata such as container names and image IDs; restrict access to logs. Diagnostic collection and transmission each require explicit opt-in. Redaction is best-effort, not a guarantee that arbitrary customer data is safe to transmit. Review application-specific redaction rules or leave log collection disabled.

The architecture is one agent per host, a persistent incident store/outbox, and Telegram delivery, with policy-controlled recovery and ticketing adapters planned next. A process lock prevents multiple incident-enabled agents from sharing one data directory; separate directories do not enforce host-wide ownership. Application recovery and root-cause ticket resolution remain separate. An agent cannot reliably report its own host being down; external monitoring is required.

An agent container cannot access host services via its own `127.0.0.1`. When HTTP probes are implemented, validate agent network reachability explicitly.

## Development

```bash
make fmt
make lint
make test
make build
make check-config
```

`make lint` runs `go vet` and a formatting check. `make test` runs unit/adapter tests with the race detector and coverage, using fake Docker responses and a disposable Unix socket; it does not touch a real Docker daemon. `make smoke-test` additionally checks the built image against a real local Docker daemon using a disposable fixture; it creates and cleans up only its own test container. Run it on a development daemon, not production. Real image checks are documented in [`docs/implementation-plan.md`](docs/implementation-plan.md).

Read [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md). Never submit production configuration, credentials, or diagnostics.

A license has not been selected yet. Public source visibility alone does not grant an open-source license.
