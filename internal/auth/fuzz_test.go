package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
)

// FuzzClientIP feeds arbitrary peers, allowlists and X-Forwarded-For lines (split on newlines)
// to ClientIP. A header can only change the answer when the peer is a trusted proxy, and then
// only to a canonical, untrusted address; X-Real-IP never matters, and an entry the caller
// prepends cannot displace an address a trusted hop already vouched for.
func FuzzClientIP(f *testing.F) {
	for _, tc := range []struct{ trusted, peer, xff string }{
		{"", "203.0.113.5:41000", "1.2.3.4"},
		{"192.0.2.0/24", "192.0.2.7:41000", "198.51.100.9, 192.0.2.8"},
		{"192.0.2.0/24", "192.0.2.7:41000", "10.9.9.9, 198.51.100.9, 192.0.2.8"},
		{"192.0.2.0/24", "192.0.2.7:41000", "192.0.2.8, 192.0.2.9"},
		{"192.0.2.0/24", "192.0.2.7:41000", "not-an-ip"},
		{"2001:db8::/64", "[2001:db8::1]:41000", "2001:db8:1::5, 2001:db8::2"},
		{"", "[::ffff:203.0.113.5]:41000", ""},
		{"192.0.2.0/24", "192.0.2.7:41000", "9.9.9.9\n198.51.100.9"},
		{"::ffff:192.0.2.0/120", "192.0.2.7:41000", "198.51.100.9"},
		{"192.0.2.7", "192.0.2.8:41000", "198.51.100.9"},
		{"192.0.2.0/24", "192.0.2.7:41000", "1.2.3.4:56789"},
		{"192.0.2.0/24", "192.0.2.7:41000", "[2001:db8::1]:443"},
		{"192.0.2.0/24", "192.0.2.7:41000", "198.51.100.9, 192.0.2.8:41000"},
		{"192.0.2.0/24", "192.0.2.7:41000", "1.2.3.4:abc"},
		{"192.0.2.0/24", "192.0.2.7:41000", "::ffff:1.2.3.4, fe80::1%eth0"},
		{"192.0.2.0/24", "192.0.2.7", "[::ffff:192.0.2.9]:1"},
	} {
		f.Add(tc.trusted, tc.peer, tc.xff)
	}
	f.Fuzz(func(t *testing.T, trustedRaw, peer, xff string) {
		trusted, err := config.ParseTrustedProxies(trustedRaw)
		if err != nil {
			return
		}
		request := func(lines ...string) string {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = peer
			r.Header.Set("X-Real-IP", "198.18.0.1")
			for _, line := range lines {
				r.Header.Add("X-Forwarded-For", line)
			}
			return auth.ClientIP(r, trusted)
		}
		lines := strings.Split(xff, "\n")
		base, got := request(), request(lines...)
		if got == base {
			return
		}
		inList := func(a netip.Addr) bool {
			for _, p := range trusted {
				if p.Contains(a) {
					return true
				}
			}
			return false
		}
		if peerAddr, err := netip.ParseAddr(base); err != nil || !inList(peerAddr) {
			t.Fatalf("untrusted peer %q let the header answer %q", base, got)
		}
		addr, err := netip.ParseAddr(got)
		if err != nil || addr.String() != got || addr.Is4In6() || inList(addr) {
			t.Fatalf("header answer %q is not a canonical untrusted address", got)
		}
		if spoofed := request(append([]string{"203.0.113.66, garbage"}, lines...)...); spoofed != got {
			t.Fatalf("a prepended entry moved %q to %q", got, spoofed)
		}
	})
}

// FuzzVerifyPoWSolution feeds arbitrary submissions to VerifyPoWSolution against one fixed,
// genuinely signed challenge. Whatever the encoding, an accepted submission decodes to exactly
// the issued challenge and its winning number.
func FuzzVerifyPoWSolution(f *testing.F) {
	const secret = "test-signing-secret"
	// Fixed rather than GeneratePoWChallenge, so every fuzz worker checks the same challenge.
	salt, number, maxNumber, expires := "00112233445566778899aabbccddeeff", 7, 50, int64(4102444800) // 2100-01-01
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s%d", salt, number)))
	want := auth.PoWSolution{Algorithm: "SHA-256", Salt: salt, Challenge: hex.EncodeToString(sum[:]), Number: number, MaxNumber: maxNumber, ExpiresAt: expires}
	want.Signature = crypto.ComputeHMACSHA256([]byte(fmt.Sprintf("%s:%s:%d:%d", salt, want.Challenge, maxNumber, expires)), secret)
	genuine, _ := json.Marshal(want)
	if !auth.VerifyPoWSolution(base64.StdEncoding.EncodeToString(genuine), secret) {
		f.Fatal("the fixed challenge no longer verifies; the signing payload changed")
	}
	wrongNumber := want
	wrongNumber.Number = 8
	forged := auth.PoWSolution{Algorithm: "SHA-256", Salt: "mine", Challenge: "fake", Number: 1, MaxNumber: 1, ExpiresAt: expires, Signature: want.Signature}
	for _, s := range []auth.PoWSolution{wrongNumber, forged} {
		b, _ := json.Marshal(s)
		f.Add(base64.StdEncoding.EncodeToString(b))
	}
	f.Add(base64.StdEncoding.EncodeToString(genuine))
	f.Add(base64.RawStdEncoding.EncodeToString(genuine))
	f.Add(string(genuine))
	f.Add("")
	f.Add("e30=")
	f.Fuzz(func(t *testing.T, submission string) {
		if !auth.VerifyPoWSolution(submission, secret) {
			return
		}
		data, err := base64.StdEncoding.DecodeString(submission)
		if err != nil {
			data, err = base64.RawStdEncoding.DecodeString(submission)
		}
		var got auth.PoWSolution
		if err != nil || json.Unmarshal(data, &got) != nil {
			t.Fatalf("accepted %q, which does not decode", submission)
		}
		if got != want {
			t.Fatalf("accepted %+v, issued %+v", got, want)
		}
	})
}
