# HUE — Code Review & Improvement Plan

Date: 2026-05-01
Scope: Whole repository at this commit. Grounded in actual code, not the PRD.

---

## What HUE is

HUE (Hiddify Usage Engine) is a **protocol-agnostic Go control plane** that VPN/proxy nodes (Xray, Singbox, WireGuard, OpenVPN, RADIUS, …) talk to in order to:

- **Track usage** (upload/download bytes per user, per node, per service) via a unified gRPC report API.
- **Enforce quotas and concurrent-session limits**, with per-user penalties on violation.
- **Manage subscription packages** (traffic caps, periodic resets, expiry), users, services, and nodes through a REST and gRPC admin API.
- **Roll up usage through a multi-level manager hierarchy** (e.g. reseller → sub-reseller) with limits enforced at each level.
- **Emit immutable events** (`USER_CONNECTED`, `USAGE_RECORDED`, `PACKAGE_EXPIRED`, …) for audit and downstream consumers via gRPC streams.

Architecture in one paragraph: a single Go binary with a cmux'd dual-server (gRPC on `:50051`, HTTP REST on `:50052`), backed by SQLite split into UserDB (metadata + counters), ActiveDB (buffered usage flushed every 5 min), and HistoryDB (long-term logs). An in-memory cache fronts hot paths and a `sync.Map`-based per-user lock manager isolates concurrent updates. Targets ~1000 users on a single instance, with TimescaleDB + multi-instance as the documented scale-out path.

The core privacy promise — **zero raw-IP retention** — is genuinely implemented: client IPs are used to count concurrent sessions and resolve geo metadata via MaxMind, then discarded. Only a hash + geo info is stored.

---

## Verified concerns

