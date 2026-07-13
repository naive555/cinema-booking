# Cinema Booking — Resubmission Improvement Plan

> **For the implementing agent (Sonnet):** Execute the phases in the order given at the bottom ("Execution order"). Work phase by phase, run the listed verification after each step, and make one conventional commit per phase using the suggested messages. Do NOT rework anything listed under "Already strong". Do NOT do anything under "Explicitly NOT doing".

## Context

This take-home (cinema seat-booking, graded: Architecture/Concurrency 35%, Correctness 30%, DevOps/Security 20%, Code Quality/README 15%) was submitted before and did not land the job. Goal: strengthen the weak spots a senior reviewer would flag, without scope creep.

### Already strong — do NOT rework
- Lock: SETNX + uuid ownerToken + EX ttl, atomic Lua release, atomic `CheckOwnership` (`backend/internal/lock/lock.go`).
- Pay flow re-verifies ownership atomically, then Mongo unique index `{showtimeId, seatId}` (`backend/internal/store/indexes.go:25-31`) catches the TOCTOU window (`backend/internal/booking/handler.go:95-183`) — three layers of defense.
- Lock expiry → Redis keyspace notifications → WS `SEAT_RELEASED` broadcast + audit (`backend/internal/realtime/realtime.go:120-155`, `backend/cmd/server/main.go:62-73`).
- WS hub actor pattern, per-showtime rooms, slow-client eviction, ping/pong.
- 50-goroutine concurrency proof test (`backend/test/concurrency_test.go`), miniredis lock unit tests, JWT middleware tests, integration tests, Makefile, Postman collection, healthchecked docker-compose, graceful shutdown, startup retry/backoff, ~360-line README with all 7 required sections.

### The gaps this plan fixes (grading-weight order)
1. **Code contradicts docs on queue durability** — `cmd/server/main.go:80` and README §5 claim unacked stream messages are "redelivered on next startup or via XAUTOCLAIM", but `internal/queue/queue.go` only reads `>`; there is no XAUTOCLAIM/XPENDING anywhere. A booking whose consumer errored loses its audit log + notification forever.
2. **No explicit seat release** — frontend cancel (`frontend/src/views/SeatsView.vue:229-235`) only clears local UI; the lock blocks everyone for the full TTL.
3. **JWT travels in URLs** — OAuth callback redirects with `?token=<jwt>` (`internal/auth/handler.go:104`); WS auth accepts `?token=` (`internal/auth/middleware.go:26`). Leaks into history/Referer/logs.
4. **WS `CheckOrigin` always true** (`internal/realtime/realtime.go:171`); OAuth state cookie lacks SameSite/Secure; keyspace channel hardcodes DB 0 (`realtime.go:127`).
5. Frontend: WS reconnect has no backoff and leaves a zombie reconnect timer after unmount; countdown shows raw seconds.
6. Hygiene: three stale `// TODO` stub files; on-disk `.env` holds real secrets (gitignored, never committed — but unsafe if the folder is zipped) with `DEV_AUTH=true`.

Note: one earlier audit claim was verified FALSE — the AdminView audit-action dropdown (`frontend/src/views/AdminView.vue:106`) matches backend actions exactly. No change needed there.

Guiding rule: minimal correct change, reusing existing utilities (`lock.Release`, the audit writer in `booking/handler.go`, `hub.Broadcast`). Every code change gets its README section updated in the same phase so docs never contradict code.

---

## Phase A — Correctness & Architecture (65% of grade)

### A1. Reclaim pending stream messages (XAUTOCLAIM) — fixes the code/doc contradiction
Files: `backend/internal/queue/queue.go`, `backend/cmd/server/main.go` (comment), new `backend/internal/queue/queue_test.go`, `README.md` §5.

1. Extract the message-processing body of `StartConsumer` (queue.go:96-111: unmarshal → poison-pill ack → handler → XACK) into a `process(ctx, group, msgs, h)` helper.
2. Add package-level tunables (vars, so tests can shrink them): `reclaimMinIdle = 30*time.Second`, `reclaimEvery = 15*time.Second`, `maxDeliveries = int64(5)`, `const DeadLetterKey = StreamKey + ":dead"`.
3. Keep the `XReadGroup(">")` loop; add a reclaim pass driven by a `time.Ticker(reclaimEvery)` (non-blocking `select` at top of loop): `XAutoClaim{Stream: StreamKey, Group: group, Consumer: consumer, MinIdle: reclaimMinIdle, Start: "0-0", Count: 10}` → run claimed msgs through the same `process` helper.
4. Dead-letter guard: for claimed IDs, check `XPendingExt`; any with `RetryCount > maxDeliveries` → `XAdd` raw payload to `DeadLetterKey`, `XAck`, log loudly. Prevents infinite redelivery of a permanently failing message.
5. Fix the comment block at `main.go:75-80` to describe the real mechanism.

