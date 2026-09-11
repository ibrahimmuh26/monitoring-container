#!/usr/bin/env bash
# Creates only disposable local containers. Never selects existing applications.
set -euo pipefail

image=${IMAGE:-monitoring-container:local}
socket=${DOCKER_SOCKET_PATH:-/var/run/docker.sock}
workdir=$(mktemp -d)
fixture_id=""
incident_agent_id=""
data_volume=""
cleanup() {
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
printf 'server:\n  id: smoke\ntargets:\n  - name: fixture\n    selector:\n      container_name: %s\n    monitoring:\n      enabled: true\n' "$name" > "$workdir/config.yaml"
chmod 644 "$workdir/config.yaml"

# Fixture has no Docker socket. It merely stays alive, logging unavailable state.
fixture_id=$(docker run --detach --rm --name "$name" --read-only --cap-drop ALL --security-opt no-new-privileges \
  -v "$workdir/config.yaml:/etc/monitoring-container/config.yaml:ro" "$image")

# Test-only root access avoids host-specific socket GID assumptions. The runtime
# image default and production Compose example remain non-root.
docker run --rm --user 0:0 --read-only --cap-drop ALL --security-opt no-new-privileges \
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
