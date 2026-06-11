package main

import (
	"cinema-booking/backend/config"
	"cinema-booking/backend/internal/auth"
	"cinema-booking/backend/internal/booking"
	"cinema-booking/backend/internal/domain"
	"cinema-booking/backend/internal/lock"
	"cinema-booking/backend/internal/queue"
	"cinema-booking/backend/internal/realtime"
	"cinema-booking/backend/internal/seat"
	"cinema-booking/backend/internal/seed"
	"cinema-booking/backend/internal/showtime"
	"cinema-booking/backend/internal/store"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("startup: %v", err)
	}

	mongoClient := connectMongo(cfg)
	rdb := connectRedis(cfg)

	db := mongoClient.Database(cfg.MongoDB)

	idxCtx, idxCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := store.EnsureIndexes(idxCtx, db); err != nil {
		log.Fatalf("startup: %v", err)
	}
	idxCancel()

	if cfg.SeedOnStart {
		seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := seed.Run(seedCtx, db); err != nil {
			log.Fatalf("startup: seed: %v", err)
		}
		seedCancel()
	}

	// Context that lives for the entire process lifetime; cancelled on shutdown
	// to stop background goroutines (queue consumer, WS expiry watcher).
	appCtx, appCancel := context.WithCancel(context.Background())

	// ── realtime hub ──────────────────────────────────────────────────────────
	wsHub := realtime.NewHub()
	go wsHub.Run()
	go wsHub.WatchLockExpiry(appCtx, rdb, func(showtimeID, seatID string) {
		stid, _ := bson.ObjectIDFromHex(showtimeID)
		seatOID, _ := bson.ObjectIDFromHex(seatID)
		if err := insertAuditLog(appCtx, db, domain.AuditLog{
			Action:     "SEAT_RELEASED",
			ShowtimeID: stid,
			SeatID:     seatOID,
		}); err != nil {
			log.Printf("audit: SEAT_RELEASED write failed: %v", err)
		}
		log.Printf("audit: SEAT_RELEASED showtime=%s seat=%s", showtimeID, seatID)
	})

	// ── queue client + async consumer ─────────────────────────────────────────
	// The consumer reads BOOKING_SUCCESS events from the Redis Stream and:
	//   (a) writes the AuditLog to Mongo   — durable record of every confirmed booking
	//   (b) sends a mock email notification — replace with real mailer in production
	// XACK is sent only after both steps succeed; on failure the message stays
	// in the PEL and will be redelivered on next startup or via XAUTOCLAIM.
	qClient := queue.New(rdb)
	go qClient.StartConsumer(appCtx, "cinema-consumers", "consumer-1", func(ev queue.Event) error {
		stid, _ := bson.ObjectIDFromHex(ev.ShowtimeID)
		seatOID, _ := bson.ObjectIDFromHex(ev.SeatID)
		userOID, _ := bson.ObjectIDFromHex(ev.UserID)

		// (a) Write audit log — the consumer is the ONLY writer for BOOKING_SUCCESS.
		if err := insertAuditLog(appCtx, db, domain.AuditLog{
			Action:     ev.Action,
			UserID:     userOID,
			ShowtimeID: stid,
			SeatID:     seatOID,
			Meta:       map[string]any{"bookingId": ev.BookingID, "source": "stream-consumer"},
		}); err != nil {
			log.Printf("consumer: audit write failed (will retry): %v", err)
			return err // do NOT ack — message stays in PEL for redelivery
		}

		// (b) Mock notification — in production send email/push/webhook here.
		log.Printf("NOTIFY: emailed %s about booking %s (showtime=%s seat=%s)",
			ev.UserEmail, ev.BookingID, ev.ShowtimeID, ev.SeatID)
		return nil // success -> XACK
	})

	lockClient := lock.New(rdb)
	authHandler := auth.NewHandler(cfg, db)
	bookingHandler := booking.NewHandler(db, lockClient, cfg.LockTTL, wsHub, qClient)
	seatHandler := seat.NewHandler(db, rdb)
	showtimeHandler := showtime.NewHandler(db)

	r := gin.New()
	r.Use(gin.Recovery())

	// Health
	r.GET("/healthz", healthzHandler(mongoClient, rdb))

	// OAuth flow
	r.GET("/auth/google/login", authHandler.Login)
	r.GET("/auth/google/callback", authHandler.Callback)

	// Dev-only token minting — never registered unless DEV_AUTH=true
	if cfg.DevAuth {
		log.Println("WARNING: DEV_AUTH=true — /dev/token endpoint is active (not for production)")
		r.POST("/dev/token", authHandler.DevToken)
	}

	// Protected API — all routes require a valid JWT
	api := r.Group("/api", auth.Middleware(cfg))
	{
		api.GET("/me", authHandler.Me)
		api.GET("/showtimes", showtimeHandler.ListShowtimes)
		api.GET("/showtimes/:showtimeId/seats", seatHandler.GetSeatMap)
		api.POST("/showtimes/:showtimeId/seats/:seatId/select", bookingHandler.Select)
		api.POST("/showtimes/:showtimeId/seats/:seatId/pay", bookingHandler.Pay)

		// WebSocket — auth enforced by parent middleware via Authorization header
		api.GET("/ws", wsHub.Handler)

		// Admin sub-group — additionally requires role=ADMIN
		admin := api.Group("/admin", auth.RequireRole(domain.RoleAdmin))
		{
			admin.GET("/ping", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"status": "ok"})
			})
			admin.GET("/bookings", bookingHandler.AdminListBookings)
			admin.GET("/audit-logs", bookingHandler.AdminListAuditLogs)
		}
	}

	srv := &http.Server{
		Addr:    ":" + cfg.ServerPort,
		Handler: r,
	}

	go func() {
		log.Printf("server listening on :%s", cfg.ServerPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutdown signal received")

	// Stop background goroutines before draining HTTP.
	appCancel()

	httpCtx, httpCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer httpCancel()
	if err := srv.Shutdown(httpCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()
	if err := mongoClient.Disconnect(dbCtx); err != nil {
		log.Printf("mongo disconnect: %v", err)
	}

	if err := rdb.Close(); err != nil {
		log.Printf("redis close: %v", err)
	}
	log.Println("shutdown complete")
}

// insertAuditLog stamps CreatedAt and writes a single audit entry to Mongo.
func insertAuditLog(ctx context.Context, db *mongo.Database, entry domain.AuditLog) error {
	entry.CreatedAt = time.Now()
	_, err := db.Collection("audit_logs").InsertOne(ctx, entry)
	return err
}

func healthzHandler(mc *mongo.Client, rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		healthy := true
		resp := gin.H{}

		if err := mc.Ping(ctx, nil); err != nil {
			resp["mongo"] = "error: " + err.Error()
			healthy = false
		} else {
			resp["mongo"] = "ok"
		}

		if err := rdb.Ping(ctx).Err(); err != nil {
			resp["redis"] = "error: " + err.Error()
			healthy = false
		} else {
			resp["redis"] = "ok"
		}

		if healthy {
			resp["status"] = "ok"
			c.JSON(http.StatusOK, resp)
		} else {
			resp["status"] = "degraded"
			c.JSON(http.StatusServiceUnavailable, resp)
		}
	}
}

func connectMongo(cfg *config.Config) *mongo.Client {
	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		log.Fatalf("mongo: %v", err)
	}
	retryConnect("mongo", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return client.Ping(ctx, nil)
	})
	return client
}

func connectRedis(cfg *config.Config) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})
	retryConnect("redis", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return rdb.Ping(ctx).Err()
	})
	return rdb
}

// retryConnect calls ping up to 10 times with linear backoff, fatal on exhaustion.
func retryConnect(name string, ping func() error) {
	const maxAttempts = 10
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		if lastErr = ping(); lastErr == nil {
			log.Printf("%s: connected", name)
			return
		}
		wait := time.Duration(i) * 500 * time.Millisecond
		log.Printf("%s: not ready (attempt %d/%d): %v — retrying in %s", name, i, maxAttempts, lastErr, wait)
		time.Sleep(wait)
	}
	log.Fatalf("%s: gave up after %d attempts: %v", name, maxAttempts, lastErr)
}