Test (`queue_test.go`, miniredis v2.38 already in go.mod supports streams): publish an event; XReadGroup as consumer "dead-1" WITHOUT acking; shrink tunables to ~10ms + `miniredis.FastForward`; run `StartConsumer` as "consumer-2" with short-lived ctx; assert handler received the stranded event and XPending count is 0. Second test: always-erroring handler → message lands in dead-letter stream and PEL drains. Fallback if miniredis's XAutoClaim misbehaves: move to `backend/test/` as an integration test against live Redis.

README §5: replace the redelivery sentence with the actual mechanism (reclaim loop min-idle 30s / every 15s, poison-pill ack, dead-letter after 5 attempts).

Verify: `cd backend && go test ./internal/queue/...`. Live drill: pay for a seat, `docker compose stop mongo` so the consumer errors, restart mongo → audit row appears within ~45s without restarting the backend.

Commit: `fix(queue): reclaim pending stream entries via XAUTOCLAIM with dead-letter`

### A2. Explicit seat-release endpoint (cancel)
Files: `backend/internal/booking/handler.go`, `backend/cmd/server/main.go`, `frontend/src/views/SeatsView.vue`, `backend/test/api_test.go`, `cinema-booking.postman_collection.json`, `README.md` §3+§4.

- New `Release` handler mirroring `Pay`: parse params; bind `{ownerToken}`; `released, err := h.locker.Release(ctx, key, req.OwnerToken)`; 500 on err; if `!released` → `200 {"released": false}` (idempotent — no audit/broadcast); if released → write audit `SEAT_RELEASED` with `meta.source="user-cancel"` + `hub.Broadcast` `{Type: SEAT_RELEASED, Status: AVAILABLE}`.
- Load-bearing comment: Lua `DEL` does NOT emit a keyspace `expired` event — only TTL expiry does — so `WatchLockExpiry` never fires for manual release; the explicit broadcast is required.
- Route: `api.DELETE("/showtimes/:showtimeId/seats/:seatId/select", bookingHandler.Release)` next to the other seat routes.
- Frontend `cancelHold` (SeatsView.vue:229-235): make async; best-effort `api.delete(..., { data: { ownerToken } })` in try/catch before the existing optimistic clear (axios passes DELETE bodies via `data`).
- Tests in `api_test.go`: select → release → immediate re-select by another user succeeds; release with wrong token → `released:false`, seat still locked (second select → 409).
- Postman: add "Release seat hold" request.
- README: §3 add "Cancel path (explicit release)" after the timeout path; §4 add the DEL-vs-expired-notification note.

Verify: two browsers — cancel in A, seat flips orange→green in B instantly.

Commit: `feat(booking): add explicit seat release endpoint`

### A3. Horizontal-scaling trade-off write-up (zero code — fold into the D1 README pass)
`README.md` §7 (+ one sentence in §1): "Scaling beyond one backend instance" — (a) WS fanout would move to Redis pub/sub (each instance subscribes to `ws:showtime:{id}`, `Broadcast` becomes PUBLISH) — sketch only, do NOT implement; (b) stream consumer already scales via consumer groups; (c) expiry watcher would duplicate SEAT_RELEASED audit rows across replicas — note dedupe options. State that single-instance was a deliberate scope choice.

---

## Phase B — Security hardening (20%)

### B1. Get the JWT out of URLs
**B1a — OAuth callback → URL fragment.** `internal/auth/handler.go:104`: redirect to `FRONTEND_URL/auth/callback#token=<jwt>` (fragments never reach server logs, stripped from Referer; note one-time-code exchange as the documented production upgrade). `frontend/src/views/AuthCallbackView.vue`: read `new URLSearchParams(route.hash.slice(1)).get('token')`; drop the query path.

**B1b — WS auth → `Sec-WebSocket-Protocol` subprotocol.**
- `internal/auth/middleware.go`: remove the `?token=` branch (line 26); if no Authorization header, parse `Sec-WebSocket-Protocol` sent as `bearer, <jwt>` (split on ",", trim, require `parts[0]=="bearer" && len(parts)==2`). Charset is safe: golang-jwt emits unpadded base64url.
- `internal/realtime/realtime.go` upgrader: add `Subprotocols: []string{"bearer"}` — the server MUST echo the selected subprotocol or browsers abort the handshake.
- `frontend/src/views/SeatsView.vue:132-134`: `new WebSocket(urlWithoutToken, ['bearer', auth.token])`.
- `internal/auth/middleware_test.go`: replace query-param tests with subprotocol tests (valid via subprotocol → 200; garbage → 401; Authorization header wins).
- `frontend/nginx.conf` `/api/ws` already forwards Upgrade headers; verify manually it also passes `Sec-WebSocket-Protocol` (default proxy_pass does).

### B2. WS CheckOrigin (`realtime.go:171`)
Move the upgrader into the Hub: `NewHub(allowedOrigins ...string)`; `main.go` passes `cfg.FrontendURL`. CheckOrigin: allow empty Origin (non-browser clients/tests), else exact match against normalized (lowercased, slash-trimmed) list.

### B3. OAuth state cookie (`auth/handler.go:56,69`)
`c.SetSameSite(http.SameSiteLaxMode)` before each SetCookie (Lax, not Strict — must survive the top-level redirect back from Google); `secure := strings.HasPrefix(cfg.FrontendURL, "https")` as the secure arg.

