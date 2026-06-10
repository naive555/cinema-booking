# Cinema Ticket Booking System — Project Context

## What we're building
A take-home assignment: an online cinema seat-booking system that must stay
correct under concurrency ("must not break when people fight over the same
seat"). This is an interview deliverable, not a production product.

## Grading priorities (optimize for these, in order)
1. Architecture & Concurrency design (35%)
2. Correctness — booking / lock / realtime, NO double booking (30%)
3. DevOps & Security — one-command run, role separation, env config (20%)
4. Code quality & README (15%)
Explicitly NOT graded: pretty UI, real payment gateway, feature completeness.
Do not spend effort there.

## Tech stack (locked — do not substitute)
- Backend: Go + Gin
- Frontend: Vue 3 (built, served via nginx) — not in scope today
- Database: MongoDB
- Cache/Lock: Redis (used for a REAL distributed lock, not just cache)
- Realtime: WebSocket
- Message Queue: Redis Streams (chosen over RabbitMQ to keep docker-compose
  lean and one-command-up reliable; gives durability + consumer groups, unlike
  Pub/Sub)
- Auth: Google OAuth 2.0 -> backend mints its OWN JWT carrying a role claim
- Deploy: Docker + docker-compose, must run with `docker compose up --build` only

## Locked architecture decisions (do NOT re-litigate these)
- Lock: `SET seat:lock:{showtimeId}:{seatId} {ownerToken} NX EX <ttl>`.
  ownerToken = a UUID generated per lock attempt. Release MUST be an atomic Lua
  script that checks ownerToken before DEL (prevents releasing a lock that
  already expired and was re-acquired by someone else). Single Redis node ->
  plain SET NX is correct; do NOT use Redlock (overkill for one node).
- Double-booking safety net: a UNIQUE compound index on bookings
  `{ showtimeId: 1, seatId: 1 }`, created at startup. Last line of defense even
  if lock logic has a bug.
- Seat state source of truth: BOOKED lives durably in Mongo; LOCKED is an
  ephemeral Redis key; AVAILABLE = neither. Seat map = merge(Mongo booked,
  Redis locked).
- Lock TTL: 5 minutes in production, but MUST be configurable via env
  (LOCK_TTL_SECONDS) so the demo can show timeout quickly.
- Roles: USER and ADMIN. ADMIN determined by an ADMIN_EMAILS env allowlist at
  login; role stored in the JWT claim; admin endpoints guarded by middleware.

## Code conventions
- Structure: cmd/server/main.go entrypoint;
  internal/{auth,booking,seat,showtime,realtime,queue,lock};
  config/ for typed env loading. Domain-oriented packages.
- NO hardcoded config anywhere — everything via env, with a typed config struct
  and a committed .env.example.
- Meaningful names, small functions, explicit error wrapping.
- External deps (Mongo, Redis) connected with retry/backoff on startup and a
  graceful shutdown.

## Current phase: DAY 2 — CONCURRENCY CORE (65% of grading)
DO: Redis lock acquire/release with Lua script, select-seat endpoint, mock
payment -> booking write, concurrency test (prove no double-booking under
concurrent requests). Audit logs may be written synchronously.
DO NOT YET: WebSocket realtime push, async Redis Streams queue consumer.