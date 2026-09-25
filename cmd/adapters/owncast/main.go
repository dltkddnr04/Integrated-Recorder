package main

import (
	"log"
	"os"

	"github.com/dltkddnr04/integrated-recorder/internal/adapters/owncast"
)

func main() {
	if err := owncast.Serve(os.Stdin, os.Stdout); err != nil {
		log.Printf("adapter stopped: %v", err)
		os.Exit(1)
	}
}
