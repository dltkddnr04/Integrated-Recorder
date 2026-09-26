package network

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	for _, tc := range []struct {
		ip      string
		private bool
	}{
		{ip: "127.0.0.1", private: true},
		{ip: "10.1.2.3", private: true},
		{ip: "172.16.0.1", private: true},
		{ip: "192.168.1.1", private: true},
		{ip: "169.254.1.1", private: true},
		{ip: "100.64.0.1", private: true},
		{ip: "100.127.255.254", private: true},
		{ip: "0.0.0.0", private: true},
		{ip: "224.0.0.1", private: true},
		{ip: "::", private: true},
		{ip: "::1", private: true},
		{ip: "fe80::1", private: true},
		{ip: "fec0::1", private: true},
		{ip: "febf:ffff::1", private: true},
		{ip: "ff02::1", private: true},
		{ip: "::ffff:192.168.1.2", private: true},
		{ip: "192.0.2.7", private: true},
		{ip: "198.18.0.1", private: true},
		{ip: "203.0.113.9", private: true},
		{ip: "2001:db8::1", private: true},
		{ip: "2002:c000:0201::1", private: true},
		{ip: "64:ff9b::c000:201", private: true},
		{ip: "8.8.8.8", private: false},
		{ip: "2606:4700:4700::1111", private: false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			if got := isPrivateIP(net.ParseIP(tc.ip)); got != tc.private {
				t.Fatalf("isPrivateIP(%s) = %v, want %v", tc.ip, got, tc.private)
			}
		})
	}
	if !isPrivateIP(nil) {
		t.Fatal("nil IP was not rejected")
	}
}

func TestValidatePublicURLRejectsLocalNamesAndInvalidPorts(t *testing.T) {
	resolver := ipLookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	})
	for _, host := range []string{"localhost", "node.localhost", "localhost..", "printer.local", "service.internal", "nas.home.arpa"} {
		err := validatePublicURL(context.Background(), "https://"+host+"/stream", resolver)
		if err == nil {
			t.Errorf("local name %q was accepted", host)
		}
	}
	for _, raw := range []string{
		"https://example.test:0/stream",
		"https://example.test:65536/stream",
		"https://example.test:abc/stream",
		"https://example.test:/stream",
	} {
		if err := validatePublicURL(context.Background(), raw, resolver); err == nil {
			t.Errorf("invalid port URL %q was accepted", raw)
		}
	}
}

func TestValidatePublicURLValidatesAllAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []net.IPAddr
		want bool
	}{
		{name: "one public", ips: ips("8.8.8.8"), want: true},
		{name: "public ipv6", ips: ips("2606:4700:4700::1111"), want: true},
		{name: "mixed public private", ips: append(ips("8.8.8.8"), ips("10.0.0.2")...), want: false},
		{name: "cgnat", ips: ips("100.64.0.1"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := ipLookupFunc(func(context.Context, string) ([]net.IPAddr, error) { return tc.ips, nil })
			err := validatePublicURL(context.Background(), "https://example.test/stream", resolver)
			if (err == nil) != tc.want {
				t.Fatalf("validate error = %v, wantSuccess=%v", err, tc.want)
			}
		})
	}
}

func TestPublicHTTPClientRetriesValidatedAddresses(t *testing.T) {
	resolver := ipLookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return ips("8.8.8.8", "1.1.1.1"), nil
	})
	var attempts []string
	dial := func(_ context.Context, network, addr string) (net.Conn, error) {
		attempts = append(attempts, network+" "+addr)
		if len(attempts) == 1 {
			return nil, errors.New("first address unavailable")
		}
		client, server := net.Pipe()
		go server.Close()
		return client, nil
	}
	client := newPublicHTTPClient(time.Second, resolver, dial)
	transport := client.Transport.(*http.Transport)
	conn, err := transport.DialContext(context.Background(), "tcp", "example.test:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	want := []string{"tcp 8.8.8.8:443", "tcp 1.1.1.1:443"}
	if !reflect.DeepEqual(attempts, want) {
		t.Fatalf("dial attempts = %#v, want %#v", attempts, want)
	}
}

func TestPublicHTTPClientRevalidatesDNSOnEveryDial(t *testing.T) {
	var lookups atomic.Int32
	resolver := ipLookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		if lookups.Add(1) == 1 {
			return ips("8.8.8.8"), nil
		}
		return ips("127.0.0.1"), nil
	})
	var dials atomic.Int32
	dial := func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		client, server := net.Pipe()
		go server.Close()
		return client, nil
	}
	client := newPublicHTTPClient(time.Second, resolver, dial)
	transport := client.Transport.(*http.Transport)
	first, err := transport.DialContext(context.Background(), "tcp", "example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	if _, err := transport.DialContext(context.Background(), "tcp", "example.test:80"); err == nil {
		t.Fatal("second private DNS answer was accepted")
	}
	if dials.Load() != 1 || lookups.Load() != 2 {
		t.Fatalf("lookups=%d dials=%d", lookups.Load(), dials.Load())
	}
}

func TestPublicHTTPClientRejectsUnsafeAnswerBeforeFallback(t *testing.T) {
	resolver := ipLookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return ips("8.8.8.8", "192.168.0.5"), nil
	})
	var dials atomic.Int32
	client := newPublicHTTPClient(time.Second, resolver, func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected dial")
	})
	transport := client.Transport.(*http.Transport)
	if _, err := transport.DialContext(context.Background(), "tcp", "example.test:80"); err == nil {
		t.Fatal("mixed public/private answer was accepted")
	}
	if dials.Load() != 0 {
		t.Fatalf("dialed %d times despite unsafe DNS answer", dials.Load())
	}
}

func TestRedirectPolicyRejectsUnsafeHostAndInvalidPort(t *testing.T) {
	resolver := ipLookupFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "private.test" {
			return ips("10.0.0.1"), nil
		}
		return ips("8.8.8.8"), nil
	})
	client := newPublicHTTPClient(time.Second, resolver, nil)
	for _, raw := range []string{"http://private.test/next", "http://public.test:0/next"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		req := &http.Request{URL: u, Header: make(http.Header)}
		if err := client.CheckRedirect(req, []*http.Request{{}}); err == nil {
			t.Errorf("redirect to %q was accepted", raw)
		}
	}
}

func TestValidatePublicURLErrorsDoNotEchoURL(t *testing.T) {
	resolver := ipLookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("lookup failed")
	})
	raw := "https://example.test/stream?token=secret-token"
	err := validatePublicURL(context.Background(), raw, resolver)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("unsafe validation error = %v", err)
	}
}

func ips(values ...string) []net.IPAddr {
	result := make([]net.IPAddr, 0, len(values))
	for _, value := range values {
		result = append(result, net.IPAddr{IP: net.ParseIP(value)})
	}
	return result
}
