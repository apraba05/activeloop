"""FastAPI KNN over the live Redis vector index.

Uses LangChain's Embeddings interface with the same deterministic mock as the Go ingest
(swap MockEmbedder for BedrockEmbeddings when AWS creds are present).
"""

from __future__ import annotations

import hashlib
import math
import os
import struct
from typing import List

import redis
import uvicorn
from fastapi import FastAPI
from langchain_core.embeddings import Embeddings
from pydantic import BaseModel

INDEX = "feed_idx"
DIM = 64
REDIS_ADDR = os.getenv("REDIS_ADDR", "127.0.0.1:6379")


class MockEmbedder(Embeddings):
    """Deterministic bag-of-tokens vectors — mirrors main.go embed()."""

    def embed_documents(self, texts: List[str]) -> List[List[float]]:
        return [self._one(t) for t in texts]

    def embed_query(self, text: str) -> List[float]:
        return self._one(text)

    def _one(self, text: str) -> List[float]:
        v = [0.0] * DIM
        for tok in text.lower().split():
            digest = hashlib.sha256(tok.encode()).digest()
            for i in range(DIM):
                v[i] += digest[i % 32] / 127.5 - 1.0
        norm = math.sqrt(sum(x * x for x in v))
        if norm == 0:
            return v
        return [x / norm for x in v]


def make_embedder() -> Embeddings:
    # Optional real Bedrock path (same interface; demo defaults to mock).
    if os.getenv("USE_BEDROCK") == "1":
        from langchain_aws import BedrockEmbeddings

        return BedrockEmbeddings(
            model_id=os.getenv("BEDROCK_MODEL", "amazon.titan-embed-text-v2:0"),
            region_name=os.getenv("AWS_REGION", "us-east-1"),
        )
    return MockEmbedder()


app = FastAPI(title="vector-feed-query")
embedder = make_embedder()
rdb = redis.Redis(
    host=REDIS_ADDR.split(":")[0],
    port=int(REDIS_ADDR.split(":")[1]),
    decode_responses=False,
)


class QueryIn(BaseModel):
    q: str
    k: int = 3


@app.get("/health")
def health():
    return {"ok": True}


@app.post("/search")
def search(body: QueryIn):
    vec = embedder.embed_query(body.q)
    blob = struct.pack(f"<{DIM}f", *vec)
    # RediSearch KNN on a live HASH index — no rebuild when ingest HSETs new docs.
    raw = rdb.execute_command(
        "FT.SEARCH",
        INDEX,
        f"*=>[KNN {body.k} @embedding $BLOB AS score]",
        "PARAMS",
        "2",
        "BLOB",
        blob,
        "SORTBY",
        "score",
        "RETURN",
        "2",
        "text",
        "score",
        "DIALECT",
        "2",
    )
    return {"query": body.q, "hits": parse_ft_search(raw)}


def parse_ft_search(raw) -> list:
    """redis-py returns [total, key, [field, val, ...], ...]"""
    if not raw or raw[0] == 0:
        return []
    hits = []
    i = 1
    while i < len(raw):
        key = raw[i]
        if isinstance(key, bytes):
            key = key.decode()
        fields = raw[i + 1]
        doc = {"id": key}
        for j in range(0, len(fields), 2):
            name = fields[j].decode() if isinstance(fields[j], bytes) else fields[j]
            val = fields[j + 1]
            if isinstance(val, bytes):
                val = val.decode()
            doc[name] = val
        hits.append(doc)
        i += 2
    return hits


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=int(os.getenv("PORT", "8080")))
