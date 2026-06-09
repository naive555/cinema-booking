# Cinema Ticket Booking System

A concurrent seat-booking service built for correctness under contention.
Multiple users racing for the same seat will never produce a double booking.

---

## 1. System Architecture

```mermaid
flowchart TD
    Browser["Browser / Vue 3\n(served by nginx)"]

    subgraph Backend ["Go / Gin Backend"]
        Auth["Auth Handler\nGoogle OAuth -> JWT"]
        BookingH["Booking Handler"]
        SeatH["Seat Handler"]
        WS["WebSocket Hub"]
        Consumer["Queue Consumer\n(Redis Streams)"]
    end

    subgraph Persistence
        Mongo[("MongoDB\nbookings · seats\nshowtimes · users")]
        Redis[("Redis\nSeat Locks  SET NX EX\nAudit Stream  XADD")]
    end

    Browser -->|"REST  /api/..."| BookingH
    Browser -->|"REST  /api/..."| SeatH
    Browser -->|"OAuth redirect"| Auth
    Browser <-->|"WebSocket  /ws"| WS

    Auth -->|"upsert user"| Mongo
    BookingH -->|"SET NX EX  acquire"| Redis
    BookingH -->|"insert booking"| Mongo
    BookingH -->|"Lua DEL  release"| Redis
    BookingH -->|"XADD booking.events"| Redis
    SeatH -->|"read bookings"| Mongo
    SeatH -->|"KEYS seat:lock:*"| Redis

    Consumer -->|"XREADGROUP"| Redis
    Consumer -->|"insert audit log"| Mongo
    Consumer -->|"broadcast"| WS
    WS -->|"seat-map push"| Browser
```

**Request path for a booking (happy path):**
`Browser -> BookingH -> Redis lock (NX) -> Mongo insert -> Redis stream -> release lock -> 200`

**Realtime path:**
`Queue Consumer reads stream -> writes AuditLog -> broadcasts via WS Hub -> Browser updates seat map`

---

## 2. Tech Stack

| Layer | Choice | Why |
|-------|--------|-----|
| Backend | Go 1.26 + Gin | Low-overhead goroutines map cleanly onto per-request lock/wait semantics; Gin adds routing without magic |
| Database | MongoDB 7 | Flexible schema for seat/showtime documents; atomic `findAndModify` available as a fallback; driver v2 |
| Cache / Lock | Redis 7 | Atomic `SET NX EX` for distributed locks; Streams for durable async queue — one infra component does both |
| Realtime | WebSocket (Gin upgrade) | Push seat-map deltas to all connected clients without polling |
| Queue | Redis Streams | See Section 5 |
| Auth | Google OAuth 2.0 -> backend JWT | Delegates credential management to Google; backend mints its own JWT so we control the role claim |
| Frontend | Vue 3 (pre-built, served by nginx) | Out of scope for this assessment |
| Deploy | Docker + docker-compose | Single `docker compose up --build` brings the full stack; no external dependencies |

---

## 3. Booking Flow

> **To be completed in DAY 1.**
> Will cover: seat availability check -> Redis lock acquisition -> Mongo booking insert -> stream publish -> lock release -> WebSocket broadcast. Sequence diagram and error paths TBD.

---

## 4. Redis Lock Strategy

### Mechanism

```
SET seat:lock:{showtimeId}:{seatId}  {ownerToken}  NX  EX {LOCK_TTL_SECONDS}
```

- **`NX`** — only sets if the key does not exist. Two concurrent requests can race; exactly one wins.
- **`EX {ttl}`** — key auto-expires, so a crashed client never leaves a seat locked forever. TTL is configurable via `LOCK_TTL_SECONDS` (default 300 s; set to 30 s for the demo so timeout behaviour is observable in real time).
- **`ownerToken`** — a UUID generated fresh per lock attempt, stored as the key's value.

### Release: atomic Lua script

```lua
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
```

The check-then-delete is atomic at the Redis server. Without it, the following race is possible:

