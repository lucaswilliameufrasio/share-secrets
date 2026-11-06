# SQLite initial baseline — 2026-10-30

This is an experimental implementation, not a production sizing claim. The
initial run used `python3 scripts/benchmark.py --backend sqlite --retained 0`:
42 independent cases (14 × 3), with no full-history measurement. Raw results
are local and ignored at `benchmark-results/20261030T025348-f28e4e/`.

- Same host, generator hash, cgroup CPU/memory limits, payload mix, warmup,
  arrival model and diagnostic criteria as the in-memory baseline in
  `2026-10-29-baseline.md`. Server build SHA-256:
  `8d8deda0aac43d64cd432ed933444e2ab331a36ce8c4a0471d87b15738110d5f`.
- SQLite modernc, real Goose-migrated fresh file per case, WAL,
  `synchronous=FULL`, secure-delete, single connection per instance, bounded
  busy timeout, periodic expiry and TRUNCATE checkpoint. Real host ext4 and
  rootless container bind, not the VPS or its storage. Synthetic payloads only.
- Creation rate raised in throughput cases; separate default-policy case. The
  metrics endpoint is also queried before/after each case.

| Case | Completed/offered (3 runs) | Median run p99 | Diagnostic passes |
| --- | ---: | ---: | ---: |
| 10 cycles/s, 10s | 285/300 | 6,404 ms | 0/3 |
| 25 cycles/s, 10s | 219/750 | 10,438 ms | 0/3 |
| 50 cycles/s, 10s | 193/1,500 | 10,469 ms | 0/3 |
| 10 cycles/s, 120s soak | 2,279/3,600 | 11,370 ms | 0/3 |

The run exhibits claim/ACK/status 503s and client timeouts under load; these
are not merely generator drops and not successes. In one soak run, after load,
108 links remained active, 20 opening and `cleanupPending=1`. Capacity and
concurrency protection probes completed 3/3; 60s expiry behaved correctly in
3/3, notwithstanding the generic latency flag that does not apply to expiry.

Endpoint distributions show delays broadly across all operations, including
status and failed-second-claim. Server CPU is low compared to its 0.5-core
quota, so CPU saturation cannot explain this result. A single database/sql
connection and 100ms maintenance/checkpoint cadence are plausible queue/I/O
sources, not yet a measured attribution. `strace` and `perf` are unavailable;
no elevated privileges or production profiling endpoints were enabled. The
database contains ciphertext, so raw artifacts are not committed.

The optimized memory store at the same 10/s completed 300/300 with 10.74 ms
median p99; it does not provide equivalent persistence/durability. Do not
conflate the two backends, claim SQLite production readiness, or disable
durability/erasure to manufacture a better comparison.

## Candidate experiment

Hypothesis: a synchronous WAL TRUNCATE checkpoint every 100ms can occupy the
only connection and serialize user operations. Candidate keeps expiry polling
and durable purge-generation tracking unchanged, but limits checkpoint retry
to once every five seconds per process. `synchronous=FULL`, secure-delete,
checkpoint mode, write atomicity and pending-observability stay enabled.
Expected trade-off: longer visible pending-WAL interval (up to five seconds
plus busy/retry time), not a guarantee of physical sanitization. Compare the
same 10/s case, three repetitions, same local environment; follow with full
campaign if the candidate is beneficial. Result: 297/300 cycles completed,
0/3 diagnostic passes, median run p99 5,274 ms, versus 285/300 and 6,404 ms
before. Raw local-only evidence:
`benchmark-results/20261030T031433-d35498/`. This small improvement does not
meet the delivery/latency criteria and lengthens the WAL-cleanup window; the
candidate was reverted. Checkpoint cadence alone is not a sufficient fix.

