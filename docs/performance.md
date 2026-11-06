# Performance evidence and capacity plan

## What has and has not been measured

Functional tests, race detection, lint, security checks and four browser E2E
journeys have passed on this working tree. They do not establish sustainable
throughput, tail latency, load stability or production capacity.

The current configured bounds are 500 active links by default (maximum setting
700), 256 API requests in flight and creation rate 60/minute/client with burst
20. Compose specifies 0.5 CPU and 128 MiB. These are policy/resource limits,
not benchmark-derived capacity guarantees.

## Initial microbenchmark: 2026-10-29

Command actually executed, sequential campaign with five samples per benchmark:

```sh
mise exec -- go test -run '^$' -bench=. -benchmem -count=5 ./...
```

Environment: Go 1.27.3, Linux 6.12.113+deb13-amd64, amd64, Intel Xeon E5-2670
v2 @ 2.50GHz, 20 logical CPUs reported; benchmark suffix `-20`. Local host,
not the 0.5 CPU/128 MiB deployment container; no explicit CPU/RAM isolation or
background-load control. Normal Go test benchmark build (no race detector),
default adaptive Go benchmark duration, no separate warmup. Synthetic fixture
from `validPayload()`; no disk, HTTP or browser encryption.

Revision: base `147e09d1829f4f38d1cc46d182382fd29bd13544` plus uncommitted feature
work. Raw local-only output: `/tmp/opencode/share-secrets-perf/microbench-20261029.txt`;
temporary artifact, not a durable shared report. The summary below is retained.

| Benchmark | Median ns/op | Range ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: |
| SecretStoreCreate | 8,073 | 7,361–8,263 | 864 | 11 |
| SecretStoreConsume | 16,799 | 15,206–19,346 | 1,024 | 15 |
| SecretStoreConcurrentConsume | 1,360 | 1,326–1,470 | 1,024 | 15 |

All samples passed. Each iteration constructs a new empty store. Consume also
includes create + claim + ACK. The concurrent benchmark runs **independent
stores**, so it does not measure contention on a shared service store; its
ns/op is amortized parallel throughput, not individual request latency. Do not
convert these numbers into service RPS or a production ceiling.

## Repeatable HTTP campaign

Executed evidence and caveats: [`benchmarks/2026-10-29-baseline.md`](benchmarks/2026-10-29-baseline.md).
The executed 57 HTTP windows expose a major history-dependent difference:
fresh-store diagnostic steps pass at 200 cycles/s, but full-history windows
pass at 25 and fail latency already at 50 cycles/s under the same CPU budget.
Do not treat the former as long-lived service capacity. After this baseline,
the optimized memory implementation replaced the full-map cleanup with
indexed heaps; see `storage-implementation.md` for the before/after comparison.
Initial SQLite evidence is in [`benchmarks/2026-10-30-sqlite-baseline.md`](benchmarks/2026-10-30-sqlite-baseline.md);
on the local rotating disk it did not meet the diagnostic criteria at 10
cycles/s. In the pre-batching baseline, two complete 25 cycles/s full-history windows also failed; a third
was truncated by the harness deadline, and a separate corrected-deadline
repetition failed as well. The later group-commit/tmpfs candidate completed
all work in the 10/s short soak and 25/s full-history cases, but each still
failed the provisional p99 threshold. In an earlier short fresh-store staircase
2 cycles/s passed and 5 failed latency. Those numbers are not a VPS capacity
or a full-history limit.

The repository now contains `cmd/benchmark` (Go standard-library load generator)
and `scripts/benchmark.py` (Python standard-library process/container owner).
These are test tools, not production endpoints. No external load-tool dependency
is installed. Run from the checkout with Python 3, mise/Go and a verified
rootless Docker context named `rootless`; the local `debian:bookworm-slim` image
must already exist. The script does not pull images or install host packages.

```sh
make bench-http
# Explicit alternate local context (still requires a rootless daemon):
python3 scripts/benchmark.py --context rootless
# Optional separate native baseline, NOT directly comparable with limited mode:
python3 scripts/benchmark.py --mode native
# SQLite: fresh migrated local database per case, FULL durability and WAL cleanup:
python3 scripts/benchmark.py --backend sqlite --retained 0
```

