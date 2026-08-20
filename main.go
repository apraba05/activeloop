// Live JSONL → embed → Redis HASH with vector field.
// Tails a feed file; each new line becomes searchable without reindex or restart.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	indexName = "feed_idx"
	keyPrefix = "doc:"
	dim       = 64
)

type record struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func main() {
	redisAddr := env("REDIS_ADDR", "127.0.0.1:6379")
	feedPath := env("FEED_PATH", "feed/live.jsonl")

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		if err := rdb.Ping(ctx).Err(); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis: %v", err)
	}
	if err := ensureIndex(ctx, rdb); err != nil {
		log.Fatalf("index: %v", err)
	}

	log.Printf("tailing %s → redis %s (index %s)", feedPath, redisAddr, indexName)
	if err := follow(ctx, rdb, feedPath); err != nil {
		log.Fatal(err)
	}
}

func ensureIndex(ctx context.Context, rdb *redis.Client) error {
	err := rdb.Do(ctx, "FT.CREATE", indexName,
		"ON", "HASH",
		"PREFIX", "1", keyPrefix,
		"SCHEMA",
		"text", "TEXT",
		"embedding", "VECTOR", "FLAT", "6",
		"TYPE", "FLOAT32",
		"DIM", fmt.Sprintf("%d", dim),
		"DISTANCE_METRIC", "COSINE",
	).Err()
	if err != nil && !strings.Contains(err.Error(), "Index already exists") {
		return err
	}
	return nil
}

// follow reads existing lines then blocks on new ones (tail -f style).
func follow(ctx context.Context, rdb *redis.Client, path string) error {
	for {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return err
		}
		break
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var offset int64
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		for {
			n, readErr := f.Read(tmp)
			if n > 0 {
				offset += int64(n)
				buf = append(buf, tmp[:n]...)
				for {
					i := bytes.IndexByte(buf, '\n')
					if i < 0 {
						break
					}
					line := string(buf[:i])
					buf = buf[i+1:]
					if err := ingestLine(ctx, rdb, line); err != nil {
						log.Printf("ingest: %v", err)
					}
				}
			}
			if readErr != nil {
				break
			}
		}
		for {
			fi, err := os.Stat(path)
			if err != nil {
				return err
			}
			if fi.Size() > offset {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
}

func ingestLine(ctx context.Context, rdb *redis.Client, line string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var rec record
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return fmt.Errorf("json: %w", err)
	}
	if rec.ID == "" || rec.Text == "" {
		return fmt.Errorf("need id and text")
	}
	vec := embed(rec.Text)
	key := keyPrefix + rec.ID
	err := rdb.HSet(ctx, key, map[string]interface{}{
		"text":      rec.Text,
		"embedding": float32Bytes(vec),
	}).Err()
	if err != nil {
		return err
	}
	log.Printf("indexed %s (%d chars)", key, len(rec.Text))
	return nil
}

// embed is a deterministic bag-of-tokens vector (mock Bedrock).
// Must match query/app.py MockEmbedder exactly so KNN is meaningful offline.
func embed(text string) []float32 {
	v := make([]float32, dim)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		sum := sha256.Sum256([]byte(tok))
		for i := 0; i < dim; i++ {
			v[i] += float32(sum[i%32])/127.5 - 1.0
		}
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return v
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

func float32Bytes(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
