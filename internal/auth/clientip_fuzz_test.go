package auth_test

import (
	"net"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/config"
)

// fuzzTrusted is used whenever the fuzzed allowlist does not parse, so every input still
// reaches the X-Forwarded-For walk.
const fuzzTrusted = "192.0.2.0/24, 2001:db8::/64"

// forwarded reads one X-Forwarded-For entry the way the contract describes it: a bare IP, or
// ip:port with the port dropped, IPv4-mapped addresses unmapped.
func forwarded(entry string) (netip.Addr, bool) {
	entry = strings.TrimSpace(entry)
	if addr, err := netip.ParseAddr(entry); err == nil {
		return addr.Unmap(), true
	}
	host, _, err := net.SplitHostPort(entry)
	if err != nil {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(host)
	return addr.Unmap(), err == nil
}

// FuzzClientIP feeds caller-controlled X-Forwarded-For lines and peer addresses through
// ClientIP. The answer is either the peer, or an untrusted entry of the chain with only
// parseable trusted entries to its right: anything else lets a caller pick its own
// rate-limit bucket.
func FuzzClientIP(f *testing.F) {
	for _, s := range [][4]string{
		{"", "203.0.113.5:41000", "1.2.3.4", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "198.51.100.9, 192.0.2.8", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "10.9.9.9, 198.51.100.9, 192.0.2.8", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "192.0.2.8, 192.0.2.9", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "not-an-ip", ""},
		{"2001:db8::/64", "[2001:db8::1]:41000", "2001:db8:1::5, 2001:db8::2", ""},
		{"", "[::ffff:203.0.113.5]:41000", "", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "9.9.9.9", "198.51.100.9"},
		{"::ffff:192.0.2.0/120", "192.0.2.7:41000", "198.51.100.9", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "1.2.3.4:56789", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "[2001:db8::1]:443", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "198.51.100.9, 192.0.2.8:41000", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "1.2.3.4:abc", ""},
		// Edge cases: empty entries, zones, mapped entries, a peer with no port.
		{"192.0.2.0/24", "192.0.2.7:41000", ",,", ","},
		{"192.0.2.0/24", "192.0.2.7", "fe80::1%eth0", ""},
		{"192.0.2.0/24", "[::ffff:192.0.2.7]:1", "::ffff:198.51.100.9, ::ffff:192.0.2.8", ""},
		{"192.0.2.0/24", "", "1.2.3.4", ""},
	} {
		f.Add(s[0], s[1], s[2], s[3])
	}
	f.Fuzz(func(t *testing.T, rawTrusted, peer, xff1, xff2 string) {
		trusted, err := config.ParseTrustedProxies(rawTrusted)
		if err != nil {
			trusted, _ = config.ParseTrustedProxies(fuzzTrusted)
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		r.Header.Add("X-Forwarded-For", xff1)
		if xff2 != "" {
			r.Header.Add("X-Forwarded-For", xff2)
		}

		got := auth.ClientIP(r, trusted)
		if again := auth.ClientIP(r, trusted); again != got {
			t.Fatalf("ClientIP is not deterministic: %q then %q", got, again)
		}
		want := auth.PeerIP(r)
		if !auth.TrustedPeer(r, trusted) {
			if got != want {
				t.Fatalf("untrusted peer %q: ClientIP = %q, want the peer %q", peer, got, want)
			}
			return
		}
		if got == want {
			return
		}

		addr, err := netip.ParseAddr(got)
		if err != nil || addr.Is4In6() {
			t.Fatalf("ClientIP = %q is not an unmapped address", got)
		}
		parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, ok := forwarded(parts[i])
			if !ok {
				t.Fatalf("ClientIP = %q, but the walk should have stopped at %q", got, parts[i])
			}
			trustedHop := false
			for _, p := range trusted {
				trustedHop = trustedHop || p.Contains(a)
			}
			if !trustedHop {
				if a.String() != got {
					t.Fatalf("ClientIP = %q, want the rightmost untrusted entry %q", got, a)
				}
				return
			}
		}
		t.Fatalf("ClientIP = %q is in no untrusted entry of %q", got, parts)
	})
}
