package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
)

// FuzzParseTrustedProxies feeds arbitrary KY_TRUSTED_PROXIES values to the parser. A rejected
// list returns no prefixes; an accepted one has one masked, unmapped, non-universal prefix per
// entry and parses back to itself from its own String form.
func FuzzParseTrustedProxies(f *testing.F) {
	for _, s := range []string{
		"", "nonsense", "192.0.2.0/33", "192.0.2.1, oops", "192.0.2.0/", "0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96",
		"  192.0.2.1 , 10.0.0.0/8 , 10.0.0.1/32", "192.0.2.0/24", "2001:db8::/64", "::ffff:192.0.2.0/120",
		"::ffff:192.0.2.7", "192.0.2.7", "fe80::1%eth0", "10.0.0.1/8", ",,", "::ffff:0:0/95", "0.0.0.0/1",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, err := config.ParseTrustedProxies(raw)
		if err != nil {
			if got != nil {
				t.Fatalf("error %v came with %v", err, got)
			}
			return
		}
		entries := 0
		for _, field := range strings.Split(raw, ",") {
			if strings.TrimSpace(field) != "" {
				entries++
			}
		}
		if len(got) != entries {
			t.Fatalf("%d entries became %d prefixes: %v", entries, len(got), got)
		}
		text := make([]string, len(got))
		for i, p := range got {
			if !p.IsValid() || p != p.Masked() || p.Bits() == 0 || p.Addr().Zone() != "" {
				t.Fatalf("prefix %d of %q is %v", i, raw, p)
			}
			if p.Addr().Is4In6() && p.Bits() >= 96 {
				t.Fatalf("prefix %d of %q stayed IPv4-mapped: %v", i, raw, p)
			}
			text[i] = p.String()
		}
		again, err := config.ParseTrustedProxies(strings.Join(text, ","))
		if err != nil || !slices.Equal(again, got) {
			t.Fatalf("%v re-parsed as %v, %v", got, again, err)
		}
	})
}