| # | Concern | Verdict | Where |
|---|---|---|---|
| 1 | Auth via `?secret=` query param | Better than the docs claim — uses `Hue-API-Key` header instead | [internal/api/http/server.go:111](internal/api/http/server.go#L111), [internal/api/grpc/server.go:744](internal/api/grpc/server.go#L744) |
| 2 | "TLS mandatory" but missing in deploy | **Confirmed.** TLS config is built but never wired into the listener | [internal/auth/auth.go](internal/auth/auth.go), [cmd/hue/main.go:179-208](cmd/hue/main.go#L179-L208) |
| 3 | Plaintext secrets in DB | **Confirmed.** `users.password`, `users.private_key`, `nodes.secret_key`, `services.secret_key` all stored verbatim | [internal/storage/sqlite/user_db.go:62-128](internal/storage/sqlite/user_db.go#L62-L128) |
| 4 | Single shared `HUE_AUTH_SECRET` | **Confirmed.** No per-manager auth despite manager hierarchy | [internal/api/http/server.go:119](internal/api/http/server.go#L119) |
| 5 | `cmd/hue/hue.exe` (28 MB) checked into git | **Confirmed.** `.gitignore` is 5 lines, doesn't list `*.exe` | `.gitignore`, `cmd/hue/hue.exe` |
| 6 | Two SQLite drivers in `go.mod` | Partially. Only `modernc.org/sqlite` is actually registered; `mattn/go-sqlite3` is unused transitive | [internal/storage/sqlite/db.go:9](internal/storage/sqlite/db.go#L9) |
| 7 | `make proto` clones googleapis on every run | **Confirmed.** No idempotency guard, second run fails | [Makefile:30](Makefile#L30) |

---

## Findings by category

### CRITICAL

| # | Issue | Where | Fix |
|---|---|---|---|
| C1 | The master auth secret is logged in plaintext at every startup | [cmd/hue/main.go:80](cmd/hue/main.go#L80) | Delete the line. Never log secrets. |
| C2 | Env-secret comparison uses `==`, vulnerable to timing side-channel. Only the DB path uses constant-time compare ([user_db.go:988](internal/storage/sqlite/user_db.go#L988)) | [internal/api/grpc/server.go:753](internal/api/grpc/server.go#L753), [internal/api/http/server.go:119](internal/api/http/server.go#L119) | `subtle.ConstantTimeCompare([]byte(apiKey), []byte(srv.secret))` |
| C3 | gRPC and HTTP are both served plaintext via cmux. The TLS plumbing in `internal/auth/auth.go` is dead code | [cmd/hue/main.go:179-208](cmd/hue/main.go#L179-L208) | Wire `auth.NewAuthenticator(...).GetTLSConfig()` into `grpc.Creds(...)` and wrap the cmux listener with `tls.NewListener` |

### HIGH

| # | Issue | Where | Fix |
|---|---|---|---|
| H1 | `db.SetMaxOpenConns(1)` serializes ALL DB ops including reads — directly contradicts the buffered-write performance story | [internal/storage/sqlite/db.go:41](internal/storage/sqlite/db.go#L41) | `MaxOpenConns >= N` with DSN `_journal=WAL&_busy_timeout=5000`; WAL allows concurrent readers |
| H2 | `RecordUsage` holds the per-user write lock across 4–6 sequential SQLite calls. Combined with H1, every user serializes globally | [internal/engine/quota.go:222-281](internal/engine/quota.go#L222-L281) | Do math under lock, fire DB writes outside or batch in one tx |
| H3 | `BatchReportUsage` calls `ReportUsage` in a loop — N round-trips and lock acquisitions, no transaction | [internal/api/grpc/server.go:148-165](internal/api/grpc/server.go#L148-L165) | Acquire per-user lock once, batch DB writes in a single tx |
| H4 | gRPC `ReportUsage` reimplements engine logic instead of calling `engine.ProcessUsageReport`. Diverged: misses manager limits, event emission, disconnect propagation | [internal/api/grpc/server.go:65-146](internal/api/grpc/server.go#L65-L146) | Inject `*engine.Engine`, delegate |
| H5 | `UpdateNodeUsage` / `UpdateServiceUsage` errors are silently ignored — quota will drift between user/node/service | [internal/api/grpc/server.go:127-130](internal/api/grpc/server.go#L127-L130) | Log at warn or fail the report |
| H6 | `createUser` accepts `Password`, `PrivateKey`, `Username`, and unbounded `[]string` fields with zero validation (no length cap, no charset, no required-field enforcement) | [internal/api/http/server.go:190-217](internal/api/http/server.go#L190-L217) | `go-playground/validator` is already a transitive dep — add `binding:"required,max=255"` and reject unbounded slices |
| H7 | CORS `Allow-Origin: *` on admin endpoints | [internal/api/http/server.go:94-107](internal/api/http/server.go#L94-L107) | Allow-list specific origins from config; never `*` for admin |
| H8 | `engine.Engine` re-fetches the package after `RecordUsage` already fetched and decided — extra round-trip per usage report on the hot path | [internal/engine/engine.go:177](internal/engine/engine.go#L177) | Have `RecordUsage` return the updated package |

### MEDIUM

| # | Issue | Where | Fix |
|---|---|---|---|
| M1 | Background flush goroutine has no `recover()`. A panic kills the writer silently and the buffer (cap 1000, [active_db.go:36](internal/storage/sqlite/active_db.go#L36)) overflows on next push | [cmd/hue/main.go:148-159](cmd/hue/main.go#L148-L159) | `defer func(){ if r:=recover(); r!=nil { logger.Error(...) } }()` and bound the buffer |
| M2 | `Publish` non-blocking sends silently drop events when the receiver is full — no log, no metric | [internal/eventstore/receivers.go:73-76](internal/eventstore/receivers.go#L73-L76) | Add a per-receiver `dropped_events` counter and log at warn |
| M3 | Two parallel lock-manager implementations, both leak entries forever — `sync.Map` is never cleaned on user delete | [internal/auth/lock.go:9-11](internal/auth/lock.go#L9-L11), [internal/engine/quota.go:22](internal/engine/quota.go#L22) | Consolidate into one `LockManager`; delete the entry in `DeleteUser` |
| M4 | `BufferUsage` is exported but never called — `usage_reports` table is unwritten | [internal/storage/sqlite/active_db.go:78-89](internal/storage/sqlite/active_db.go#L78-L89) | Wire it into `RecordUsage` or drop the table |
| M5 | LIMIT/OFFSET injected via `fmt.Sprintf("%d", …)` — currently safe (parsed as int upstream) but fragile | [internal/storage/sqlite/user_db.go:379-382](internal/storage/sqlite/user_db.go#L379-L382), [history_db.go:125,211](internal/storage/sqlite/history_db.go) | Use `?` placeholders |
| M6 | `generateID()` returns `UnixNano()` formatted as decimal — colliding under load | [internal/storage/sqlite/history_db.go:310](internal/storage/sqlite/history_db.go#L310) | Use `uuid.New().String()` like everywhere else |
| M7 | Shutdown order is reversed: HTTP `Shutdown` has a 5s timeout but cmux/listener doesn't drain, and final ActiveDB flush runs *before* server stop, so usage reports during shutdown are lost | [cmd/hue/main.go:217-247](cmd/hue/main.go#L217-L247) | Stop accepting new requests → drain in-flight → final flush |
| M8 | Plaintext `users.password`, `nodes.secret_key`, `services.secret_key` in the DB; only owner/service auth keys go through SHA-256 (and SHA-256 alone is not a password KDF — no salt, fast) | [internal/storage/sqlite/user_db.go:62-128](internal/storage/sqlite/user_db.go#L62-L128), [user_db.go:1031](internal/storage/sqlite/user_db.go#L1031) | bcrypt / argon2id for passwords; salted SHA-256 for service/node keys |

### LOW

| # | Issue | Where | Fix |
|---|---|---|---|
| L1 | `gin v1.9.1` has CVE-2023-29401 (file upload bypass, fixed in 1.9.2) | [go.mod:6](go.mod#L6) | Bump to `gin v1.10.0+` |
| L2 | "Daily rotating salt" allows correlation across the day and creates a discontinuity at midnight | [internal/engine/session.go:155](internal/engine/session.go#L155) | Process-lifetime random salt is enough — sessions are short-lived |
| L3 | `Dockerfile` sets `CGO_ENABLED=1` for nothing (modernc.org/sqlite is pure-Go); causes ~30 s longer build with libc deps | `deployments/docker/Dockerfile:16` | `CGO_ENABLED=0`, remove the gcc/musl install |

---

## Top 8 prioritized actions

1. **Stop logging the auth secret** ([main.go:80](cmd/hue/main.go#L80)) and switch all secret comparisons to `subtle.ConstantTimeCompare`. One-line fix, eliminates two CVE-class issues in the server's own logs.
2. **Wire TLS into the actual servers.** The `internal/auth/auth.go` plumbing exists but is never called. Without it the shared admin secret crosses the wire in cleartext.
3. **Fix the SQLite contention bottleneck.** `MaxOpenConns=1` plus write-lock-held-across-many-queries will fall over under the 1000-user load the docs target. Open multiple connections (WAL allows concurrent readers), shrink critical sections, batch in `BatchReportUsage`.
4. **Replace plaintext password storage** with bcrypt/argon2id; salt-and-hash node/service secret keys the same way owner keys are hashed.
5. **Per-manager API keys.** The manager hierarchy and limit-enforcement code already works; add a `manager_auth_keys` table mirroring `service_auth_keys`, propagate `manager_id` through `gin.Context`, and authorize admin ops by manager.
6. **Have gRPC `ReportUsage` delegate to `engine.Engine.ProcessUsageReport`** ([grpc/server.go:65](internal/api/grpc/server.go#L65)). The two paths have already diverged.
7. **Repo hygiene**: remove `cmd/hue/hue.exe` from git, gitignore `*.exe`, `bin/`, `coverage.out`, `third_party/`, and add idempotency to the `Makefile` `proto` target (`[ -d third_party/googleapis ] || git clone …`).
8. **Add a race-flagged concurrent quota-enforcement integration test.** The whole product is "enforce quota under concurrency" but [internal/engine/engine_test.go](internal/engine/engine_test.go) is sequential. One `t.Parallel()` test under `-race` would catch the lock issues in #3.

---

## What HUE does well

1. **Privacy posture on raw IPs is real**, not just a slogan. The IP flows through the geo+session functions and is then dropped — the DB never sees it ([internal/engine/geo.go:32-61](internal/engine/geo.go#L32-L61), [session.go:150-157](internal/engine/session.go#L150-L157)). Unusual in this category of project.
2. **Per-user `sync.Map` locking** is the right pattern for fine-grained concurrency. Most projects ship one global mutex around the engine.
3. **Manager hierarchy is fully implemented**, including ancestor traversal, parent-package validation, and projected-usage checks before commit ([user_db.go:1196-1312](internal/storage/sqlite/user_db.go#L1196-L1312)). PRD-to-code parity here is better than expected.
4. **Graceful shutdown is wired up** — signal handling, context cancellation, final flush, gRPC `GracefulStop`, and HTTP `Shutdown` are all present. Many small Go services skip half of this.

---

## Roadmap gap (from PRD vs code)

The PRD lists protocol adapters (Xray, Singbox, WireGuard, RADIUS) and event receivers as in-scope, but only the **transport layer** exists today — there is no protocol-specific adapter package. RADIUS in particular needs a UDP listener and AVP encoding, neither of which is present. The README's roadmap reflects this honestly (`[ ]` for adapters and RADIUS).
