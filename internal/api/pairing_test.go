package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

func pairVerify(t *testing.T, srv *api.Server, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/devices/pair/verify", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:1000"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

// pair/verify is anonymous and mints a full session, so a six-digit code would be an
// account takeover by brute force. Only the whole QR secret redeems a pairing.
func TestPairVerifyAcceptsOnlyTheSecret(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	ctx := context.Background()
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_boss", Username: "boss", Role: "admin", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	parentAuth := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	secret := crypto.RandomHex(24)
	if err := st.Devices().CreatePairing(ctx, &store.DevicePairing{
		Secret: secret, UserID: "usr_boss", Status: "pending", AuthenticatedAt: parentAuth,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(90 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	for _, guess := range []string{"123456", secret[:6], secret[:47]} {
		w := pairVerify(t, srv, map[string]string{"secret": guess, "platform": "pwa"})
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "session_token") ||
			!strings.Contains(w.Body.String(), "pairing not found or expired") {
			t.Fatalf("guess %q: got %d %s", guess, w.Code, w.Body.String())
		}
	}
	if w := pairVerify(t, srv, map[string]string{"secret": secret, "device_name": strings.Repeat("x", 256)}); w.Code != http.StatusBadRequest {
		t.Fatalf("oversized device name: got %d %s", w.Code, w.Body.String())
	}

	w := pairVerify(t, srv, map[string]string{"secret": secret, "platform": "pwa"})
	var paired struct {
		Token string `json:"session_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &paired); err != nil || w.Code != http.StatusOK || paired.Token == "" {
		t.Fatalf("real secret: got %d %s", w.Code, w.Body.String())
	}
	// Pairing verifies no credentials: the derived session is only as fresh as its parent.
	sess, err := st.Sessions().GetSession(ctx, crypto.SHA256Hex([]byte(paired.Token)))
	if err != nil || !sess.CreatedAt.Equal(parentAuth) {
		t.Fatalf("derived session created_at = %v (err %v), want the parent's %v", sess, err, parentAuth)
	}
	if again := pairVerify(t, srv, map[string]string{"secret": secret, "platform": "pwa"}); again.Code != http.StatusBadRequest {
		t.Fatalf("replay: got %d %s", again.Code, again.Body.String())
	}

	poll := httptest.NewRecorder()
	srv.ServeHTTP(poll, httptest.NewRequest("GET", "/api/devices/pair/poll?secret="+secret, nil))
	if !strings.Contains(poll.Body.String(), `"consumed"`) {
		t.Fatalf("poll after pairing: %s", poll.Body.String())
	}
	records, _, err := st.Audit().ListAuditRecords(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	audited := false
	for _, r := range records {
		audited = audited || (r.Action == "device.paired" && r.UserID == "usr_boss")
	}
	if !audited {
		t.Fatal("pairing was not audited")
	}
}

// pair/init records the initiating session's credential time and is limited per account.
func TestPairInitInheritsSessionTimeAndIsLimited(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	cookie := loginAs(t, srv, st, "alice", "user")
	parent, err := st.Sessions().GetSession(context.Background(), crypto.SHA256Hex([]byte(cookie.Value)))
	if err != nil {
		t.Fatal(err)
	}

	w := do(t, srv, "POST", "/api/devices/pair/init", cookie)
	var res struct{ Secret, Code string }
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != http.StatusOK || res.Code != "" {
		t.Fatalf("pair init: %d %s", w.Code, w.Body.String())
	}
	p, err := st.Devices().GetPairingBySecret(context.Background(), res.Secret)
	if err != nil || !p.AuthenticatedAt.Equal(parent.CreatedAt) {
		t.Fatalf("pairing authenticated_at = %v (err %v), want %v", p, err, parent.CreatedAt)
	}

	for i := 2; i <= 10; i++ {
		if w := do(t, srv, "POST", "/api/devices/pair/init", cookie); w.Code != http.StatusOK {
			t.Fatalf("init %d: got %d", i, w.Code)
		}
	}
	if w := do(t, srv, "POST", "/api/devices/pair/init", cookie); w.Code != http.StatusTooManyRequests {
		t.Fatalf("init 11: got %d, want 429", w.Code)
	}
}
