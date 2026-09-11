#!/usr/bin/env bash
# Creates only disposable local containers. Never selects existing applications.
set -euo pipefail

image=${IMAGE:-monitoring-container:local}
socket=${DOCKER_SOCKET_PATH:-/var/run/docker.sock}
workdir=$(mktemp -d)
fixture_id=""
recovery_fixture_id=""
recovery_agent_id=""
incident_agent_id=""
data_volume=""
recovery_volume=""
cleanup() {
  if [[ -n "$recovery_agent_id" ]]; then
    docker stop --time 5 "$recovery_agent_id" >/dev/null || true
  fi
  if [[ -n "$recovery_fixture_id" ]]; then
    docker rm --force "$recovery_fixture_id" >/dev/null || true
  fi
  if [[ -n "$recovery_volume" ]]; then
    docker volume rm "$recovery_volume" >/dev/null || true
  fi
  if [[ -n "$incident_agent_id" ]]; then
    docker stop --time 5 "$incident_agent_id" >/dev/null || true
  fi
  if [[ -n "$data_volume" ]]; then
    docker volume rm "$data_volume" >/dev/null || true
  fi
  if [[ -n "$fixture_id" ]]; then
    docker stop --time 2 "$fixture_id" >/dev/null || true
  fi
  rm -rf "$workdir"
}
trap cleanup EXIT

# mktemp directories are private by default; allow the non-root fixture to read
# only the generic, credential-free configuration mounted below.
chmod 755 "$workdir"
name="mc-smoke-$(basename "$workdir" | tr '[:upper:]' '[:lower:]')"
# Docker Desktop can map the host socket to a different numeric GID inside a
# Linux container, so inspect the mounted socket rather than host stat output.
socket_gid=$(docker run --rm -v "$socket:/var/run/docker.sock:ro" alpine:3.20 sh -c "stat -c '%g' /var/run/docker.sock")
printf 'server:\n  id: smoke\ntargets:\n  - name: fixture\n    selector:\n      container_name: %s\n    monitoring:\n      enabled: true\n' "$name" > "$workdir/config.yaml"
chmod 644 "$workdir/config.yaml"

# Fixture has no Docker socket. It merely stays alive, logging unavailable state.
fixture_id=$(docker run --detach --rm --name "$name" --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$workdir/config.yaml:/etc/monitoring-container/config.yaml:ro" "$image")

# Match the mounted socket group while keeping the agent's non-root UID, as in
# the production Compose example.
docker run --rm --user "65532:${socket_gid}" --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$socket:/var/run/docker.sock:ro" \
  -v "$workdir/config.yaml:/etc/monitoring-container/config.yaml:ro" \
  "$image" --once > "$workdir/output.json"

grep -Fq '"observation_status":"observed"' "$workdir/output.json"
grep -Fq "\"container_id\":\"$fixture_id\"" "$workdir/output.json"
grep -Fq '"state":"running"' "$workdir/output.json"
grep -Fq '"docker_health":"unknown"' "$workdir/output.json"
grep -Fq '"application_health":"unknown"' "$workdir/output.json"
printf 'PASS: read-only Docker inspection matched only the disposable fixture.\n'

# A separate non-root agent gets no Docker socket: inspection unavailability is
# intentional. Verify incident persistence in an isolated, newly created volume.
printf '\nincidents:\n  enabled: true\n  failure_threshold: 1\n' >> "$workdir/config.yaml"
data_volume=$(docker volume create)
incident_agent_id=$(docker run --detach --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$data_volume:/var/lib/monitoring-container" \
  -v "$workdir/config.yaml:/etc/monitoring-container/config.yaml:ro" "$image")
found=false
for attempt in {1..20}; do
  docker logs "$incident_agent_id" > "$workdir/incident.log" 2>&1
  if grep -Fq 'Incident opened:' "$workdir/incident.log"; then found=true; break; fi
  sleep 1
done
[[ "$found" == true ]]
docker stop --time 5 "$incident_agent_id" >/dev/null
incident_agent_id=""
# Read the resulting file as the image's non-root UID. No production volume is used.
docker run --rm --user 65532:65532 --read-only --cap-drop ALL \
  -v "$data_volume:/var/lib/monitoring-container:ro" \
  --entrypoint /bin/sh golang:1.26-alpine -c 'test -s /var/lib/monitoring-container/incidents.sqlite'
printf 'PASS: non-root incident agent persisted evidence metadata in its own volume.\n'

# Recovery smoke test: an allowlisted disposable container is stopped, observed,
# diagnosed, and restarted. No pre-existing container can match this random name.
recovery_name="mc-recovery-$(basename "$workdir" | tr '[:upper:]' '[:lower:]')"
printf 'server:\n  id: smoke\ninterval: 1s\nincidents:\n  enabled: true\n  failure_threshold: 1\n  success_threshold: 1\n  startup_grace: 0s\ntargets:\n  - name: fixture\n    selector:\n      container_name: %s\n    monitoring:\n      enabled: true\n    recovery:\n      enabled: true\n      timeout: 20s\n      cooldown: 1m\n      max_attempts_per_incident: 1\n      max_attempts_per_hour: 1\n' "$recovery_name" > "$workdir/recovery.yaml"
recovery_fixture_id=$(docker run --detach --name "$recovery_name" alpine:3.20 sleep 300)
recovery_volume=$(docker volume create)
recovery_agent_id=$(docker run --detach --rm --user "65532:${socket_gid}" --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$socket:/var/run/docker.sock:ro" \
  -v "$recovery_volume:/var/lib/monitoring-container" \
  -v "$workdir/recovery.yaml:/etc/monitoring-container/config.yaml:ro" "$image")
sleep 2
docker stop --time 1 "$recovery_fixture_id" >/dev/null
recovered=false
for attempt in {1..30}; do
  if [[ "$(docker inspect --format '{{.State.Running}}' "$recovery_fixture_id")" == "true" ]]; then recovered=true; break; fi
  sleep 1
done
[[ "$recovered" == true ]]
docker logs "$recovery_agent_id" > "$workdir/recovery.log" 2>&1
grep -Fq 'Incident opened:' "$workdir/recovery.log"
printf 'PASS: recovery agent restarted only its disposable exited fixture.\n'
