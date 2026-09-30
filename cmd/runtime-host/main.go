// Command runtime-host is PID 1 in the production image. It owns the stable
// external listener and supervises separate Control and Recorder Engine
// processes from the immutable image-bundled application release.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/dltkddnr04/integrated-recorder/internal/runtimehost/bootstrap"
)

func main() {
	config, err := bootstrap.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.Printf("runtime-host: invalid startup configuration: %v", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := bootstrap.Run(ctx, config); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("runtime-host: %v", err)
		os.Exit(1)
	}
}
