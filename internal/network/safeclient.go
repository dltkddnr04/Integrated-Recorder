// Package network provides the bounded public-network client used for
// user-supplied stream URLs. It resolves and validates every dial target to
// reduce SSRF and DNS-rebinding exposure.
package network

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type ipLookupFunc func(context.Context, string) ([]net.IPAddr, error)

var deniedSpecialPrefixes = mustPrefixes(
	"0.0.0.0/8",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.88.99.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"64:ff9b::/96",
	"64:ff9b:1::/48",
	"100::/64",
	"100:0:0:1::/64",
	"2001::/23",
	"2001:db8::/32",
	"2002::/16",
	"fec0::/10",
	"3fff::/20",
	"5f00::/16",
)

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, _ := netip.ParsePrefix(value) // These prefixes are source constants, not user input.
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

func (f ipLookupFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

var _ ipResolver = (*net.Resolver)(nil)

func ValidatePublicURL(ctx context.Context, raw string) error {
	return validatePublicURL(ctx, raw, net.DefaultResolver)
}

func validatePublicURL(ctx context.Context, raw string, resolver ipResolver) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("source URL must be http or https with a host")
	}
	if err := validatePort(u); err != nil {
		return err
	}
	return validateHost(ctx, u.Hostname(), resolver)
}

func validateHost(ctx context.Context, host string, resolver ipResolver) error {
	if isLocalHost(host) {
		return fmt.Errorf("private and localhost source hosts are not allowed")
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve source host")
	}
	if len(ips) == 0 {
		return fmt.Errorf("source host did not resolve")
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return fmt.Errorf("source host resolves to a non-public address")
		}
	}
	return nil
}

func NewPublicHTTPClient(timeout time.Duration) *http.Client {
	return newPublicHTTPClient(timeout, net.DefaultResolver, (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext)
}

// newPublicHTTPClient keeps resolver and dial injection package-private so
// tests can exercise rebinding and address fallback without weakening the
// production client or connecting to public infrastructure.
func newPublicHTTPClient(timeout time.Duration, resolver ipResolver, dial func(context.Context, string, string) (net.Conn, error)) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dial == nil {
		dial = (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, ResponseHeaderTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, networkName, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid dial address")
			}
			portNum, err := strconv.Atoi(port)
			if err != nil || portNum < 1 || portNum > 65535 {
				return nil, fmt.Errorf("invalid destination port")
			}
			if isLocalHost(host) {
				return nil, fmt.Errorf("refusing private source host")
			}
			ips, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("resolve source host")
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("host has no addresses")
			}
			// Fail closed when any answer is unsafe. Do not let a public address
			// in a mixed answer authorize a private alternate address.
			for _, entry := range ips {
				if isPrivateIP(entry.IP) {
					return nil, fmt.Errorf("refusing non-public source address")
				}
			}
			var lastErr error
			for _, entry := range ips {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				// The URL host remains untouched in net/http, so Host and TLS
				// verification still use the caller's hostname. Only the socket
				// destination is pinned to this freshly validated answer.
				address := net.JoinHostPort(entry.IP.String(), port)
				conn, dialErr := dial(ctx, networkName, address)
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no usable source addresses")
			}
			return nil, lastErr
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.Host == "" || req.URL.User != nil {
			return fmt.Errorf("redirect to unsupported URL")
		}
		if err := validatePort(req.URL); err != nil {
			return fmt.Errorf("redirect to unsupported destination")
		}
		if err := validateHost(req.Context(), req.URL.Hostname(), resolver); err != nil {
			return fmt.Errorf("redirect to non-public destination")
		}
		return nil
	}}
}

func validatePort(u *url.URL) error {
	port := u.Port()
	if port == "" {
		if hasExplicitEmptyPort(u.Host) {
			return fmt.Errorf("source URL has an invalid port")
		}
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("source URL has an invalid port")
	}
	return nil
}

func hasExplicitEmptyPort(hostport string) bool {
	if strings.HasPrefix(hostport, "[") {
		end := strings.LastIndexByte(hostport, ']')
		return end >= 0 && hostport[end+1:] == ":"
	}
	return strings.HasSuffix(hostport, ":")
}

func isLocalHost(host string) bool {
	host = strings.TrimRight(strings.ToLower(host), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		host == "local" || strings.HasSuffix(host, ".local") ||
		host == "internal" || strings.HasSuffix(host, ".internal") || host == "home.arpa" || strings.HasSuffix(host, ".home.arpa")
}

func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast() {
		return true
	}
	if netip.MustParsePrefix("100.64.0.0/10").Contains(addr) {
		return true
	}
	for _, prefix := range deniedSpecialPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
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
