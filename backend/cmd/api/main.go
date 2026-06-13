package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"punchline/backend/internal/cards"
	"punchline/backend/internal/httpapi"
	"punchline/backend/internal/realtime"
	"punchline/backend/internal/roomstore"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	port := getenv("PORT", "8080")
	instanceID := instanceID()
	roomLeaseTTL := getenvDuration("ROOM_LEASE_TTL", 6*time.Hour)
	roomIdleTTL := getenvDuration("ROOM_IDLE_TTL", 30*time.Minute)
	maxLocalRooms := getenvInt("MAX_LOCAL_ROOMS", 5000)

	deckPath, err := cards.FindSeedDeckPath()
	if err != nil {
		log.Fatalf("find seed deck: %v", err)
	}
	deck, err := cards.LoadSeedDeck(deckPath)
	if err != nil {
		log.Fatalf("load seed deck %s: %v", deckPath, err)
	}
	registry, cleanup := roomRegistry()
	defer cleanup()

	manager := realtime.NewRoomManager(
		deck,
		realtime.WithInstanceID(instanceID),
		realtime.WithRoomRegistry(registry),
		realtime.WithRoomLeaseTTL(roomLeaseTTL),
		realtime.WithRoomIdleTTL(roomIdleTTL),
		realtime.WithMaxLocalRooms(maxLocalRooms),
	)
	manager.StartHeartbeat(ctx, time.Minute)
	manager.StartJanitor(ctx, time.Minute)
	handler := httpapi.NewHandler(manager)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}

	go func() {
		log.Printf("punchline backend listening on :%s instance=%s registry=%s", port, instanceID, registry.RegistryName())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Println("shutdown signal received; draining connections")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("invalid %s=%q; using %s", key, v, fallback)
		return fallback
	}
	return d
}

func getenvInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("invalid %s=%q; using %d", key, v, fallback)
		return fallback
	}
	return n
}

func instanceID() string {
	for _, key := range []string{"PUNCHLINE_INSTANCE_ID", "FLY_MACHINE_ID", "HOSTNAME"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "local"
}

func roomRegistry() (realtime.RoomRegistry, func()) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return realtime.NewMemoryRoomRegistry(), func() {}
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatalf("open postgres room registry: %v", err)
	}
	// Bound the pool so a traffic spike can't exhaust Postgres connections.
	db.SetMaxOpenConns(getenvInt("DB_MAX_OPEN_CONNS", 10))
	db.SetMaxIdleConns(getenvInt("DB_MAX_IDLE_CONNS", 5))
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		log.Fatalf("connect postgres room registry: %v", err)
	}
	return roomstore.NewPostgresRoomRegistry(db), func() { _ = db.Close() }
}
