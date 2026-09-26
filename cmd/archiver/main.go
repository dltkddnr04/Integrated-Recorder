package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
		addr = "127.0.0.1:8080"
	}
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "./data"
	}
	adapterDirsValue := strings.TrimSpace(os.Getenv("ADAPTER_DIR"))
	if adapterDirsValue == "" {
		adapterDirsValue = "./adapters"
	}
	adapterDirs := filepath.SplitList(adapterDirsValue)

	store, err := storage.New(dataDir)
	if err != nil {
		return fmt.Errorf("initialize storage: %w", err)
	}
	configStore, secretStore, stateStore, err := pluginconfig.NewTypedFileStoresAndState(dataDir)
	if err != nil {
		return fmt.Errorf("initialize plugin settings: %w", err)
	}
	configs, err := pluginconfig.NewService(configStore, secretStore)
	if err != nil {
		return fmt.Errorf("initialize plugin settings: %w", err)
	}
	adapters, err := adapterhost.DiscoverDirs(context.Background(), adapterDirs, configs, stateStore)
	if err != nil {
		return fmt.Errorf("discover adapters: %w", err)
	}
	manager, err := acquire.NewManager(store, network.NewPublicHTTPClient(25*time.Second), adapters, nil)
	if err != nil {
		adapters.Close()
		return fmt.Errorf("load recordings: %w", err)
	}
	for _, issue := range store.RecoveryIssues() {
		log.Printf("storage recovery: recording=%s code=%s: %s", issue.ID, issue.Code, issue.Message)
	}

	httpServer := &http.Server{Addr: addr, Handler: server.New(manager, adapters, configs), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()

	log.Printf("archiver listening on %s (data directory %s)", addr, dataDir)
	var runErr error
	select {
	case err = <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			runErr = fmt.Errorf("HTTP server: %w", err)
		}
	case <-shutdownCtx.Done():
	}

	// Stop new HTTP work before stopping recording workers, then close adapter
	// processes only after workers have finished any in-flight adapter call.
	shutdownHTTP, cancelHTTP := context.WithTimeout(context.Background(), 10*time.Second)
	if err = httpServer.Shutdown(shutdownHTTP); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("HTTP shutdown: %w", err))
		_ = httpServer.Close()
	}
	cancelHTTP()

	shutdownWorkers, cancelWorkers := context.WithTimeout(context.Background(), 15*time.Second)
	if err = manager.Close(shutdownWorkers); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("recording shutdown: %w", err))
	}
	cancelWorkers()
	adapters.Close()
	if runErr != nil {
		return runErr
	}
	return nil
}