The first group-commit candidate additionally returned `503` on approximately
one or two requests per 100-cycle run even when its bounded queue did not fill.
Stage/error counters identified extended SQLite code 6410
(`SQLITE_IOERR_GETTEMPPATH`): the read-only benchmark container lacked a
writable temporary directory, and the savepoint transaction needed one for a
large payload. This was not evidence of a disk fault. Compose and the benchmark
runner now mount a bounded 16-MiB noexec tmpfs at `/tmp`; SQLite's temp path is
set explicitly to that mount in container configurations. A rerun must verify
the fix, including the 44-KiB payload. `synchronous=FULL`, secure-delete and
the durable DB/WAL are unchanged. The changed temp filesystem is an explicit
environment difference from the failing group-commit candidate, not a pure
apples-to-apples latency comparison. tmpfs may still be swapped or snapshotted.

The corrected group-commit/tmpfs rerun is retained locally at
`benchmark-results/20261106T110410-f15045/`. Three 10-second repetitions per
rate: 5/s completed 150/150 with median run p99 894 ms (3/3 diagnostic passes),
10/s completed 300/300 with median p99 1,133 ms (1/3 passes), and 25/s completed
750/750 with median p99 1,955 ms (0/3 passes). All expected large-payload
journeys completed; no `GETTEMPPATH` 503 occurred. Compare with the earlier
non-batching baseline and the failed group-commit/no-tmpfs trial cautiously:
both code and temporary-filesystem configuration changed. p99 at these sample
counts is a nearest-rank maximum and short-window evidence, not an SLO.

### Complete group-commit campaign

`python3 scripts/benchmark.py --backend sqlite` completed three repetitions
per case after the bounded-shutdown changes. Raw local-only evidence:
`benchmark-results/20261106T111323-3aa05c/`. Server SHA-256
`1517e42e9279489618b9f6753179e3da36d778ae4ee56c9bec65ddbfd297b1d1`;
generator SHA-256
`3835261831b0d94106d8a7bb6dbf562b98dfbf1d8958462688dd8004874c965d`.
Configuration and workload match the campaign metadata; `/tmp` is now a
bounded tmpfs, a necessary environment change from the initial no-batch
baseline, which makes this an overall system comparison, not proof that every
improvement was caused only by batching.

| Case | Completed/offered across three runs | Median run p99 | Diagnostic passes |
| --- | ---: | ---: | ---: |
| Fresh store, 10/s × 10s | 300/300 | 1,000 ms | 1/3 |
| Fresh store, 25/s × 10s | 750/750 | 1,867 ms | 0/3 |
| 10/s × 120s soak | 3,600/3,600 | 1,435 ms | 0/3 |
| Initially 9,510 tracked, 25/s × 10s | 750/750 | 1,867 ms | 0/3 |
| Subsequently ~9,760–10,000 tracked, 50/s × 10s | 1,492/1,500 | 1,920 ms | 0/3 |
| Subsequently ~10,000 tracked, 100/s × 10s | 1,716/3,000 | 2,638 ms | 0/3 |

The 200/s full-history step was skipped after the 100/s window recorded
failures, as designed. At 10/s the fresh-store p99 values were 963, 1,000 and
1,225 ms; at 25/s with 9,510 tracked they were 1,767, 1,867 and 1,901 ms.
The 10/s soak had no unexpected contract/transport failures or dropped work,
but failed the provisional p99 <1s criterion in all three 120-second windows.
The capacity, API-concurrency and 60-second expiry functional probes each
completed 3/3. The generic diagnostic latency flag is not meaningful for an
intentional 61-second expiry probe or expected rate-limit rejections.

This candidate makes a large improvement to **completion under the tested
conditions**, including 750/750 full-history cycles at 25/s versus 96/250 and
63/250 in two complete initial windows. It still fails the latency criterion,
and 100/s loses substantial work under this offered load. These short tests are
not evidence of production capacity on the VPS. No `FULL` durability or secure
delete setting was disabled. Rate limits were raised only in the isolation
tests; normal per-client creation limits remain distinct.

A second candidate used a bounded separate read pool for ordinary status reads
while preserving transactional expiry checks. Same 10/s scenario: 275/300
completed, 0/3 passes, p99 median 6,473 ms. Raw local-only evidence:
`benchmark-results/20261030T031710-56e674/`. It did not improve this workload;
the additional pool was removed. Neither experiment identifies the underlying
I/O or scheduling cause. More profiling is required before tuning further.

