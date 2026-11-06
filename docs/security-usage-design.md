# Security, usage tracking and online PWA

**Status:** Implemented for default memory backend; SQLite additions under validation
**Date:** 2026-10-29

## Context

The app shares browser-encrypted, one-time links without user accounts. The
default server store is volatile and intended for one process. The feature
needs shorter per-link expiry, creator-side status tracking, usage telemetry,
and overload protection without retaining plaintext or making the decryption
key available to the server.

## Decisions

- Each link can override the server default with a validity from 1 minute to 24
  hours. Invalid durations are rejected by the API; the browser default follows
  the server's configured `SECRET_TTL`.
- Claiming a one-time secret is a `POST`, not a `GET`; safe/prefetching HTTP
  methods cannot reserve a link.
- Link history is local to the creator's browser. It stores the opaque link ID,
  timestamps, and a random status-only management token. It never stores the
  share URL, fragment key, plaintext, or ciphertext. The server stores only a
  hash of this token; it authorizes status reads, not retrieval, consumption, or
  extension of the secret. Clearing a history row forgets local tracking; it
  does not revoke a link.
- The server retains only terminal status metadata for up to seven days (subject
  to the bounded 10,000-record tracking capacity). It drops stored references
  to ciphertext and verification material at expiry or successful consumption;
  Go does not guarantee secure zeroization of prior heap allocations. The
  browser refreshes tracked statuses every 30 seconds while the tracking panel
  is visible, and when the tab returns to the foreground.
- Aggregate runtime metrics are available at `GET /api/metrics` only when an
  operator configures a `METRICS_TOKEN` of at least 32 characters. The token is
  compared by hash, entered only for the current browser session, and is not
  saved by the UI. Metrics contain low-cardinality totals only. Memory-store
  totals reset on restart; SQLite lifecycle totals persist in its local DB,
  while rate-limit/overload counters and uptime remain process-local.
- Creation is rate-limited per client (default 60/minute, burst 20), and API
  concurrency is bounded at 256 in-flight requests. `TRUSTED_PROXIES` is the
  explicit allowlist for parsing `X-Forwarded-For`; untrusted forwarding
  headers are ignored. Active capacity defaults to 500 and can be tuned up to
  700 within the container's memory budget. Rate limiting and capacity are
  process-local, not a distributed quota or DDoS defense.
- The PWA has a manifest and a network-only service worker. It does not cache
  app files or API responses and is deliberately unavailable offline.

## Alternatives and trade-offs

- Persisting full share URLs in local storage would make links easy to recover
  after reload but would also persist the decryption key. The status-only token
  keeps monitoring useful without storing that bearer link.
- Public aggregate metrics would simplify scraping but reveal service activity
  to anyone who can reach the app. Metrics therefore stay disabled unless the
  operator configures a separate access token.
- Durable/shared storage and multi-instance support would improve availability
  and capacity, but require a database, recovery/retention policy, and a
  deployment decision. They are not implied by this in-memory implementation.
- Offline caching conflicts with the app's server-authoritative one-time
  lifecycle, so the installed PWA remains online-only.

## Revisit when

See `roadmap.md` for SQLite status and the future optional PostgreSQL backend,
`storage-and-erasure.md` for the erasure boundary, and `performance.md` for
measured behavior and limits. SQLite is optional; memory remains the default.

Use a shared durable store and distributed admission control before running
multiple replicas or promising that generated links survive restarts. Revisit
the terminal metadata retention/capacity if usage metrics show that the current
in-memory bounds are reached.
