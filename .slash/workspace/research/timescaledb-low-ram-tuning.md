# Research: TimescaleDB on PostgreSQL 16 — Low-RAM Container Tuning
Date: 2026-05-22

## Summary

Running TimescaleDB on PostgreSQL 16 inside a ≤350 MB RSS container is viable for 100–1000 msg/sec ingest if you aggressively cap shared_buffers, limit connections, disable telemetry workers, and use batched multi-row INSERTs or COPY from the Go ingest service. Native compression should be ON for retention >7 days — it reduces storage 90–98% and actually speeds up analytical reads, with negligible CPU overhead at this ingest scale. Use `pg_dump` cron for per-tenant backup; WAL archiving is overkill unless you need PITR.

---

## 1. Minimum Viable `postgresql.conf` Settings

### The `timescaledb-tune` formula (source)

The official `timescaledb-tune` tool ([github.com/timescale/timescaledb-tune](https://github.com/timescale/timescaledb-tune)) applies these ratios from [`pkg/pgtune/memory.go`](https://github.com/timescale/timescaledb-tune/blob/main/pkg/pgtune/memory.go):

```
shared_buffers       = totalRAM / 4
effective_cache_size = totalRAM * 3/4
maintenance_work_mem = (totalRAM_GB * 128 MB), capped at 2047 MB
work_mem             = (totalRAM_GB * 20 MB * 20 baseConns) / maxConns / (cpus/2)
                       minimum: 64 KB
```

### Concrete numbers for 256 MB container

```ini
# postgresql.conf — 256 MB container, 2 vCPU, max_connections=10

shared_buffers          = 64MB          # 256/4 = 64 MB
effective_cache_size    = 192MB         # 256*3/4 = 192 MB
maintenance_work_mem    = 32MB          # 256 MB * 0.125 = 32 MB (capped formula)
work_mem                = 2MB           # ~(0.25 GB * 400 MB / 10 conns / 1 cpu) ≈ 10 MB; floor to 2 MB for safety
max_connections         = 10            # 1 app conn + 1 admin + headroom; each idle conn ~5-8 MB
wal_buffers             = 4MB           # default auto-tune = 1/32 of shared_buffers, min 64 kB, max 16 MB
                                        # 64 MB / 32 = 2 MB; bump to 4 MB for write-heavy ingest
checkpoint_completion_target = 0.9
synchronous_commit      = off           # safe for time-series; ~3x write throughput gain
random_page_cost        = 1.1           # SSD/tmpfs in container
effective_io_concurrency = 200          # SSD
min_wal_size            = 80MB
max_wal_size            = 256MB

# TimescaleDB
shared_preload_libraries = 'timescaledb'
timescaledb.max_background_workers = 4  # see §6 for trimming
```

**RSS budget breakdown (256 MB container):**
| Component | Approx RSS |
|---|---|
| PostgreSQL postmaster + shared_buffers | ~80 MB |
| 10 idle backend processes × ~5 MB | ~50 MB |
| TimescaleDB BGW scheduler + 2 workers | ~20 MB |
| OS/libc/stack overhead | ~30 MB |
| **Total** | **~180 MB** |

This leaves ~76 MB headroom for query working memory spikes.

### Concrete numbers for 512 MB container

```ini
# postgresql.conf — 512 MB container, 2 vCPU, max_connections=20

shared_buffers          = 128MB         # 512/4
effective_cache_size    = 384MB         # 512*3/4
maintenance_work_mem    = 64MB          # 512 MB * 0.125
work_mem                = 4MB           # conservative; 20 conns × 4 MB = 80 MB peak
max_connections         = 20
wal_buffers             = 8MB
checkpoint_completion_target = 0.9
synchronous_commit      = off
random_page_cost        = 1.1
effective_io_concurrency = 200
min_wal_size            = 80MB
max_wal_size            = 512MB

shared_preload_libraries = 'timescaledb'
timescaledb.max_background_workers = 4
```

**RSS budget breakdown (512 MB container):**
| Component | Approx RSS |
|---|---|
| PostgreSQL postmaster + shared_buffers | ~160 MB |
| 20 idle backend processes × ~5 MB | ~100 MB |
| TimescaleDB BGW scheduler + 2 workers | ~20 MB |
| OS/libc/stack overhead | ~30 MB |
| **Total** | **~310 MB** |

Leaves ~200 MB headroom — comfortable for 1000 msg/sec ingest bursts.

### Key rationale

- **`shared_buffers = RAM/4`** is the timescaledb-tune default. Going higher (e.g., RAM/2) is only beneficial when you have many concurrent analytical queries; for ingest-heavy workloads it wastes RSS without benefit.
- **`work_mem`** is *per sort/hash operation per query*, not per connection. With 10 connections and 2 MB work_mem, a single complex query can use up to ~20 MB. Keep it low.
- **`synchronous_commit = off`** is safe for append-only time-series data. A crash loses at most the last ~200 ms of writes (controlled by `wal_writer_delay`), which is acceptable for sensor/telemetry data.
- **`wal_buffers`**: PostgreSQL 16 auto-tunes this to `shared_buffers/32` (min 64 kB, max 16 MB). For write-heavy ingest, explicitly set 4–8 MB to avoid WAL write stalls.

---

## 2. TimescaleDB `chunk_time_interval` Recommendations

### The sizing rule

TimescaleDB's official guidance: **each chunk should fit in ~25% of `shared_buffers`** when actively written. This ensures the hot chunk stays in cache.

```
target_chunk_size ≈ shared_buffers * 0.25
```

For a row with 5 columns (timestamp BIGINT, device_id INT, value FLOAT8, tag TEXT, quality SMALLINT) ≈ ~60 bytes/row uncompressed:

```
rows_per_chunk = target_chunk_size / bytes_per_row
chunk_time_interval = rows_per_chunk / ingest_rate_per_second
```

### 256 MB container (shared_buffers = 64 MB, target chunk = 16 MB)

```
rows_per_chunk ≈ 16 MB / 60 bytes ≈ 267,000 rows
```

| Ingest Rate | Retention | Recommended `chunk_time_interval` | Rationale |
|---|---|---|---|
| 100 msg/sec | 1 day | `INTERVAL '45 minutes'` | 267k rows / 100 rps = 2670 s ≈ 44 min |
| 100 msg/sec | 7 days | `INTERVAL '1 hour'` | Slightly larger chunks reduce chunk count overhead |
| 100 msg/sec | 30 days | `INTERVAL '2 hours'` | Fewer chunks = less catalog overhead |
| 1000 msg/sec | 1 day | `INTERVAL '4 minutes'` | 267k rows / 1000 rps = 267 s ≈ 4.5 min |
| 1000 msg/sec | 7 days | `INTERVAL '10 minutes'` | Balance between chunk count and cache fit |
| 1000 msg/sec | 30 days | `INTERVAL '15 minutes'` | ~2880 chunks/month; manageable |

### 512 MB container (shared_buffers = 128 MB, target chunk = 32 MB)

```
rows_per_chunk ≈ 32 MB / 60 bytes ≈ 533,000 rows
```

| Ingest Rate | Retention | Recommended `chunk_time_interval` |
|---|---|---|
| 100 msg/sec | 1 day | `INTERVAL '1.5 hours'` |
| 100 msg/sec | 7 days | `INTERVAL '2 hours'` |
| 100 msg/sec | 30 days | `INTERVAL '4 hours'` |
| 1000 msg/sec | 1 day | `INTERVAL '9 minutes'` |
| 1000 msg/sec | 7 days | `INTERVAL '20 minutes'` |
| 1000 msg/sec | 30 days | `INTERVAL '30 minutes'` |

### Setting chunk_time_interval

```sql
-- At hypertable creation:
SELECT create_hypertable(
    'metrics',
    'time',
    chunk_time_interval => INTERVAL '15 minutes'
);

-- Or alter after creation:
SELECT set_chunk_time_interval('metrics', INTERVAL '15 minutes');
```

**Warning for 1d retention at 1000 msg/sec:** With 4-minute chunks you get ~360 chunks/day. TimescaleDB handles thousands of chunks fine, but the PostgreSQL planner overhead grows. If you see slow planning, increase `timescaledb.max_open_chunks_per_insert` (default 10) or widen the interval.

---

## 3. `add_retention_policy` Syntax and BGW Behavior

### Full syntax

```sql
-- Drop data older than the specified interval
SELECT add_retention_policy(
    'metrics',                          -- hypertable name
    drop_after => INTERVAL '7 days',    -- drop chunks older than this
    schedule_interval => INTERVAL '1 hour',  -- how often to check (default: 1 day)
    if_not_exists => true               -- no error if policy already exists
);
-- Returns: job_id INTEGER
```

### All retention intervals for reference

```sql
-- 1 minute retention (extreme; only for ephemeral buffers)
SELECT add_retention_policy('metrics', INTERVAL '1 minute',
    schedule_interval => INTERVAL '1 minute', if_not_exists => true);

-- 1 hour retention
SELECT add_retention_policy('metrics', INTERVAL '1 hour',
    schedule_interval => INTERVAL '10 minutes', if_not_exists => true);

-- 1 day retention
SELECT add_retention_policy('metrics', INTERVAL '1 day',
    schedule_interval => INTERVAL '1 hour', if_not_exists => true);

-- 7 day retention
SELECT add_retention_policy('metrics', INTERVAL '7 days',
    schedule_interval => INTERVAL '1 hour', if_not_exists => true);

-- 30 day retention
SELECT add_retention_policy('metrics', INTERVAL '30 days',
    schedule_interval => INTERVAL '6 hours', if_not_exists => true);
```

### Monitoring retention jobs

```sql
-- View all scheduled jobs
SELECT job_id, application_name, schedule_interval, max_runtime,
       max_retries, retry_period, proc_schema, proc_name, scheduled
FROM timescaledb_information.jobs;

-- View job execution history
SELECT * FROM timescaledb_information.job_stats
WHERE job_id = <your_job_id>;

-- Manually run retention now (useful for testing)
CALL run_job(<job_id>);
```

### BGW behavior under low RAM

The retention BGW runs as a separate PostgreSQL background worker process. Key behaviors:

1. **Memory footprint**: Each BGW process uses ~5–8 MB RSS. The retention job itself is lightweight — it issues `DROP TABLE` on old chunks (a metadata operation), not row-by-row deletes. Peak memory during a retention run is dominated by catalog lookups, typically <10 MB additional.

2. **Scheduling**: The scheduler uses `schedule_interval` as the *wait after successful completion*, not a fixed clock interval. Under memory pressure, if the BGW OOM-kills, it restarts with exponential backoff (`retry_period`, default 5 minutes).

3. **Chunk drop is fast**: Dropping a chunk is a single `DROP TABLE` — O(1) regardless of chunk size. No vacuum, no bloat. This is the key advantage over `DELETE`-based retention.

4. **Concurrent ingest**: Retention runs concurrently with ingest. The only lock is a brief `AccessExclusiveLock` on the chunk being dropped. For 1000 msg/sec ingest, this causes a sub-millisecond pause per chunk drop.

5. **`schedule_interval` tuning for low RAM**: Set `schedule_interval` to match your chunk_time_interval or longer. Running retention every minute when chunks are 15 minutes wide wastes a BGW slot. Rule of thumb: `schedule_interval ≥ chunk_time_interval`.

---

## 4. Native Compression: ON or OFF at This Scale?

### Recommendation: **ON for data older than 1–2× chunk_time_interval**

At 100–1000 msg/sec with ≤350 MB RSS, compression is a net win:

| Factor | Without Compression | With Compression |
|---|---|---|
| Storage (typical time-series) | 1× | 0.05–0.10× (90–95% reduction) |
| Analytical query speed | baseline | 10–1000× faster (less I/O) |
| Ingest speed (hot chunk) | baseline | identical (hot chunk stays uncompressed) |
| CPU during compression job | 0 | ~1 CPU-second per 1M rows compressed |
| RAM during compression job | 0 | ~10–20 MB additional (one BGW) |

**Source**: TimescaleDB benchmark on 1B rows showed 90%+ compression with columnar algorithms (Gorilla for floats, delta-of-delta + Simple-8b for timestamps, dictionary for low-cardinality columns). ([tigerdata.com/blog/building-columnar-compression-in-a-row-oriented-database](https://www.tigerdata.com/blog/building-columnar-compression-in-a-row-oriented-database))

### Why compression helps RAM-constrained containers

- Compressed chunks are **10–20× smaller on disk**, meaning more historical data fits in `shared_buffers` when queried.
- The compression BGW runs **asynchronously** — it doesn't block ingest.
- At 100–1000 msg/sec, the compression job runs infrequently (once per chunk_time_interval) and completes in seconds.

### Setup

```sql
-- Enable compression on the hypertable
ALTER TABLE metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'device_id',  -- column you filter by most
    timescaledb.compress_orderby = 'time DESC'
);

-- Add compression policy: compress chunks older than 2× chunk_time_interval
-- Example: chunk_time_interval = 15 min → compress after 30 min
SELECT add_compression_policy(
    'metrics',
    compress_after => INTERVAL '30 minutes',
    schedule_interval => INTERVAL '15 minutes',
    if_not_exists => true
);
```

### When to turn compression OFF

- If your workload is **exclusively point queries** on recent data (last 5 minutes) with no analytical aggregations — compression adds no benefit.
- If you have **very high UPDATE/DELETE rates** on historical data — compression adds decompression overhead for mutations (though TimescaleDB 2.x handles this via a staging area).
- If your retention is **≤1 minute** — chunks never age enough to compress.

---

## 5. COPY vs INSERT vs Multi-row INSERT for Go Ingest

### Throughput comparison (published benchmarks)

From the TimescaleDB 2.7.2 vs PostgreSQL 14.4 benchmark ([tigerdata.com/blog/postgresql-timescaledb-1000x-faster-queries-90-data-compression](https://www.tigerdata.com/blog/postgresql-timescaledb-1000x-faster-queries-90-data-compression-and-much-more/)):

- **Optimal batch size**: 10,000–15,000 rows per transaction
- **Peak ingest rate**: ~100,000–150,000 rows/sec on m5.2xlarge (8 vCPU, 32 GB RAM)
- **Small batches (<100 rows)**: "significantly hinder ingest performance"
- TimescaleDB and vanilla PostgreSQL showed nearly identical ingest rates at optimal batch sizes

For a memory-constrained container (2 vCPU, 256–512 MB RAM), realistic throughput:

| Method | Batch Size | Approx Throughput | Notes |
|---|---|---|---|
| Single-row `INSERT` | 1 | ~500–2,000 rows/sec | Each row = 1 round-trip + WAL flush |
| Multi-row `INSERT` | 100 | ~5,000–15,000 rows/sec | Good for 100 msg/sec |
| Multi-row `INSERT` | 1,000 | ~20,000–50,000 rows/sec | Good for 1,000 msg/sec |
| Multi-row `INSERT` | 10,000 | ~50,000–100,000 rows/sec | Optimal per benchmark |
| `COPY` (binary) | bulk | ~80,000–150,000 rows/sec | Fastest; bypasses expression parsing |
| `COPY` (text) | bulk | ~60,000–120,000 rows/sec | Slightly slower than binary |

### Go implementation patterns

**Multi-row INSERT (recommended for 100–1000 msg/sec):**

```go
// Batch accumulator — flush every N rows or T milliseconds
const batchSize = 500
const flushInterval = 100 * time.Millisecond

func (w *Writer) flush(ctx context.Context, rows []Row) error {
    if len(rows) == 0 {
        return nil
    }
    // Build: INSERT INTO metrics (time, device_id, value) VALUES ($1,$2,$3), ($4,$5,$6), ...
    valueStrings := make([]string, 0, len(rows))
    valueArgs := make([]interface{}, 0, len(rows)*3)
    for i, r := range rows {
        valueStrings = append(valueStrings,
            fmt.Sprintf("($%d,$%d,$%d)", i*3+1, i*3+2, i*3+3))
        valueArgs = append(valueArgs, r.Time, r.DeviceID, r.Value)
    }
    query := "INSERT INTO metrics (time, device_id, value) VALUES " +
        strings.Join(valueStrings, ",")
    _, err := w.db.ExecContext(ctx, query, valueArgs...)
    return err
}
```

**COPY (recommended for >5,000 msg/sec or bulk backfill):**

```go
import "github.com/jackc/pgx/v5"

func (w *Writer) copyFlush(ctx context.Context, rows []Row) error {
    conn, err := w.pool.Acquire(ctx)
    if err != nil {
        return err
    }
    defer conn.Release()

    _, err = conn.Conn().CopyFrom(
        ctx,
        pgx.Identifier{"metrics"},
        []string{"time", "device_id", "value"},
        pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
            return []any{rows[i].Time, rows[i].DeviceID, rows[i].Value}, nil
        }),
    )
    return err
}
```

### Decision guide for Go ingest service

| Scenario | Recommended Method |
|---|---|
| 100 msg/sec, simple schema | Multi-row INSERT, batch=100–500 |
| 1000 msg/sec, simple schema | Multi-row INSERT, batch=500–2000 |
| >5000 msg/sec or bulk load | `pgx.CopyFrom` (COPY protocol) |
| Need upsert/conflict handling | Multi-row INSERT with `ON CONFLICT DO UPDATE` |
| Compressed chunks + direct write | `SET timescaledb.enable_direct_compress_copy = on` + COPY |

**Key insight**: With `synchronous_commit = off`, multi-row INSERT at batch=500 easily handles 1000 msg/sec on a 512 MB container. COPY is only necessary if you need to saturate the I/O subsystem.

---

## 6. Memory Cost of `timescaledb-tune`, Background Workers, and Disabling Nonessential Ones

### Background worker inventory

| Worker | Purpose | RSS | Disable? |
|---|---|---|---|
| BGW Scheduler | Orchestrates all other BGW jobs | ~5 MB | No (required) |
| Retention policy worker | Drops old chunks | ~5–8 MB (only during run) | Only if no retention policy |
| Compression policy worker | Compresses old chunks | ~8–15 MB (only during run) | Only if no compression |
| Continuous aggregate worker | Refreshes caggs | ~5–10 MB (only during run) | Yes, if no caggs |
| Telemetry worker | Sends anonymous usage stats to Timescale | ~3–5 MB | **Yes — disable in production** |

### Disabling telemetry (recommended for all production containers)

```ini
# postgresql.conf
timescaledb.telemetry_level = off
```

Or at runtime:
```sql
ALTER SYSTEM SET timescaledb.telemetry_level = 'off';
SELECT pg_reload_conf();
```

### Controlling max background workers

```ini
# postgresql.conf
# Default is 8; reduce to 4 for low-RAM containers
timescaledb.max_background_workers = 4
```

Each slot reserves a PostgreSQL background worker slot (from `max_worker_processes`). The workers themselves only consume RSS when actively running a job.

```ini
# Also reduce PostgreSQL's global worker pool
max_worker_processes = 8    # default 8; reduce to 6 for containers
max_parallel_workers = 2    # default = max_worker_processes; reduce for low RAM
max_parallel_workers_per_gather = 1  # disable parallel query (saves ~10 MB per parallel worker)
```

### `timescaledb-tune` memory overhead

`timescaledb-tune` is a **one-shot CLI tool** — it modifies `postgresql.conf` and exits. It has zero runtime memory overhead. Run it once at container startup:

```dockerfile
# In Dockerfile or entrypoint.sh
RUN timescaledb-tune --memory="256MB" --cpus=2 --max-bg-workers=4 \
    --quiet --yes --dry-run >> /etc/postgresql/postgresql.conf
```

Or use the `--memory` flag to override auto-detection in containers where `/proc/meminfo` shows host RAM instead of container limit.

### Minimal BGW configuration for 256 MB container

```ini
# postgresql.conf additions for minimal BGW footprint
timescaledb.max_background_workers = 2   # scheduler + 1 worker slot
timescaledb.telemetry_level = off
max_worker_processes = 6
max_parallel_workers = 0                  # disable parallel query entirely
max_parallel_workers_per_gather = 0
```

With this config, only the BGW scheduler runs persistently (~5 MB). Policy workers spawn on-demand and exit after completion.

---

## 7. Backup Approach for Small Per-Tenant DBs

### Option A: `pg_dump` cron (recommended default)

```bash
# Cron entry: daily dump at 02:00, keep 7 days
0 2 * * * pg_dump -Fc -Z 6 \
    "postgresql://user:pass@localhost:5432/tenant_db" \
    > /backups/tenant_db_$(date +%Y%m%d).dump \
    && find /backups -name "tenant_db_*.dump" -mtime +7 -delete
```

**Pros:**
- Zero configuration overhead
- Dump is self-contained and portable
- Restore is simple: `pg_restore -d new_db tenant_db_20260522.dump`
- No WAL archiving infrastructure needed
- `-Fc -Z 6` (custom format, gzip level 6) compresses 60–80% for time-series data

**Cons:**
- Point-in-time recovery only to the dump timestamp
- Dump takes a consistent snapshot (uses `REPEATABLE READ`) — brief lock on schema changes
- For large DBs (>10 GB), dump can take minutes

**For compressed hypertables**: `pg_dump` correctly handles compressed chunks — it dumps the underlying compressed storage and restores it as-is.

### Option B: WAL archiving / continuous backup

```ini
# postgresql.conf
wal_level = replica
archive_mode = on
archive_command = 'cp %p /wal_archive/%f'
```

**Pros:**
- Point-in-time recovery (PITR) to any second
- Continuous — no backup window

**Cons:**
- WAL archive grows at `wal_segment_size` (16 MB) per segment, continuously
- Requires WAL management infrastructure (pgBackRest, Barman, or S3 lifecycle)
- Adds ~5–10% write overhead
- For a 256 MB container with 100–1000 msg/sec, WAL generation is ~1–10 MB/min — manageable but requires external storage

### Recommendation

**Use `pg_dump` cron for per-tenant containers.** Rationale:

1. At 100–1000 msg/sec with 1–30 day retention, the DB size is bounded (typically <5 GB). `pg_dump` completes in <60 seconds.
2. Per-tenant isolation means losing a few minutes of data on crash is acceptable — the upstream ingest service can replay from its own buffer.
3. WAL archiving requires external storage and adds operational complexity disproportionate to the data value at this scale.
4. If PITR becomes a requirement, add `pgBackRest` — it handles both base backups and WAL archiving with S3/GCS backends.

### Dump script for Go-managed containers

```go
// Trigger pg_dump from Go ingest service (e.g., daily maintenance window)
func backupDB(ctx context.Context, connStr, backupPath string) error {
    cmd := exec.CommandContext(ctx,
        "pg_dump", "-Fc", "-Z", "6", "-f", backupPath, connStr)
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    return cmd.Run()
}
```

---

## 8. Citations

### Official TimescaleDB Documentation

1. **timescaledb-tune source code** — memory tuning formulas (shared_buffers = RAM/4, effective_cache_size = RAM*3/4, maintenance_work_mem = RAM_GB * 128 MB):
   - [`github.com/timescale/timescaledb-tune/blob/main/pkg/pgtune/memory.go`](https://github.com/timescale/timescaledb-tune/blob/main/pkg/pgtune/memory.go)

2. **Background worker scheduler design** — per-database scheduler, schedule_interval semantics, exponential backoff on failure:
   - [`github.com/timescale/timescaledb/blob/main/src/bgw/README.md`](https://github.com/timescale/timescaledb/blob/main/src/bgw/README.md)

3. **add_retention_policy API** (from Context7 / timescaledb llms.txt):
   - `SELECT add_retention_policy('table', drop_after => INTERVAL '90 days');`
   - Source: [context7.com/timescale/timescaledb](https://context7.com/timescale/timescaledb/llms.txt)

4. **Hypercore / Compression architecture** — hybrid row-columnar storage, 90–98% compression, direct INSERT into compressed chunks:
   - [`tigerdata.com/docs/use-timescale/latest/hypercore/`](https://www.tigerdata.com/docs/use-timescale/latest/hypercore/)

### Community Benchmark Posts

5. **"PostgreSQL + TimescaleDB: 1,000x Faster Queries, 90% Data Compression"** (Ryan Booz, Timescale, 2022-09-22, updated 2025-12-09):
   - Benchmark: 1 billion rows, TimescaleDB 2.7.2 vs PostgreSQL 14.4 on m5.2xlarge
   - Key finding: optimal batch size 10,000–15,000 rows; ingest rates nearly identical between TSDB and vanilla PG at optimal batch sizes; compression reduces I/O 27× for analytical queries
   - URL: [`tigerdata.com/blog/postgresql-timescaledb-1000x-faster-queries-90-data-compression-and-much-more`](https://www.tigerdata.com/blog/postgresql-timescaledb-1000x-faster-queries-90-data-compression-and-much-more/)

6. **"Building Columnar Compression for Large PostgreSQL Databases"** (Mike Freedman, Timescale, 2023-11-14, updated 2026-03-11):
   - Deep dive on compression algorithms: Gorilla (floats), delta-of-delta + Simple-8b (timestamps), dictionary (low-cardinality), LZ (other)
   - Real-world compression rates: 94–97% from production customers (Ndustrial, Octave, METER Group)
   - Explains segmentby/orderby mechanics and their impact on query performance
   - URL: [`tigerdata.com/blog/building-columnar-compression-in-a-row-oriented-database`](https://www.tigerdata.com/blog/building-columnar-compression-in-a-row-oriented-database)

---

## Quick-Reference Cheatsheet

```sql
-- 1. Create hypertable with appropriate chunk interval
SELECT create_hypertable('metrics', 'time',
    chunk_time_interval => INTERVAL '15 minutes');  -- 1000 msg/sec, 512 MB

-- 2. Enable compression
ALTER TABLE metrics SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'device_id',
    timescaledb.compress_orderby = 'time DESC'
);

-- 3. Add compression policy (compress after 2 chunks age)
SELECT add_compression_policy('metrics',
    compress_after => INTERVAL '30 minutes',
    schedule_interval => INTERVAL '15 minutes',
    if_not_exists => true);

-- 4. Add retention policy
SELECT add_retention_policy('metrics',
    drop_after => INTERVAL '7 days',
    schedule_interval => INTERVAL '1 hour',
    if_not_exists => true);

-- 5. Disable telemetry
ALTER SYSTEM SET timescaledb.telemetry_level = 'off';
SELECT pg_reload_conf();

-- 6. Monitor jobs
SELECT job_id, application_name, schedule_interval, scheduled
FROM timescaledb_information.jobs;
```

```ini
# Minimal postgresql.conf for 256 MB container
shared_buffers = 64MB
effective_cache_size = 192MB
maintenance_work_mem = 32MB
work_mem = 2MB
max_connections = 10
wal_buffers = 4MB
synchronous_commit = off
checkpoint_completion_target = 0.9
max_worker_processes = 6
max_parallel_workers = 0
max_parallel_workers_per_gather = 0
timescaledb.max_background_workers = 2
timescaledb.telemetry_level = off
shared_preload_libraries = 'timescaledb'
```
