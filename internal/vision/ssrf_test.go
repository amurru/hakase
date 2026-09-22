package vision

import (
	"context"
	"net/netip"
	"testing"
)

// TestCheckIPPublic pins the address classes the SSRF guards reject; all
// cases are literal IPs so the test never touches the network.
func TestCheckIPPublic(t *testing.T) {
	cases := []struct {
		ip    string
		block bool
	}{
		{"203.0.113.10", false},         // public unicast (TEST-NET-3 is routed documentation space, acceptable as "not internal")
		{"8.8.8.8", false},              // public
		{"2606:4700:4700::1111", false}, // public v6
		{"10.1.2.3", true},              // RFC1918 private
		{"172.16.0.9", true},            // RFC1918 private
		{"192.168.1.1", true},           // RFC1918 private
		{"127.0.0.1", true},             // loopback
		{"::1", true},                   // v6 loopback
		{"169.254.169.254", true},       // link-local (cloud metadata)
		{"fe80::1", true},               // v6 link-local
		{"0.0.0.0", true},               // unspecified
		{"224.0.0.1", true},             // multicast
		{"ff02::1", true},               // v6 multicast
		{"100.64.0.1", true},            // CGNAT shared range
		{"100.127.255.254", true},       // CGNAT upper bound
		{"100.128.0.1", false},          // above CGNAT range (public)
		{"::ffff:127.0.0.1", true},      // IPv4-mapped loopback must not sneak past as v6
		{"::ffff:10.0.0.5", true},       // IPv4-mapped private
	}
	for _, tc := range cases {
		err := CheckIPPublic(netip.MustParseAddr(tc.ip))
		if (err != nil) != tc.block {
			t.Errorf("CheckIPPublic(%s): err=%v, want block=%v", tc.ip, err, tc.block)
		}
	}
}

// TestGuardedDialContextBlocksInternalLiterals verifies the dial-time guard
// rejects internal literal-IP targets before any connection attempt (the
// error must come from validation, not from the network).
func TestGuardedDialContextBlocksInternalLiterals(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "10.0.0.1:443", "169.254.169.254:80", "[::1]:9000", "100.64.0.1:80"} {
		conn, err := GuardedDialContext(context.Background(), "tcp", addr)
		if err == nil {
			if conn != nil {
				conn.Close()
			}
			t.Errorf("GuardedDialContext(%q): expected rejection, got a connection", addr)
			continue
		}
		if conn != nil {
			t.Errorf("GuardedDialContext(%q): returned a conn alongside an error", addr)
		}
	}
}