The runner builds static release-style server/generator binaries, launches a
fresh owned server for each case/repetition, and binds only loopback. Limited
mode requests 0.5 CPU/128 MiB/no swap and verifies actual cgroup v2 `cpu.max` and
`memory.max`, refusing an unenforced limit. The generator runs outside that
cgroup; both still share host hardware. Native mode has no explicit CPU/RAM cap.
No production environment settings are sourced. Each server gets a synthetic
metrics token that is never written to reports or passed as a command argument.
In SQLite mode the runner creates a private disposable database directory under
the unique results directory, applies the pinned Goose migration with `-env
none`, binds it into the owned container and removes only that generated
directory when the case ends. SQLite runs as container UID 0 mapped to the
unprivileged rootless Docker user for this test-only writable bind; the normal
application image uses UID 65532. No actual host backup policy is verified by
the synthetic ACK. The container's disk I/O uses the host filesystem and is not
representative of a VPS disk; FULL synchronous and checkpoint work are included.

`--retained 0` is an explicitly partial campaign, not a full-history result.
SQLite seeding via thousands of durable HTTP journeys can take over an hour on
slow storage across three repetitions. The bounded setup deadline scales with
the number of retained records, separately from the bounded measurement
window; if it still fails, retain and report that
failure rather than seeding through direct SQL or claiming a complete run.

### Workload and isolation

- Three repetitions per case by default, sequentially, with unique servers and
  result directories. Do not run other benchmarks/builds/tests concurrently.
  Full-history follow-up windows intentionally keep the same retained store
  within a repetition to exercise the 10,000-record cap and eviction.
- Real AES-GCM fixtures with 90% 1 KiB, 9% 16 KiB and 1% 44 KiB synthetic
  plaintext; JSON/base64 wire bodies remain below 64 KiB. Fixtures are prepared
  outside the arrival window; decrypt/verify runs on the generator per claim.
  This measures server HTTP plus a Go client, not browser JS/WebCrypto latency.
- Cycles: create → active status → claim → decrypt/verify → ACK → used status →
  rejected second claim. Every tenth cycle also tries an invalid proof, releases
  the reservation and reclaims before valid ACK. No test-only API is used.
- Default staircase: 10/25/50/100/200/400 offered cycles/s for 10s each. These
  are independent fresh-server steps, not a single ever-growing staircase.
- Open-loop fixed arrivals, at most 128 cycles in flight, no waiting queue.
  Scheduler lateness beyond two arrival intervals and exhausted slots count as
  dropped work. This exposes delivery shortfall rather than slowing arrivals
  silently. Report generator CPU and scheduler lag as possible confounders.
- Two-second 5-cycle/s warmup is excluded from arrival measurements. Its small
  terminal metadata footprint deliberately remains until the next server reset.
- Additional cases: 450 active links, a 10s jump from warmup to the highest
  rate followed by a recovery cycle, a 120s soak at 10 cycles/s, production
  creation-rate policy (20 offered/s for 5s), capacity rejection at 500 active
  links, concurrency saturation with 270 partial-body connections followed by
  recovery, real 60s TTL expiry, and 9,500 retained records seeded through HTTP.
  Retained-store follow-ups offer 50/100/200 cycles/s on the same server after
  the initial 25/s window. `--only retained` runs that focused campaign;
  `--retained-rates` configures those additional rates. This subset is not a
  replacement for the other contract/load scenarios.
- Capacity/concurrency/expiry are contract probes, not throughput measurements;
  expiry intentionally takes over a minute. The `diagnosticHealthy` field is a
  latency/delivery screen for load windows, not the verdict on these probes.
  Check their failures, statuses, metrics and recovery separately.
- All policies except creation rate remain unchanged. Throughput-isolation
  cases raise creation rate/burst to 100,000 using supported configuration;
  the separate policy case keeps 60/minute/burst 20. Do not present isolated
  throughput as achievable from one client under production-default policy.

### Evidence and interpretation