### B4. Keyspace channel DB hardcode (`realtime.go:127`)
`fmt.Sprintf("__keyevent@%d__:expired", rdb.Options().DB)`.

Verify: `go test ./internal/auth/...`; DEV_AUTH login delivers token via fragment; two-browser WS works through nginx; a WS client with `Origin: http://evil.example` is refused.

Commits: `fix(auth): move JWT out of URLs (fragment redirect + WS subprotocol)`, `fix(realtime): enforce WebSocket origin allowlist and derive keyspace DB`

---

## Phase C — Frontend robustness polish

### C1. WS reconnect backoff + zombie-timer fix (`SeatsView.vue:127-148, 251-254`)
Add `closedByUs` flag, `reconnectTimer`, `reconnectDelay` starting 1s doubling to 15s cap, reset on `onopen`; `onclose` returns early if `closedByUs`; `onUnmounted` sets the flag and clears the timer. (Current bug: unmount → `ws.close()` → `onclose` → reconnect keeps firing after navigation.)

### C2. Countdown mm:ss
Computed `countdownLabel` ("expires in 4:32") in the hold panel instead of raw seconds.

Commit: `fix(frontend): WS reconnect backoff, unmount cleanup, mm:ss countdown`

---

## Phase D — Docs, tests, submission hygiene

### D1. README.md single pass (include A3 here)
- §3: "Cancel path (explicit release)" subsection; auth-flow updated for fragment redirect + WS subprotocol.
- §4: DEL-vs-expired-notification paragraph.
- §5: rewrite redelivery paragraph → XAUTOCLAIM loop, dead-letter stream, poison-pill ack.
- §6: explicit callout — assignment specifies a 5-minute hold; code default is `LOCK_TTL_SECONDS=300`; compose pins 15s so the reviewer can watch expiry live; set 300 in `.env` for spec-faithful behavior.
- §7: scaling entry (A3), JWT-in-localStorage vs httpOnly-cookie trade-off, CheckOrigin policy, rate limiting/refresh tokens out of scope.
- New short "Security notes" subsection: JWT never in URLs, SameSite=Lax + conditional Secure state cookie, origin-checked WS, datastore ports unexposed, secrets via .env.

### D2. Delete stale TODOs
`backend/internal/auth/auth.go`, `backend/internal/seat/seat.go`, `backend/internal/showtime/showtime.go`: delete only the `// TODO: ...` line, keep the package doc sentence. `go build ./...`.

### D3. Stretch test (only if time remains)
Integration test in `backend/test/`: dial `/api/ws` with gorilla `websocket.Dialer` sending `Sec-WebSocket-Protocol: bearer, <token>` (end-to-end-verifies B1b), select a seat, wait TTL+2s (15s in compose), assert a `SEAT_RELEASED` frame — covers the expiry→broadcast path. Guard with the existing skip-if-stack-down pattern.

### D4. Postman collection
Add the Release request; update any `?token=` WS instructions to the subprotocol scheme.

### D5. Submission hygiene (do LAST)
Replace real secrets in on-disk `.env` with placeholders — then STOP and tell the user to rotate the Google OAuth client secret in the Google console themselves (it lived on disk); set `DEV_AUTH=false` in `.env`; confirm `.env.example` completeness; `gofmt -l .` + `go vet ./...` clean.

Commits: `docs: update README for release path, queue redelivery, scaling trade-offs`, `chore: remove stale TODO stubs and scrub .env placeholders`

---

## Execution order
A1 → A2 → B1a → B1b → B2 → B3 → B4 → C1 → C2 → A3+D1 (one README pass) → D2 → D3 (optional) → D4 → final checklist → D5.

## Final verification checklist
1. `docker compose down -v && docker compose up --build -d` with `SEED_ON_START=true DEV_AUTH=true` — all four containers healthy.
2. `cd backend && go test ./...` — lock, middleware (incl. subprotocol), queue reclaim all pass.
3. `make test-concurrency` — 50 goroutines, exactly 1 success.
4. `go test ./test/...` — incl. new release-endpoint tests.
5. Two-browser manual: select→orange, **cancel→green instantly (new)**, pay→red, expire→green after 15s.
6. PEL drill: stop mongo right after pay, restart → audit row appears within ~45s, no backend restart.
7. Login token arrives in `#token=` fragment; backend access log contains no JWT.
8. WS connects via subprotocol auth; wrong-Origin WS refused.
9. Admin audit log shows `SEAT_RELEASED` with `meta.source=user-cancel`.
10. Fresh-clone rehearsal following README §6 verbatim.
11. `.env` placeholders only; clean conventional git log.

## Explicitly NOT doing (scope creep)
Multi-seat/cart booking, seat-map editor, real payment, implementing Redis pub/sub WS fanout (documented only), Kubernetes/Helm/CI/TLS, rate limiting, refresh tokens, one-time-code OAuth handoff (documented as future work), UI redesign, Streams→RabbitMQ migration.