1. Client A's lock expires (TTL hit).
2. Client B acquires the same key.
3. Client A's deferred `DEL` fires -> deletes Client B's lock -> seat appears free while B thinks it holds the lock.

The Lua script ensures a client can only release a lock it still owns.

### Why plain SET NX, not Redlock

Redlock is designed for multi-node Redis deployments where clock drift and partial writes are real concerns. This system runs a **single Redis node**, so `SET NX EX` is both correct and sufficient. Redlock would add implementation complexity (multiple round-trips, quorum logic) with no correctness benefit here.

### Final safety net: MongoDB unique index

Even if lock logic has a latent bug, the compound unique index on `bookings { showtimeId: 1, seatId: 1 }` causes the second concurrent insert to fail with a duplicate-key error rather than silently succeed. The application catches this error and returns a conflict response. The lock prevents contention; the index prevents corruption.

---

## 5. Message Queue (Redis Streams)

### What it does

On every successful booking, the booking handler publishes an event to the `booking.events` stream:

```
XADD booking.events * bookingId <id> showtimeId <id> seatId <id> userId <id> action BOOKED
```

A background consumer group (`XREADGROUP GROUP cinema-consumers consumer-1`) reads these events and:
1. Writes an `AuditLog` document to MongoDB (durable record of every state change).
2. Notifies the WebSocket hub so all connected clients receive a seat-map update.

### Why Redis Streams over Pub/Sub

| Property | Pub/Sub | Streams |
|----------|---------|---------|
| Durability | No — messages are lost if no subscriber is connected | Yes — messages persist in the log |
| Consumer groups | No | Yes — multiple consumers, each message ACKed exactly once |
| Replay / backfill | No | Yes — new consumers can read from any offset |
| At-least-once delivery | No | Yes — unACKed messages stay in the PEL for retry |

Pub/Sub would work if the consumer were always running and we accepted lost events on restart. For an audit log that is a non-starter.

### Why Streams over RabbitMQ

RabbitMQ would be the right choice in a polyglot microservice environment. Here, Redis already exists for locking. Adding RabbitMQ would mean a third infra component, a longer `docker-compose.yml`, a separate Go AMQP client, and a more complex `docker compose up`. The Streams API gives us consumer groups and durability without that cost. For a single-service system under interview conditions this is the correct trade-off.

---

## 6. Running Locally

> **To be completed.**
> Will be: `cp .env.example .env` (fill in OAuth credentials) -> `docker compose up --build`. One command, no other setup.

---

## 7. Assumptions & Trade-offs

**Single Redis node**
Accepted. A multi-node cluster would require Redlock or a different locking scheme. At interview scale, single-node Redis with `SET NX EX` is correct and simpler to reason about.

**Seat status is computed, not stored**
`BOOKED` is a durable MongoDB document. `LOCKED` is an ephemeral Redis key. `AVAILABLE` means neither exists. There is no `status` field on the Seat document. This avoids a write-ahead cache-invalidation problem: if we cached status in Mongo, a lock expiry would require a Mongo update, which adds a write and a potential consistency gap. The merge-at-read approach is always consistent at the cost of two reads per seat map fetch.

**Lock TTL determines maximum hold time**
If a user acquires a lock and then closes the tab, the seat is released after `LOCK_TTL_SECONDS`. There is no explicit "cancel reservation" flow yet. The TTL is the safety valve.

**Roles are minted at login, not dynamically updated**
`ADMIN_EMAILS` is an env allowlist checked at OAuth callback time. The role is baked into the JWT. Revoking admin access requires the token to expire (up to `JWT_EXPIRY_HOURS`) or a deploy with the email removed from the allowlist. Acceptable for an internal admin use case; not acceptable for a fine-grained RBAC system.

**Idempotent seed for demo convenience**
`SEED_ON_START=true` upserts a fixed set of showtimes and seats using `$setOnInsert`. It is safe to run on a populated database and will not overwrite existing data. Disabled by default.

**No payment integration**
Booking succeeds as soon as the Mongo insert commits. Payment is explicitly out of scope per the assignment brief.
