#!/usr/bin/env bash
# Blocks until midPoint's REST API authenticates the administrator, polling
# every 5 s for up to 10 minutes. On timeout it prints the server's recent
# logs and exits non-zero.
set -u

dir=$(cd "$(dirname "$0")" && pwd)
url=http://localhost:8080/midpoint/ws/rest/self
deadline=$((SECONDS + 600))

echo "waiting for $url ..."
until out=$(curl -fsS -o /dev/null -u administrator:Test5ecr3t "$url" 2>&1); do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "midPoint did not answer within 10 minutes; last error: $out" >&2
    docker compose -f "$dir/docker-compose.yml" logs --tail=50 midpoint_server >&2
    exit 1
  fi
  sleep 5
done
echo "midPoint is up after ${SECONDS}s"
