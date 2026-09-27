package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/adapterhost"
	"github.com/dltkddnr04/integrated-recorder/internal/authn"
	"github.com/dltkddnr04/integrated-recorder/internal/derivative"
	"github.com/dltkddnr04/integrated-recorder/internal/integrity"
	"github.com/dltkddnr04/integrated-recorder/internal/management"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
	"github.com/dltkddnr04/integrated-recorder/internal/server"
	"github.com/dltkddnr04/integrated-recorder/internal/storage"
	"github.com/dltkddnr04/integrated-recorder/internal/systemsettings"
)

const segmentPayload = "integrated-recorder-browser-e2e-source-segment"

func main() {
	if err := run(); err != nil {
		log.Printf("browser e2e backend: %v", err)
		os.Exit(1)
	}
}

func run() error {
	root := repositoryRoot()
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		return errors.New("DATA_DIR is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	adapterDir := filepath.Join(dataDir, "adapters")
	if err := os.MkdirAll(adapterDir, 0o700); err != nil {
		return err
	}
	for _, build := range []struct {
		name string
		pkg  string
	}{
		{name: "integrated-recorder-adapter-owncast", pkg: "./cmd/adapters/owncast"},
		{name: "integrated-recorder-adapter-workflow-fixture", pkg: "./web/e2e/fixture_adapter"},
	} {
		command := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(adapterDir, build.name), build.pkg)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("build %s: %w\n%s", build.name, err, output)
		}
	}

	source := http.Server{Handler: http.HandlerFunc(sourceHandler)}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() { _ = source.Serve(listener) }()
	defer source.Close()
	sourceURL := "http://" + listener.Addr().String()
	if err = os.WriteFile(filepath.Join(dataDir, "e2e-source-url"), []byte(sourceURL), 0o600); err != nil {
		return err
	}
	store, err := storage.New(dataDir)
	if err != nil {
		return err
	}
	configStore, secretStore, stateStore, err := pluginconfig.NewTypedFileStoresAndState(dataDir)
	if err != nil {
		return err
	}
	configs, err := pluginconfig.NewService(configStore, secretStore)
	if err != nil {
		return err
	}
	adapters, err := adapterhost.DiscoverDirs(context.Background(), []string{adapterDir}, configs, stateStore)
	if err != nil {
		return err
	}
	validateFixtureOrigin := func(_ context.Context, raw string) error {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u.User != nil || u.Scheme != "http" || u.Host != listener.Addr().String() {
			return errors.New("fixture source origin is not allowed")
		}
		return nil
	}
	manager, err := acquire.NewManager(store, &http.Client{Timeout: 25 * time.Second}, adapters, validateFixtureOrigin)
	if err != nil {
		adapters.Close()
		return err
	}
	products, err := management.Open(dataDir)
	if err != nil {
		_ = manager.Close(context.Background())
		adapters.Close()
		return err
	}
	settings, err := systemsettings.Open(dataDir)
	if err != nil {
		_ = manager.Close(context.Background())
		adapters.Close()
		return err
	}
	integrityService, err := integrity.Open(dataDir, store, settings.IntegrityConcurrency())
	if err != nil {
		_ = manager.Close(context.Background())
		adapters.Close()
		return err
	}
	// The export service requires an absolute, resolvable path. A directory is
	// intentionally not an executable, so the test reports FFmpeg unavailable
	// deterministically even on developers' machines that have FFmpeg installed.
	disabledFFmpegPath := filepath.Join(dataDir, "ffmpeg-disabled")
	if err := os.MkdirAll(disabledFFmpegPath, 0o700); err != nil {
		_ = manager.Close(context.Background())
		adapters.Close()
		return err
	}
	exportService, err := derivative.Open(dataDir, store, disabledFFmpegPath, 1)
	if err != nil {
		_ = integrityService.Close(context.Background())
		_ = manager.Close(context.Background())
		adapters.Close()
		return err
	}
	auth, err := authn.Open(dataDir)
	if err != nil {
		_ = exportService.Close(context.Background())
		_ = integrityService.Close(context.Background())
		_ = manager.Close(context.Background())
		adapters.Close()
		return err
	}
	api := server.NewWithOptions(manager, adapters, configs, server.Options{
		Management: products, Integrity: integrityService, Derivatives: exportService,
		Auth: auth, Settings: settings, InitialIntegrityConcurrency: settings.IntegrityConcurrency(),
		StartedAt: time.Now().UTC(), Version: "browser-e2e", Commit: "test-fixture",
	})

	addr := strings.TrimSpace(os.Getenv("ADDR"))
	if addr == "" {
		addr = "127.0.0.1:4173"
	}
	web := &http.Server{Addr: addr, Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- web.ListenAndServe() }()
	log.Printf("browser e2e API listening on %s", addr)

	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err = <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			_ = closeServices(manager, integrityService, exportService, adapters)
			return err
		}
	case <-shutdown.Done():
	}
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = web.Shutdown(ctx)
	if err = manager.Close(ctx); err != nil {
		return err
	}
	if err = exportService.Close(ctx); err != nil {
		return err
	}
	if err = integrityService.Close(ctx); err != nil {
		return err
	}
	adapters.Close()
	return nil
}

func closeServices(manager *acquire.Manager, integrityService *integrity.Service, exportService *derivative.Service, adapters *adapterhost.Host) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defer adapters.Close()
	return errors.Join(manager.Close(ctx), exportService.Close(ctx), integrityService.Close(ctx))
}

func repositoryRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}

func sourceHandler(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/hls/stream.m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:7\n#EXTINF:1.5,browser fixture\nsegment-7.ts\n")
	case "/hls/segment-7.ts":
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = fmt.Fprint(w, segmentPayload)
	default:
		http.NotFound(w, r)
	}
}
