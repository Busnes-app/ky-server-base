package scim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/scim"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func scopedSCIM(t *testing.T, allowAdmin bool) (store.Store, func(method, path string, body any) *httptest.ResponseRecorder) {
	t.Helper()
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := scim.NewServer(st, config.SCIMConfig{Enabled: true, BearerToken: "tok", AllowAdmin: allowAdmin}, "http://localhost:8080")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	h := srv.AuthMiddleware(mux)
	return st, func(method, path string, body any) *httptest.ResponseRecorder {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/scim+json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
}

// SCIM sees and changes only the accounts it created: the local bootstrap admin and
// KyIdentity accounts are invisible to it, and it cannot grant admin unless allowed.
func TestSCIMIsScopedToTheAccountsItManages(t *testing.T) {
	st, do := scopedSCIM(t, false)
	ctx := context.Background()
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_admin", Username: "admin", Role: "admin", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/scim/v2/Users/usr_admin"},
		{"PUT", "/scim/v2/Users/usr_admin"},
		{"PATCH", "/scim/v2/Users/usr_admin"},
		{"DELETE", "/scim/v2/Users/usr_admin"},
	} {
		var body any
		switch c.method {
		case "PUT":
			body = map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "admin", "active": false}
		case "PATCH":
			body = map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []any{map[string]any{"op": "replace", "path": "active", "value": false}}}
		}
		if w := do(c.method, c.path, body); w.Code != http.StatusNotFound {
			t.Errorf("%s %s on a local account: %d %s, want 404", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_admin"); u == nil || u.Status != "active" {
		t.Fatal("SCIM changed the local admin")
	}
	if w := do("GET", "/scim/v2/Users", nil); strings.Contains(w.Body.String(), "usr_admin") {
		t.Fatalf("list exposes the local admin: %s", w.Body.String())
	}
	if w := do("GET", `/scim/v2/Users?filter=userName%20eq%20%22admin%22`, nil); strings.Contains(w.Body.String(), "usr_admin") {
		t.Fatalf("filter exposes the local admin: %s", w.Body.String())
	}

	// Admin grants are refused; ordinary accounts are created and managed.
	admin := map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "mallory", "roles": []any{map[string]any{"value": "admin"}}}
	if w := do("POST", "/scim/v2/Users", admin); w.Code != http.StatusBadRequest {
		t.Fatalf("admin create: %d %s, want 400", w.Code, w.Body.String())
	}
	w := do("POST", "/scim/v2/Users", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "ivan"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	promote := map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}, "Operations": []any{map[string]any{"op": "replace", "path": "roles", "value": []any{map[string]any{"value": "admin"}}}}}
	if w := do("PATCH", "/scim/v2/Users/"+created.ID, promote); w.Code != http.StatusBadRequest {
		t.Fatalf("admin grant by PATCH: %d %s, want 400", w.Code, w.Body.String())
	}
	if w := do("GET", "/scim/v2/Users/"+created.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("own account: %d", w.Code)
	}
	// A local account cannot be pulled into a SCIM group either.
	group := map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "g", "members": []any{map[string]any{"value": "usr_admin"}}}
	if w := do("POST", "/scim/v2/Groups", group); w.Code != http.StatusBadRequest {
		t.Fatalf("group with a local member: %d %s, want 400", w.Code, w.Body.String())
	}
}

func TestSCIMAdminGrantWhenAllowed(t *testing.T) {
	_, do := scopedSCIM(t, true)
	admin := map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "root2", "roles": []any{map[string]any{"value": "admin"}}}
	if w := do("POST", "/scim/v2/Users", admin); w.Code != http.StatusCreated {
		t.Fatalf("KY_SCIM_ALLOW_ADMIN=true admin create: %d %s", w.Code, w.Body.String())
	}
}
