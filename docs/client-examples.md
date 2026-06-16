# Client Examples

Redge speaks RESP2, so clients connect as if they were talking to Redis or Valkey.

The examples assume Redge is running on `127.0.0.1:6379`.

## Connection URLs

Use Redis TCP URLs for Redis clients. Do not use `https://...` URLs for Redis commands; HTTPS is only for Redge status endpoints such as `/health`, `/ready`, and `/version`.

For a Coolify deployment with a fixed `6379:6379` port mapping:

```text
redis://:PASSWORD@test1.conn.redgedb.com:6379/0
```

If Coolify assigns a dynamic public TCP port, replace `6379` with that public port:

```text
redis://:PASSWORD@test1.conn.redgedb.com:PUBLIC_PORT/0
```

When Redis TLS is enabled, use `rediss://`:

```text
rediss://:PASSWORD@test1.conn.redgedb.com:6379/0
```

When possible, prefer separate client config fields over one URL:

```text
host=test1.conn.redgedb.com
port=6379
password=PASSWORD
db=0
```

If the password is embedded in a URL and contains characters such as `@`, `:`, `/`, `#`, `?`, or `%`, URL-encode the password first. For example, `pa:ss@word` becomes `pa%3Ass%40word`.

## redis-cli

```powershell
redis-cli -h 127.0.0.1 -p 6379 ping
redis-cli -h 127.0.0.1 -p 6379 set user:1 "Ada" ex 60
redis-cli -h 127.0.0.1 -p 6379 get user:1
```

With auth:

```powershell
redis-cli -h 127.0.0.1 -p 6379 -a "$env:REDGE_PASSWORD" ping
```

With Redis TLS:

```powershell
redis-cli --tls -h test1.conn.redgedb.com -p 6379 -a "$env:REDGE_PASSWORD" ping
```

Sorted set example:

```powershell
redis-cli -p 6379 zadd rate:user:1 100 request-a 101 request-b
redis-cli -p 6379 zcard rate:user:1
redis-cli -p 6379 zrange rate:user:1 0 -1 withscores
redis-cli -p 6379 zremrangebyscore rate:user:1 -inf 100
```

## ioredis

Install:

```powershell
npm install ioredis
```

Basic usage:

```js
const Redis = require("ioredis");

const redis = new Redis({
  host: process.env.REDGE_HOST || "127.0.0.1",
  port: 6379,
  password: process.env.REDGE_PASSWORD || undefined,
  tls: process.env.REDGE_TLS === "true" ? {} : undefined,
  maxRetriesPerRequest: 2,
});

await redis.set("session:123", JSON.stringify({ userId: "u_123" }), "EX", 300);
const raw = await redis.get("session:123");
console.log(JSON.parse(raw));

await redis.quit();
```

Pipeline example:

```js
const key = "rate:user:123";
const now = Date.now();
const windowStart = now - 60_000;

const pipeline = redis.pipeline();
pipeline.zremrangebyscore(key, 0, windowStart);
pipeline.zcard(key);
pipeline.zadd(key, now, `request:${now}`);
pipeline.expire(key, 120);

const results = await pipeline.exec();
console.log(results);
```

Simple transaction example:

```js
const result = await redis
  .multi()
  .set("lock:job:1", "taken", "EX", 30, "NX")
  .get("lock:job:1")
  .exec();

console.log(result);
```

SCAN example:

```js
let cursor = "0";

do {
  const [nextCursor, keys] = await redis.scan(cursor, "MATCH", "session:*", "COUNT", 100);
  cursor = nextCursor;
  console.log(keys);
} while (cursor !== "0");
```

GUI clients such as Another Redis Desktop Manager may call `TYPE`, `STRLEN`, `MEMORY USAGE`, `DBSIZE`, and `INFO keyspace` when browsing keys. Redge implements these for strings and sorted sets so basic key browsing/editing works with the supported data types.

## go-redis

Install:

```powershell
go get github.com/redis/go-redis/v9
```

Basic usage:

```go
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	rdb := redis.NewClient(&redis.Options{
		Addr:     "127.0.0.1:6379",
		Password: "", // Set this to REDGE_PASSWORD in production.
		DB:       0,
		TLSConfig: func() *tls.Config {
			if os.Getenv("REDGE_TLS") == "true" {
				return &tls.Config{MinVersion: tls.VersionTLS12}
			}
			return nil
		}(),
	})
	defer rdb.Close()

	if err := rdb.Set(ctx, "hello", "world", 60*time.Second).Err(); err != nil {
		panic(err)
	}

	value, err := rdb.Get(ctx, "hello").Result()
	if err != nil {
		panic(err)
	}

	fmt.Println(value)
}
```

Pipeline example:

```go
key := "rate:user:123"
now := time.Now()

pipe := rdb.Pipeline()
pipe.ZRemRangeByScore(ctx, key, "0", fmt.Sprint(now.Add(-time.Minute).UnixMilli()))
pipe.ZCard(ctx, key)
pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixMilli()), Member: "request-1"})
pipe.Expire(ctx, key, 120*time.Second)

cmds, err := pipe.Exec(ctx)
if err != nil {
	panic(err)
}

fmt.Println(len(cmds))
```

Simple transaction example:

```go
tx := rdb.TxPipeline()
tx.Set(ctx, "lock:job:1", "taken", 30*time.Second)
tx.Get(ctx, "lock:job:1")

cmds, err := tx.Exec(ctx)
if err != nil {
	panic(err)
}

fmt.Println(cmds)
```

Sorted set example:

```go
_, err := rdb.ZAdd(ctx, "leaderboard", redis.Z{Score: 10, Member: "ada"}).Result()
if err != nil {
	panic(err)
}

members, err := rdb.ZRangeWithScores(ctx, "leaderboard", 0, -1).Result()
if err != nil {
	panic(err)
}

fmt.Println(members)
```

## Client Notes

- Use RESP2-compatible client behavior.
- Avoid unsupported Redis commands unless your client lets you feature-detect or provide fallbacks.
- For high-throughput D1 deployments, benchmark your exact pipeline patterns because the Redis protocol does not mark pipeline boundaries for server-side SQL batching.
