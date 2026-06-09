package main

import (
	"cinema-booking/backend/config"
	"cinema-booking/backend/internal/seed"
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

	// Ensure indexes (idempotent — safe on every startup).
	idxCtx, idxCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := store.EnsureIndexes(idxCtx, db); err != nil {
		log.Fatalf("startup: %v", err)
	}
	idxCancel()

	// Optional seed (controlled by SEED_ON_START=true).
	if cfg.SeedOnStart {
		seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := seed.Run(seedCtx, db); err != nil {
			log.Fatalf("startup: seed: %v", err)
		}
		seedCancel()
	}

	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", healthzHandler(mongoClient, rdb))

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

// connectMongo dials MongoDB with linear backoff, fatally exiting if it never becomes reachable.
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

// connectRedis dials Redis with linear backoff, fatally exiting if it never becomes reachable.
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
