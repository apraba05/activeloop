# Live-Updating Vector Feed (Continual Ingestion → Retrieval Loop)

Streaming ingestion, embedding, and low-latency retrieval over a mutable vector index.

**Live demo:** https://activeloop.ashanpraba.com

The demo runs entirely in the browser against seeded data — no API keys,
no accounts, and no external services required.

## Stack

- Go
- Python
- Redis (RediSearch)
- LangChain
- AWS Bedrock (or mock embedding fn)
- Helm
- Kubernetes (kind)

## How it works

- Docker-compose or kind: spin up Redis Stack (RediSearch module) + a minimal Helm chart wrapping it.
- Go service: tail a JSONL file simulating a live feed, embed each new line (Bedrock call or deterministic mock embedding), HSET vector+metadata into Redis.
- Python/FastAPI endpoint: run KNN similarity query against the Redis index, return top matches.
- Run a query against the empty/stale index first to show a baseline answer.
- Start the Go tailer live on camera, append new lines to the feed file, re-run the same query to show the answer set change.
- Show the Helm chart / k8s manifest briefly to signal this is meant to run as a deployed service, not a script.

## Running locally

```bash
cd src
bash run.sh
```

Then open the printed URL. A prebuilt static version of the UI lives in
`src/web/` and can be opened directly with no server.
