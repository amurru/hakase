// s21_test.go - all-addrs SSRF choke tests (S21).
package vision

import (
	gocontext "context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// stubLookup replaces the resolver with canned answers, restoring on cleanup.
func stubLookup(t *testing.T, fn func(ctx gocontext.Context, host string) ([]net.IPAddr, error)) {
	t.Helper()
	saved := lookupIPAddr
	lookupIPAddr = fn
	t.Cleanup(func() { lookupIPAddr = saved })
}

// ipAddrs renders string IPs as net.IPAddr answers.
func ipAddrs(t *testing.T, ips ...string) []net.IPAddr {
	t.Helper()
	var out []net.IPAddr
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		out = append(out, net.IPAddr{IP: ip})
	}
	return out
}

// TestS21CheckHostPublicAllAddrs pins the all-addrs choke: a mixed answer
// set (one public + one RFC1918) is blocked, an empty set fails closed,
// and a literal public IP validates without touching the resolver.
func TestS21CheckHostPublicAllAddrs(t *testing.T) {
	called := false
	stubLookup(t, func(ctx gocontext.Context, host string) ([]net.IPAddr, error) {
		called = true
		if host == "mixed.test" {
			return ipAddrs(t, "93.184.216.34", "10.0.0.1"), nil
		}
		if host == "empty.test" {
			return nil, nil
		}
		return ipAddrs(t, "93.184.216.34"), nil
	})
	if err := CheckHostPublic("mixed.test"); err == nil {
		t.Fatal("mixed {public, 10.0.0.1} must be blocked")
	} else if !strings.Contains(err.Error(), "blocked internal address") {
		t.Fatalf("mixed-set refusal must name the guard, got: %v", err)
	}
	if err := CheckHostPublic("empty.test"); err == nil {
		t.Fatal("empty answer set must fail closed")
	}
	called = false
	if err := CheckHostPublic("93.184.216.34"); err != nil {
		t.Fatalf("literal public IP: %v", err)
	}
	if called {
		t.Fatal("literal IPs must validate without DNS")
	}
	if err := CheckHostPublic("93.184.216.34:443"); err != nil {
		t.Fatalf("host-with-port literal: %v", err)
	}
}

// TestS21GuardedDialPinsValidated pins dial-time validation: a mixed-answer
// host never reaches the dialer, and an all-public host dials the pinned
// first validated address.
func TestS21GuardedDialPinsValidated(t *testing.T) {
	stubLookup(t, func(ctx gocontext.Context, host string) ([]net.IPAddr, error) {
		return ipAddrs(t, "93.184.216.34", "10.0.0.1"), nil
	})
	savedDial := guardedDial
	dialed := ""
	guardedDial = func(ctx gocontext.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		return nil, fmt.Errorf("must not dial")
	}
	t.Cleanup(func() { guardedDial = savedDial })
	if _, err := GuardedDialContext(gocontext.Background(), "tcp", "mixed.test:80"); err == nil {
		t.Fatal("mixed-answer dial: want blocked")
	} else if !strings.Contains(err.Error(), "blocked internal address") {
		t.Fatalf("mixed-answer dial must name the guard, got: %v", err)
	}
	if dialed != "" {
		t.Fatalf("mixed-answer host must never dial the internal leg (dialed %q)", dialed)
	}

	stubLookup(t, func(ctx gocontext.Context, host string) ([]net.IPAddr, error) {
		return ipAddrs(t, "93.184.216.34", "8.8.8.8"), nil
	})
	c1, c2 := net.Pipe()
	guardedDial = func(ctx gocontext.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		return c1, nil
	}
	defer c2.Close()
	conn, err := GuardedDialContext(gocontext.Background(), "tcp", "public.test:443")
	if err != nil {
		t.Fatalf("all-public dial: %v", err)
	}
	conn.Close()
	if dialed != net.JoinHostPort("93.184.216.34", "443") {
		t.Fatalf("all-public host must dial the pinned first address, dialed %q", dialed)
	}
}

// TestS21RedirectHopRechecked pins per-hop redirect re-checking: a hop onto
// an internal host is stopped, and over-long chains are refused.
func TestS21RedirectHopRechecked(t *testing.T) {
	c := NewGuardedClient(DownloadTimeout)
	if c.Timeout != 30*time.Second {
		t.Fatalf("guarded client timeout = %v, want 30s", c.Timeout)
	}
	mkReq := func(raw string) *http.Request {
		req, err := http.NewRequest("GET", raw, nil)
		if err != nil {
			t.Fatalf("mkreq: %v", err)
		}
		return req
	}
	via := []*http.Request{mkReq("http://public.test/a")}
	if err := c.CheckRedirect(mkReq("http://127.0.0.1/evil"), via); err == nil {
		t.Fatal("redirect onto loopback: want blocked")
	} else if !strings.Contains(err.Error(), "redirect target blocked") {
		t.Fatalf("rebound refusal must name the guard, got: %v", err)
	}
	long := make([]*http.Request, 5)
	for i := range long {
		long[i] = mkReq("http://public.test/a")
	}
	if err := c.CheckRedirect(mkReq("http://public.test/b"), long); err == nil {
		t.Fatal("6-hop chain: want too-many-redirects")
	}
}