The same 10/s HTTP case was also run natively, without a container CPU/memory
quota: `python3 scripts/benchmark.py --mode native --backend sqlite --only
step-10 --retained 0 --repeats 3`. It completed 282/300, median run p99
6,808 ms, 0/3 diagnostic passes. Raw local-only evidence:
`benchmark-results/20261030T034005-a18cfc/`. Because the quota and execution
environment differ this is not a controlled before/after throughput comparison;
it does show the slowdown is not exclusive to the rootless container.

## Full-history attempt is invalid

`python3 scripts/benchmark.py --backend sqlite --only retained` attempted three
repetitions with 9,500 real HTTP seed journeys. The preparation failed in all
three: `benchmark-results/20261030T032109-f3a8c3/retained-{1,2,3}.json`
contains `terminal seed failed`, offered=0. The harness incorrectly continued
to later rates on incompletely populated stores; all subsequent retained-50,
retained-100 and retained-200 numbers from that run are **invalid as full-
history evidence**. The harness was changed to skip subsequent steps after a
failed setup/window. This is an additional observed failure, not a successful
full-history capacity test; no synthetic SQL shortcut was used to bypass it.

### Retry with longer seeding budget

`python3 scripts/benchmark.py --backend sqlite --only retained` was repeated
with the longer seed budget. Raw local-only results:
`benchmark-results/20261030T171304-017a1e/`. Each run reached 9,510 tracked
records before its first 25 cycles/s window. The first two offered 250 cycles;
96 and 63 respectively completed, with unexpected endpoint failures and
timeouts. Their run-level p99 values were 11,114 ms and 11,073 ms. At least
one run also dropped 13 arrivals before starting them. These observations
demonstrate failure against the diagnostic criteria for those two windows; they
do not establish a sustainable rate.

The third window is **incomplete and not comparable**: only 172 of 250 planned
arrivals were offered and the final metrics request failed. Investigation found
that the original generator used one deadline for both the slow seed and the
measurement, allowing setup to consume the window budget. The harness now
gives setup and the load window independent bounded contexts and records an
explicit failure if arrivals are cancelled. Subsequent 50/100/200 windows were
correctly skipped after the failed 25/s windows. The generated report's 672
denominator and aggregate p99 combine a partial run and must not be treated as
three complete samples.

One final isolated repetition used the corrected separate deadlines:
`python3 scripts/benchmark.py --backend sqlite --only retained --repeats 1`.
At 9,510 tracked records it offered and started all 250 cycles at 25/s, completed
53, and had a 9,649 ms run p99; there were 197 contract/transport failures and
no dropped arrivals. Final metrics were available, with 86 active and 11 opening
records after the failed load. The next load steps were correctly skipped.
Raw local-only evidence: `benchmark-results/20261105T121035-6471ed/`.
The same server binary digest was used as the two complete earlier windows;
the generator digest changed because the deadline correction changed its code.
This corroborates failure under these conditions but does not make the three
runs a strictly identical campaign or establish a passing SQLite ceiling.

## Local runtime trace (separate microbenchmark)

After the HTTP campaigns, `BenchmarkSQLiteLifecycle` was run locally with a
Go runtime trace for 10s (`-benchtime=10s -trace ...`) and inspected with
`go tool trace -pprof=sync` and `-pprof=syscall`. It uses a real fresh Goose-
migrated SQLite file and shared store, concurrent create/status/claim/ACK, and
100ms maintenance. It does **not** reproduce the 0.5-CPU container HTTP mix;
trace includes benchmark setup/migration and is diagnostic rather than a
capacity result. Raw trace/profiles and the test binary remain local-only in
`/tmp/opencode/share-secrets-perf/`.

