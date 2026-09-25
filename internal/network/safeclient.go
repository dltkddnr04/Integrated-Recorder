// Package network provides the bounded public-network client used for
// user-supplied stream URLs. It resolves and validates each dial target to
// reduce SSRF and DNS-rebinding exposure.
package network

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func ValidatePublicURL(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("source URL must be http or https with a host")
	}
	if isLocalHost(u.Hostname()) {
		return fmt.Errorf("private and localhost source hosts are not allowed")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		return fmt.Errorf("resolve source host: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("source host did not resolve")
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return fmt.Errorf("source host resolves to a private, loopback, or link-local address")
		}
	}
	return nil
}

func NewPublicHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, networkName, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if isLocalHost(host) {
				return nil, fmt.Errorf("refusing private source host")
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("host has no addresses")
			}
			for _, entry := range ips {
				if isPrivateIP(entry.IP) {
					return nil, fmt.Errorf("refusing private source address")
				}
			}
			// Dial a validated address directly. net/http retains the original
			// hostname for Host and TLS certificate verification.
			return (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, networkName, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" {
			return fmt.Errorf("redirect to unsupported URL")
		}
		if isLocalHost(req.URL.Hostname()) {
			return fmt.Errorf("redirect to private source host refused")
		}
		return nil
	}}
}

func isLocalHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "local" || strings.HasSuffix(host, ".local")
}
func isPrivateIP(ip net.IP) bool {
	return ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// EffectivePort is shared by diagnostics and tests.
func EffectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return strconv.Itoa(443)
	}
	return strconv.Itoa(80)
}
