package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"testing"
	"time"
)

func TestValidateTOTPKeyStrength(t *testing.T) {
	for _, size := range []int{1, 14, 15, 16, 20} {
		t.Run(fmt.Sprintf("%d_bytes", size), func(t *testing.T) {
			key := bytes.Repeat([]byte{0x42}, size)
			secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key)
			// Independent RFC 4226 truncation: the library's Code also refuses
			// short keys after the repair, so it cannot create this regression input.
			counter := uint64(time.Now().Unix() / 30)
			var input [8]byte
			binary.BigEndian.PutUint64(input[:], counter)
			mac := hmac.New(sha1.New, key)
			mac.Write(input[:])
			digest := mac.Sum(nil)
			offset := digest[len(digest)-1] & 15
			value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
			code := fmt.Sprintf("%06d", value%1_000_000)

			_, valid := ValidateTOTP(secret, code)
			if want := size >= 16; valid != want {
				t.Fatalf("ValidateTOTP accepted=%v, want %v for %d decoded bytes", valid, want, size)
			}
		})
	}
}
