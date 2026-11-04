package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// A SCIM-provisioned account signs in through KyIdentity: the row whose externalId is the
// KyIdentity subject is adopted (it stays SCIM-managed), back-channel logout reaches it, and a
// name that collides with an unrelated account gets a 409, not a 500.
func TestKyIdentitySignInAdoptsTheSCIMAccount(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	f := newFakeKySignOn(t)
	cfg.SSO.KySignOnIssuer, cfg.SSO.KySignOnClientID, cfg.SSO.AutoProvision = f.issuer, "client", true
	srv = api.NewServer(cfg, st)
	ctx := context.Background()
	for _, u := range []*store.User{
		{ID: "usr_scim", Username: "frank", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "frank", SCIMManaged: true},
		{ID: "usr_local", Username: "gina", Role: "user", Status: "active", SSOProvider: "local"},
		{ID: "usr_off", Username: "hal", Role: "user", Status: "inactive", SSOProvider: "scim", SSOSubject: "hal", SCIMManaged: true},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}

	w := ssoLogin(t, srv, f, "frank", "sid-f")
	c := sessionCookie(w)
	if w.Code != http.StatusFound || c == nil || !signedIn(srv, c) {
		t.Fatalf("SCIM user could not sign in: %d %s", w.Code, w.Body.String())
	}
	u, err := st.Users().GetUserByID(ctx, "usr_scim")
	if err != nil || u.SSOProvider != "kysignon" || !u.SCIMManaged {
		t.Fatalf("SCIM row not adopted: %+v %v", u, err)
	}
	if n, _ := st.Users().CountUsers(ctx); n != 3 {
		t.Fatalf("adoption created a second account: %d users", n)
	}
	if w := postLogout(srv, f.logoutToken(t, "frank", "")); w.Code != http.StatusOK || signedIn(srv, c) {
		t.Fatalf("back-channel logout did not reach the adopted account: %d", w.Code)
	}

	// Different subject, same name as a local account: a clear 409.
	w = ssoLogin(t, srv, f, "gina", "sid-g")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "already exists") {
		t.Fatalf("name collision: %d %s, want 409", w.Code, w.Body.String())
	}
	// A deprovisioned account is refused plainly, not with a 500.
	if w := ssoLogin(t, srv, f, "hal", "sid-h"); w.Code != http.StatusForbidden {
		t.Fatalf("inactive account: %d %s, want 403", w.Code, w.Body.String())
	}
}

// The role follows KyIdentity's per-app `roles` on every sign-in (legacy `role` when there
// are no `roles`); a change revokes the other sessions; a token with neither leaves the
// role alone.
func TestKyIdentitySignInAppliesTheMappedRole(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	f := newFakeKySignOn(t)
	cfg.SSO.KySignOnIssuer, cfg.SSO.KySignOnClientID, cfg.SSO.AutoProvision = f.issuer, "client", true
	srv = api.NewServer(cfg, st)
	ctx := context.Background()
	login := func(sid string, extra map[string]any) *http.Cookie {
		t.Helper()
		w := ssoLoginClaims(t, srv, f, "ivy", sid, time.Now(), extra)
		c := sessionCookie(w)
		if w.Code != http.StatusFound || c == nil {
			t.Fatalf("sign-in %s: %d %s", sid, w.Code, w.Body.String())
		}
		return c
	}
	role := func() string {
		t.Helper()
		u, err := st.Users().GetUserBySSO(ctx, "kysignon", "ivy")
		if err != nil {
			t.Fatal(err)
		}
		return u.Role
	}

	first := login("s1", map[string]any{"roles": []string{"editor", "admin"}})
	if role() != "admin" {
		t.Fatalf("auto-provisioned role %q, want admin from roles", role())
	}
	// An IdP-wide admin without the app's admin role is a user here.
	second := login("s2", map[string]any{"roles": []string{}, "role": "admin"})
	if role() != "user" || signedIn(srv, first) || !signedIn(srv, second) {
		t.Fatalf("demotion: role %q, old session kept %v, new session %v", role(), signedIn(srv, first), signedIn(srv, second))
	}
	// No roles claim: the legacy role applies.
	third := login("s3", map[string]any{"role": "admin"})
	if role() != "admin" || signedIn(srv, second) {
		t.Fatalf("legacy role: %q, old session kept %v", role(), signedIn(srv, second))
	}
	// Neither claim: unchanged, and no session is revoked.
	login("s4", nil)
	if role() != "admin" || !signedIn(srv, third) {
		t.Fatalf("no role claims: role %q, other session kept %v", role(), signedIn(srv, third))
	}
}
