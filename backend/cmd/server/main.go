package main

import (
	"cinema-booking/backend/config"
	"cinema-booking/backend/internal/auth"
	"cinema-booking/backend/internal/domain"
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

	authHandler     := auth.NewHandler(cfg, db)
	seatHandler     := seat.NewHandler(db, rdb)
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

		// Admin sub-group — additionally requires role=ADMIN
		admin := api.Group("/admin", auth.RequireRole(domain.RoleAdmin))
		{
			admin.GET("/ping", func(c *gin.Context) {
				claims, _ := auth.ClaimsFromContext(c)
				c.JSON(http.StatusOK, gin.H{"status": "ok", "authed_as": claims.Email})
			})
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	if err := mongoClient.Disconnect(ctx); err != nil {
		log.Printf("mongo disconnect: %v", err)
	}
	if err := rdb.Close(); err != nil {
		log.Printf("redis close: %v", err)
	}
	log.Println("shutdown complete")
}

func healthzHandler(mc *mongo.Client, rdb *redis.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		code := http.StatusOK
		resp := gin.H{}

		if err := mc.Ping(ctx, nil); err != nil {
			resp["mongo"] = "error: " + err.Error()
			code = http.StatusServiceUnavailable
		} else {
			resp["mongo"] = "ok"
		}

		if err := rdb.Ping(ctx).Err(); err != nil {
			resp["redis"] = "error: " + err.Error()
			code = http.StatusServiceUnavailable
		} else {
			resp["redis"] = "ok"
		}

		if code == http.StatusOK {
			resp["status"] = "ok"
		} else {
			resp["status"] = "degraded"
		}
		c.JSON(code, resp)
	}
}

func connectMongo(cfg *config.Config) *mongo.Client {
	const maxAttempts = 10
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
		if err == nil {
			pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = client.Ping(pingCtx, nil)
			cancel()
		}
		if err == nil {
			log.Printf("mongo: connected")
			return client
		}
		lastErr = err
		wait := time.Duration(i) * 500 * time.Millisecond
		log.Printf("mongo: not ready (attempt %d/%d): %v — retrying in %s", i, maxAttempts, err, wait)
		time.Sleep(wait)
	}
	log.Fatalf("mongo: gave up after %d attempts: %v", maxAttempts, lastErr)
	return nil
}

func connectRedis(cfg *config.Config) *redis.Client {
	const maxAttempts = 10
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
	})
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		lastErr = rdb.Ping(ctx).Err()
		cancel()
		if lastErr == nil {
			log.Printf("redis: connected")
			return rdb
		}
		wait := time.Duration(i) * 500 * time.Millisecond
		log.Printf("redis: not ready (attempt %d/%d): %v — retrying in %s", i, maxAttempts, lastErr, wait)
		time.Sleep(wait)
	}
	log.Fatalf("redis: gave up after %d attempts: %v", maxAttempts, lastErr)
	return nil
}
