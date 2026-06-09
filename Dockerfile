# ── Stage 1: build ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Dependency layer cached separately from source
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /cinema-booking ./cmd/server

# ── Stage 2: runtime ──────────────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates needed for TLS to external services (OAuth, etc.)
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app \
    && adduser  -S -G app app

COPY --from=builder /cinema-booking /cinema-booking

USER app
EXPOSE 8080

ENTRYPOINT ["/cinema-booking"]
