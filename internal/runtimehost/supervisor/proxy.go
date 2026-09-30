package supervisor

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type fixedProxy struct {
	target    url.URL
	transport *http.Transport
}

func makeProxy(target *url.URL) *fixedProxy {
	copyTarget := *target
	return &fixedProxy{
		target: copyTarget,
		transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       60 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			DisableCompression:    true,
		},
	}
}

func (p *fixedProxy) ServeHTTP(w http.ResponseWriter, incoming *http.Request) {
	request := incoming.Clone(incoming.Context())
	target := p.target
	target.Path = incoming.URL.Path
	target.RawPath = incoming.URL.RawPath
	target.RawQuery = incoming.URL.RawQuery
	target.Fragment = ""
	request.URL = &target
	request.RequestURI = ""
	request.Host = target.Host
	request.Header = incoming.Header.Clone()
	removeHopHeaders(request.Header)
	request.Header.Del("Forwarded")
	request.Header.Del("X-Forwarded-For")
	request.Header.Del("X-Forwarded-Host")
	request.Header.Del("X-Forwarded-Proto")

	response, err := p.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "control plane unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for name, values := range response.Header {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	removeHopHeaders(w.Header())
	w.WriteHeader(response.StatusCode)
	copyResponse(w, response.Body)
	for name, values := range response.Trailer {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
}

func copyResponse(w http.ResponseWriter, body io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

func removeHopHeaders(header http.Header) {
	for _, connection := range header.Values("Connection") {
		for _, token := range strings.Split(connection, ",") {
			header.Del(strings.TrimSpace(token))
		}
	}
	for _, name := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		header.Del(name)
	}
}
