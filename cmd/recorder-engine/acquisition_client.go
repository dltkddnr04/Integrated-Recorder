//go:build !runtime_e2e

package main

import (
	"net/http"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/acquire"
	"github.com/dltkddnr04/integrated-recorder/internal/network"
)

// newAcquisitionClient preserves the normal public-network policy for all
// production Recorder Engine builds.
func newAcquisitionClient(timeout time.Duration) (*http.Client, acquire.SourceValidator) {
	return network.NewPublicHTTPClient(timeout), network.ValidatePublicURL
}
