package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
)

// PoWChallenge represents a client-side SHA256 puzzle matching the KySecurity standard.
type PoWChallenge struct {
	Algorithm string `json:"algorithm"`
	Salt      string `json:"salt"`
	Challenge string `json:"challenge"`
	MaxNumber int    `json:"maxnumber"`
	ExpiresAt int64  `json:"expires_at"`
	Signature string `json:"signature"`
}

// PoWSolution represents the client's submitted answer.
type PoWSolution struct {
	Algorithm string `json:"algorithm"`
	Salt      string `json:"salt"`
	Challenge string `json:"challenge"`
	Number    int    `json:"number"`
	MaxNumber int    `json:"maxnumber"`
	ExpiresAt int64  `json:"expires_at"`
	Signature string `json:"signature"`
}

// GeneratePoWChallenge creates a puzzle of given difficulty (e.g. 5000 to 50000 max iterations).
func GeneratePoWChallenge(difficulty int, signingSecret string) (*PoWChallenge, error) {
	if difficulty <= 0 {
		difficulty = 20000
	}

	saltBytes := make([]byte, 16)
	if _, err := rand.Read(saltBytes); err != nil {
		return nil, err
	}
	salt := hex.EncodeToString(saltBytes)

	// Pick a random winning number in range [1, difficulty]
	nBig, err := rand.Int(rand.Reader, big.NewInt(int64(difficulty)))
	if err != nil {
		return nil, err
	}
	targetNumber := int(nBig.Int64()) + 1

	targetStr := fmt.Sprintf("%s%d", salt, targetNumber)
	h := sha256.Sum256([]byte(targetStr))
	challenge := hex.EncodeToString(h[:])

	challengeExpires := time.Now().Add(5 * time.Minute).Unix()
	signaturePayload := fmt.Sprintf("%s:%s:%d:%d", salt, challenge, difficulty, challengeExpires)
	return &PoWChallenge{
		Algorithm: "SHA-256",
		Salt:      salt,
		Challenge: challenge,
		MaxNumber: difficulty,
		ExpiresAt: challengeExpires,
		Signature: crypto.ComputeHMACSHA256([]byte(signaturePayload), signingSecret),
	}, nil
}

// powSpentCap bounds the spent-salt set. Salts are recorded only after the signature and the
// work check out, and login is rate limited per client before that, so reaching the cap
// needs many clients; at the cap a solution is accepted unrecorded rather than locking
// every sign-in out.
const powSpentCap = 100000

// PoWSpent remembers solved challenges until they expire, so one solution buys one attempt.
type PoWSpent struct {
	mu    sync.Mutex
	salts map[string]int64 // salt -> expiry (unix seconds)
}

func NewPoWSpent() *PoWSpent { return &PoWSpent{salts: make(map[string]int64)} }

// spend records salt and reports false when it was already spent.
func (p *PoWSpent) spend(salt string, expiresAt int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().Unix()
	if exp, ok := p.salts[salt]; ok && exp >= now {
		return false
	}
	if len(p.salts) >= powSpentCap {
		for s, exp := range p.salts {
			if exp < now {
				delete(p.salts, s)
			}
		}
		if len(p.salts) >= powSpentCap {
			return true
		}
	}
	p.salts[salt] = expiresAt
	return true
}

// VerifyPoWSolution decodes and validates the client's base64-encoded JSON solution. With a
// non-nil spent set, a valid solution is spent and a second use of it fails.
func VerifyPoWSolution(solutionBase64, signingSecret string, spent *PoWSpent) bool {
	data, err := base64.StdEncoding.DecodeString(solutionBase64)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(solutionBase64)
		if err != nil {
			return false
		}
	}

	var sol PoWSolution
	if err := json.Unmarshal(data, &sol); err != nil {
		return false
	}

	if sol.Algorithm != "SHA-256" || sol.Salt == "" || sol.Challenge == "" || sol.Signature == "" ||
		sol.Number < 1 || sol.MaxNumber < 1 || sol.Number > sol.MaxNumber || sol.ExpiresAt < time.Now().Unix() {
		return false
	}
	signaturePayload := sol.Salt + ":" + sol.Challenge + ":" + strconv.Itoa(sol.MaxNumber) + ":" + strconv.FormatInt(sol.ExpiresAt, 10)
	if !crypto.VerifyHMACSHA256([]byte(signaturePayload), signingSecret, sol.Signature) {
		return false
	}

	targetStr := fmt.Sprintf("%s%d", sol.Salt, sol.Number)
	h := sha256.Sum256([]byte(targetStr))
	actualChallenge := hex.EncodeToString(h[:])

	if actualChallenge != sol.Challenge {
		return false
	}
	return spent == nil || spent.spend(sol.Salt, sol.ExpiresAt)
}
