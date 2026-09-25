package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/example/integrated-recorder/internal/acquire"
	"github.com/example/integrated-recorder/internal/network"
	"github.com/example/integrated-recorder/internal/server"
	"github.com/example/integrated-recorder/internal/storage"
)

func main() {
	addr := strings.TrimSpace(os.Getenv("ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "./data"
	}

	store, err := storage.New(dataDir)
	if err != nil {
		log.Fatalf("initialize storage: %v", err)
	}
	manager, err := acquire.NewManager(store, network.NewPublicHTTPClient(25*time.Second), nil, nil)
	if err != nil {
		log.Fatalf("load recordings: %v", err)
	}

	httpServer := &http.Server{Addr: addr, Handler: server.New(manager), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownCtx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	}()

	log.Printf("archiver listening on %s (data directory %s)", addr, dataDir)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("HTTP server: %v", err)
	}
}
