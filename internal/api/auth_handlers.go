package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Busnes-app/ky-primitives/password"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

type LoginRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	CaptchaToken string `json:"captcha_token,omitempty"`
}

func (s *Server) handlePoWChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	challenge, err := auth.GeneratePoWChallenge(s.config.Captcha.DifficultyPoW, s.config.Security.SessionSecret)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to generate PoW challenge")
		return
	}

	s.writeJSON(w, http.StatusOK, challenge)
}

// Per account and client: a password guess against one account from one client (IPv6 by
// /64). Keyed on both, so nobody elsewhere can lock the admin out by guessing at it.
const (
	loginAccountLimit  = 10
	loginAccountWindow = 15 * time.Minute
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	client := s.limiterIP(r)
	if !s.perIPExempt(r) && !s.allowAttempt("login:"+client, 20, time.Minute) {
		s.writeError(w, http.StatusTooManyRequests, "Too many login attempts")
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		s.writeError(w, http.StatusBadRequest, "Username and password are required")
		return
	}

	// Verify PoW CAPTCHA if enabled. A solved puzzle buys one attempt.
	if s.config.Captcha.Provider == "pow" {
		if req.CaptchaToken == "" || !auth.VerifyPoWSolution(req.CaptchaToken, s.config.Security.SessionSecret, s.powSpent) {
			s.writeError(w, http.StatusForbidden, "Security check failed. Please complete the CAPTCHA puzzle.")
			return
		}
	}

	// Checked before any password hashing. An exhausted window answers exactly like a wrong
	// password, for known and unknown names alike. The name is hashed so a key stays small.
	accountKey := "login-account:" + crypto.SHA256Hex([]byte(strings.ToLower(req.Username)))[:32] + "|" + client
	if s.attemptsExhausted(accountKey, loginAccountLimit) {
		s.writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	user, err := s.store.Users().GetUserByUsername(r.Context(), req.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeError(w, http.StatusInternalServerError, "Authentication error")
		return
	}
	ok := false
	if user == nil || user.PasswordHash == "" {
		password.DummyVerify() // an unknown or password-less name costs what a wrong password costs
	} else {
		ok, _ = password.Verify(req.Password, user.PasswordHash)
	}
	// A disabled account answers like a wrong password, so the status cannot enumerate it.
	if !ok || user.Status != "active" {
		s.allowAttempt(accountKey, loginAccountLimit, loginAccountWindow)
		s.writeError(w, http.StatusUnauthorized, "Invalid credentials")
		return
	}

	// Issue active session
	_, _, err = s.sessions.IssueSession(r.Context(), w, r, user)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to issue session")
		return
	}

	_ = s.store.Audit().LogAudit(r.Context(), &store.AuditRecord{
		UserID:   user.ID,
		Action:   "auth.login",
		Resource: "session",
	})

	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          user,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	_ = s.sessions.RevokeSession(r.Context(), w, r)
	s.writeJSON(w, http.StatusOK, map[string]bool{"logged_out": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"user":          user,
	})
}