Every run retains metadata, sanitized per-case JSON, warmup JSON, resource
samples, `summary.json` and `report.md` in a unique ignored directory under
`benchmark-results/`. A crash/interruption preserves completed cases; use only
finished evidence. Generated executables are removed on successful completion.
Teardown removes only the exact generated container/process, never other apps,
images, networks or volumes. No request/response bodies, URLs, IDs, payloads,
keys, tracking tokens or metrics tokens are included in reports.

Reports include offered/started/dropped/completed/rejected cycles, unexpected
contract/transport failures, status counts by endpoint, p50/p95/p99/max request
and cycle latency, scheduler lag, per-second cycle results, and before/after
aggregate service counters. Snapshots observe active/opening/tracked counts;
this is not continuous internal queue instrumentation.

Linux `/proc` and cgroup sampling at 200ms track server/generator CPU/RSS,
sampled cgroup memory, CPU throttling and OOM kills. Summaries select samples
within the measured window, excluding seeding. Very short probes may have no
samples; zero/default summaries are not evidence of zero resource use. RSS
and cgroup memory accounting differ; sampled peaks can miss short spikes.
Internal Go GC pauses/heap telemetry are not exposed by the application and
are not inferred from RSS. Allocation benchmarks/profiles supplement HTTP data.

Tool tests are part of `make all`: Go validates safe target/work budgets,
authenticated fixture round trips, percentile accounting, report redaction and
journeys against a freshly built real application; Python validates selection
of resource samples within the measurement window. Run
`make test-benchmark-tools` for the Python-only checks.

Provisional diagnostic criteria (not an operator-approved production SLO): at
least 99% of offered cycles complete, cycle p99 under 1s, no unexpected failures.
Reject-heavy policy/overload probes can succeed in protecting the service while
failing these delivery criteria. Report both. A 120s soak is bounded short-term
stability evidence, not a multi-hour endurance or leak-freedom claim.

The selected no-payload-backups policy is documented in
`storage-and-erasure.md`. The memory baseline uses in-memory reference removal;
the separate SQLite campaign includes WAL/erasure costs on the local test disk,
while PostgreSQL does not exist yet. Local host measurements exclude VPS architecture, TLS/proxy,
external disk behavior, OS swap/dumps and physical sanitization.

## Campaign completion checklist

Use the performance-optimization and performance-benchmarking-and-load-testing
skills. Agree intended offered load, payload mix, p95/p99, error budget and
allowed memory/cleanup backlog before identifying a healthy capacity step.

1. Add shared-store benchmarks at empty, typical, near-capacity and retained
   metadata loads; include contention and cleanup cost. Profile only after a
   reproducible bottleneck exists.
2. Drive the real HTTP lifecycle: create, claim, valid ACK, status, invalid
   proof, release/retry and expiry. Include representative small/large payloads
   within the encoded 64 KiB request limit and idle active-link occupancy.
3. Measure production-policy behavior separately from isolated service capacity:
   rate limits, capacity 503 and concurrency 503 are expected rejections, not
   successful secret operations. Never silently disable protections and claim
   those settings as deployable performance.
4. Run bounded smoke, constant-arrival staircase, burst and soak experiments on
   isolated local resources. Track offered/started/completed/dropped work,
   response status, p50/p95/p99, errors, CPU, RSS, GC/allocations, active count,
   reservations and cleanup backlog. Check generator saturation too.
5. Repeat each comparable run at least three times; retain raw results and a
   sanitized summary with revision, runtimes, hardware, limits, scenario,
   duration/warmup, input distribution and reset procedure. Keep generated bulk
   artifacts out of Git and never record payloads, URLs or tokens in reports.
6. Use the 0.5 CPU/128 MiB budget in one representative campaign, then validate
   VPS architecture/storage and TLS/proxy separately with explicit approval.
   Never target a live/shared VPS implicitly. Report host-only findings as such.
7. Compare in-memory baseline and SQLite under equal load, including WAL,
   durability, token hashing, deletion/checkpoint/key-destruction costs and
   blue/green overlap. Later repeat for PostgreSQL with real dependencies.

The diagnostic ceiling is the last step meeting the stated latency, delivery/error and
resource criteria. Report the first failing step and cause. A capped or
generator-limited experiment gives a bound, not an exact capacity. No ceiling
or production SLO should be inferred beyond an executed report's scope.