The synchronization profile shows aggregate goroutine waiting concentrated
at `database/sql.(*DB).conn` and `BeginTx`; summed goroutine wait times are
not wall-clock time. The syscall profile includes `pwrite`, `fcntl`, `fsync`,
`ftruncate` and migration subprocess waits. The trace does not establish how
much HTTP latency is caused by storage hardware versus Go scheduling, nor does
it prove that additional database connections would help. The two bounded
candidate experiments above failed; avoid weakening cleanup or durability to
claim a throughput improvement.

### Filesystem comparison: critical test boundary

The original microbenchmark and trace above used Go `b.TempDir()` with the
host default `/tmp`, which `findmnt` identifies as **tmpfs**. The HTTP SQLite
campaign stored its temporary database under `benchmark-results/` on ext4 on a
rotational local disk. Thus the original ~2.16 ms/op was **not a durable-disk
measurement** and must not be used to infer HTTP performance.

The same native `BenchmarkSQLiteLifecycle` was run with four synthetic payload
sizes, `-benchtime=3s -count=2`, first under `/tmp` (tmpfs), then with
`TMPDIR` under `benchmark-results/sqlite-bench-tmp` (ext4). Go 1.27.3,
20-way benchmark suffix, same host and `synchronous=FULL`, WAL, secure-delete
and 100ms cleanup/checkpoint. Representative ranges:

| Encoded input source | tmpfs ns/op | ext4 ns/op |
| --- | ---: | ---: |
| 16 bytes | 1,639,660–1,755,037 | 138,385,731–141,442,158 |
| 1 KiB | 1,785,593–1,829,235 | 111,869,765–136,301,647 |
| 16 KiB | 3,022,621–3,075,634 | 118,354,810–136,611,747 |
| 44 KiB | 3,500,569–3,828,901 | 121,414,558–124,309,349 |

Numbers are parallel amortized ns/op, not individual request latency; ext4
samples had only 26–33 iterations due to the short duration. They are not a
controlled hardware benchmark or estimate of another disk. They establish that
the test filesystem has a major effect on this workload; they do not prove a
particular syscall or claim any PostgreSQL/SSD result. Raw local-only output:
`/tmp/opencode/share-secrets-perf/sqlite-payload-bench.txt` and
`/tmp/opencode/share-secrets-perf/sqlite-payload-ext4.txt`.

`FULL` durability remains enabled; neither fsync nor deletion was removed.
Under the tested disk and criteria SQLite did not pass even the 10/s window.
The target VPS's actual filesystem, flush semantics, volume, host contention
and architecture remain untested. An operator must not interpret an SSD or VPS
performance estimate as verified merely from this local comparison.

## Lower-rate SQLite staircase

After the deadline fix, `python3 scripts/benchmark.py --backend sqlite
--retained 0 --rates 1,2,5 --only step-1,step-2,step-5 --repeats 3` ran on
the same locally described 0.5-CPU/128-MiB configuration. Each step used a
fresh Goose-migrated DB, 10-second arrival window and three repetitions; this
does **not** measure a full terminal history or a long soak. Raw local-only
evidence: `benchmark-results/20261105T175701-2be724/`; the server and generator
binary digests match the corrected-deadline retained repetition.

| Offered cycles/s | Completed/offered | Median run p99 | Diagnostic passes |
| --- | ---: | ---: | ---: |
| 1 | 30/30 | 256 ms | 3/3 |
| 2 | 60/60 | 302 ms | 3/3 |
| 5 | 150/150 | 1,267 ms | 0/3 |

At 5/s all journeys finished and no unexpected failures were recorded; each
run failed the provisional **p99 < 1s** criterion (run p99: 1,149–1,662 ms).
With just 10, 20 and 50 observations per run, nearest-rank p99 is the observed
maximum, so this is a short-window diagnostic, not a robust tail-latency SLO.
The tested fresh-store bracket is 2 passing to 5 failing offered cycles/s;
neither is an exact capacity nor a prediction for the VPS. At full history,
25/s failed in the windows described above; no low-rate full-history staircase
or multi-hour high-rate soak was performed.
