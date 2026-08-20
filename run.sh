#!/usr/bin/env bash
# One-command demo: Redis Stack → baseline KNN → live Go ingest → answer set flips.
set -euo pipefail
cd "$(dirname "$0")"

if ! docker info >/dev/null 2>&1; then
  if sg docker -c 'docker info' >/dev/null 2>&1; then
    exec sg docker -c "\"$0\" $*"
  fi
  echo "Docker is required (and your user must reach the docker socket)." >&2
  exit 1
fi

REDIS_NAME=activeloop-redis
QUERY_PORT=8080
REDIS_ADDR=127.0.0.1:6379
QUERY_PID=""
INGEST_PID=""

cleanup() {
  [[ -n "${QUERY_PID}" ]] && kill "${QUERY_PID}" 2>/dev/null || true
  [[ -n "${INGEST_PID}" ]] && kill "${INGEST_PID}" 2>/dev/null || true
  docker rm -f "${REDIS_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> Redis Stack (RediSearch vector module)"
docker rm -f "${REDIS_NAME}" >/dev/null 2>&1 || true
docker run -d --name "${REDIS_NAME}" -p 6379:6379 redis/redis-stack-server:7.2.0-v10 >/dev/null
for i in $(seq 1 40); do
  if docker exec "${REDIS_NAME}" redis-cli PING 2>/dev/null | grep -q PONG; then
    break
  fi
  sleep 0.5
done
docker exec "${REDIS_NAME}" redis-cli PING | grep -q PONG

echo "==> Build Go ingest + Python query venv"
go mod tidy
go build -o ingest .
if [[ ! -d .venv ]]; then
  python3 -m venv .venv
  .venv/bin/pip install -q -r query/requirements.txt
fi

# Seed feed with stale office notes; live rows appended after the baseline query.
mkdir -p feed
cp feed/stale.jsonl feed/live.jsonl

echo "==> Start query API + ingest tailer"
REDIS_ADDR="${REDIS_ADDR}" .venv/bin/python query/app.py &
QUERY_PID=$!
REDIS_ADDR="${REDIS_ADDR}" FEED_PATH=feed/live.jsonl ./ingest &
INGEST_PID=$!

for i in $(seq 1 40); do
  if curl -sf "http://127.0.0.1:${QUERY_PORT}/health" >/dev/null; then
    break
  fi
  sleep 0.25
done
# Let ingest index the stale lines.
sleep 1

QUERY='streaming multimodal training data for AI'
echo
echo "==> BASELINE search (stale index only)"
echo "    q: ${QUERY}"
curl -sS -X POST "http://127.0.0.1:${QUERY_PORT}/search" \
  -H 'content-type: application/json' \
  -d "{\"q\":\"${QUERY}\",\"k\":3}" | .venv/bin/python -m json.tool

echo
echo "==> Append fresh feed lines (simulates live data landing)"
cat feed/fresh.jsonl >> feed/live.jsonl
# Near-real-time: wait a few seconds, no restart / reindex job.
sleep 2

echo
echo "==> LIVE search (same query — answers should flip to fresh docs)"
curl -sS -X POST "http://127.0.0.1:${QUERY_PORT}/search" \
  -H 'content-type: application/json' \
  -d "{\"q\":\"${QUERY}\",\"k\":3}" | .venv/bin/python -m json.tool

echo
echo "==> Helm chart (deploy shape — not a one-off script)"
echo "    helm/vector-feed/Chart.yaml + templates/redis.yaml + templates/ingest.yaml"
sed -n '1,20p' helm/vector-feed/templates/redis.yaml

echo
echo "==> Demo complete. Index mutated in-place; query process never restarted."
