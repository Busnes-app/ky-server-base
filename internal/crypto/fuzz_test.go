package crypto_test

import (
	"bytes"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
)

// FuzzAESGCM round-trips arbitrary plaintext and feeds arbitrary strings to DecryptAESGCM. A
// string that is not a genuine ciphertext under the key returns an error and no bytes; the
// only thing that can decrypt under sealKey is the one message sealed under it.
func FuzzAESGCM(f *testing.F) {
	key := bytes.Repeat([]byte{0x43}, 32)
	sealKey := bytes.Repeat([]byte{0x42}, 32)
	// EncryptAESGCM([]byte("totp secret"), 0x42 x 32), fixed so every fuzz worker agrees on it.
	const sealed = "XAi4ZZNMz6_2SSmgpa2hMQgxH2OdKgfwqLVjGS7mUij6KO6DPSIf"
	if got, err := crypto.DecryptAESGCM(sealed, sealKey); err != nil || string(got) != "totp secret" {
		f.Fatalf("fixed ciphertext no longer opens: %q, %v", got, err)
	}
	for _, s := range []string{sealed, sealed + "A", sealed[:len(sealed)-1], sealed[:16], "", "!!!!", "XAi4ZZNMz6_2SSmgpa2hMQgx", "a=="} {
		f.Add([]byte("totp secret"), s)
	}
	f.Add([]byte{}, "")
	f.Fuzz(func(t *testing.T, plaintext []byte, forged string) {
		enc, err := crypto.EncryptAESGCM(plaintext, key)
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		if dec, err := crypto.DecryptAESGCM(enc, key); err != nil || !bytes.Equal(dec, plaintext) {
			t.Fatalf("round trip: %q, %v", dec, err)
		}
		if dec, err := crypto.DecryptAESGCM(enc, sealKey); err == nil || dec != nil {
			t.Fatalf("wrong key opened ciphertext: %q, %v", dec, err)
		}
		dec, err := crypto.DecryptAESGCM(forged, sealKey)
		if err != nil {
			if dec != nil {
				t.Fatalf("error %v came with %q", err, dec)
			}
			return
		}
		if string(dec) != "totp secret" {
			t.Fatalf("forged %q opened to %q", forged, dec)
		}
	})
}
