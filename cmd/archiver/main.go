package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterhost"
	"github.com/dltkddnr04/integrated-recorder/internal/network"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
	"github.com/dltkddnr04/integrated-recorder/internal/server"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Printf("archiver: %v", err)
		os.Exit(1)
	}
}

func run() error {
	addr := strings.TrimSpace(os.Getenv("ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "./data"
	}
	adapterDir := strings.TrimSpace(os.Getenv("ADAPTER_DIR"))
	if adapterDir == "" {
		adapterDir = "./adapters"
	}

	store, err := storage.New(dataDir)
	if err != nil {
		return fmt.Errorf("initialize storage: %w", err)
	}
	configStore, secretStore, err := pluginconfig.NewTypedFileStores(dataDir)
	if err != nil {
		return fmt.Errorf("initialize plugin settings: %w", err)
	}
	configs, err := pluginconfig.NewService(configStore, secretStore)
	if err != nil {
		return fmt.Errorf("initialize plugin settings: %w", err)
	}
	adapters, err := adapterhost.Discover(context.Background(), adapterDir, configs)
	if err != nil {
		return fmt.Errorf("discover adapters: %w", err)
	}
	defer adapters.Close()
	manager, err := acquire.NewManager(store, network.NewPublicHTTPClient(25*time.Second), adapters, nil)
	if err != nil {
		return fmt.Errorf("load recordings: %w", err)
	}

	httpServer := &http.Server{Addr: addr, Handler: server.New(manager, adapters, configs), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
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
		return fmt.Errorf("HTTP server: %w", err)
	}
	return nil
}
