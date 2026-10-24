package config_test

import (
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
)

// FuzzParseTrustedProxies checks KY_TRUSTED_PROXIES parsing. A refused list returns no
// prefixes. An accepted one holds only masked, unmapped prefixes narrower than the whole
// address space (a mapped ::ffff:0.0.0.0/96 is 0.0.0.0/0), and printing it back parses to
// the same list.
func FuzzParseTrustedProxies(f *testing.F) {
	for _, s := range []string{
		"", "192.0.2.0/24", "2001:db8::/64", "::ffff:192.0.2.0/120", "192.0.2.7",
		"  192.0.2.1 , 10.0.0.0/8 , 10.0.0.1/32",
		"nonsense", "192.0.2.0/33", "192.0.2.1, oops", "192.0.2.0/", "0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96",
		// Edge cases: unmasked host bits, mapped bare IPs, zones, empty fields, /1.
		"10.1.2.3/8", "::ffff:10.0.0.1", "fe80::1%eth0", ",,,", "128.0.0.0/1", "::ffff:0.0.0.0/97", "::ffff:0:0/95",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := config.ParseTrustedProxies(raw)
		if err != nil {
			if got != nil {
				t.Fatalf("ParseTrustedProxies(%q) failed (%v) but returned %v", raw, err, got)
			}
			return
		}
		fields := 0
		for _, field := range strings.Split(raw, ",") {
			if strings.TrimSpace(field) != "" {
				fields++
			}
		}
		if len(got) != fields {
			t.Fatalf("ParseTrustedProxies(%q) = %v: %d prefixes from %d entries", raw, got, len(got), fields)
		}
		printed := make([]string, len(got))
		for i, p := range got {
			switch {
			case !p.IsValid():
				t.Fatalf("ParseTrustedProxies(%q) returned an invalid prefix", raw)
			case p != p.Masked():
				t.Fatalf("ParseTrustedProxies(%q): %v is not masked", raw, p)
			case p.Bits() == 0:
				t.Fatalf("ParseTrustedProxies(%q): %v trusts every address", raw, p)
			case p.Addr().Is4In6() && p.Bits() >= 96:
				t.Fatalf("ParseTrustedProxies(%q): %v is still IPv4-mapped and matches no unmapped peer", raw, p)
			}
			printed[i] = p.String()
		}
		again, err := config.ParseTrustedProxies(strings.Join(printed, ","))
		if err != nil {
			t.Fatalf("printed list %q does not parse: %v", printed, err)
		}
		for i := range got {
			if again[i] != got[i] {
				t.Fatalf("round trip of %q: %v became %v", raw, got[i], again[i])
			}
		}
	})
}
