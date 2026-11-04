package scim_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/scim"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// SCIM creating someone who already signed in through KyIdentity takes that account over
// (201 with its id) instead of a permanent 409: by externalId = KyIdentity subject first,
// by userName only when no externalId is sent. Local accounts and a mismatched externalId
// still get 409, and the take-over cannot grant admin.
func TestSCIMCreateTakesOverTheKyIdentityAccount(t *testing.T) {
	st, do := scopedSCIM(t, false)
	ctx := context.Background()
	for _, u := range []*store.User{
		{ID: "usr_ann", Username: "ann", Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: "sub-ann"},
		{ID: "usr_bob", Username: "bob", Role: "admin", Status: "active", SSOProvider: "kysignon", SSOSubject: "sub-bob"},
		{ID: "usr_cat", Username: "cat", Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: "sub-cat"},
		{ID: "usr_dan", Username: "dan", Role: "user", Status: "active", SSOProvider: "local"},
		{ID: "usr_eve", Username: "eve", Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: "sub-eve"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	create := func(body map[string]any) (int, string) {
		t.Helper()
		body["schemas"] = []string{scim.SchemaUser}
		w := do("POST", "/scim/v2/Users", body)
		if w.Code != http.StatusCreated {
			return w.Code, ""
		}
		var out struct{ ID string }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return w.Code, out.ID
	}
	managed := func(id string) *store.User {
		t.Helper()
		u, err := st.Users().GetUserByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	// externalId match, under a new name: the same row, renamed, still a KyIdentity row.
	if code, id := create(map[string]any{"userName": "ann.k", "externalId": "sub-ann", "displayName": "Ann K"}); code != http.StatusCreated || id != "usr_ann" {
		t.Fatalf("externalId take-over: %d %q", code, id)
	}
	if u := managed("usr_ann"); !u.SCIMManaged || u.Username != "ann.k" || u.SSOProvider != "kysignon" || u.SSOSubject != "sub-ann" {
		t.Fatalf("taken-over row: %+v", u)
	}
	// Already SCIM's: a second create is a uniqueness conflict.
	if code, _ := create(map[string]any{"userName": "ann.k", "externalId": "sub-ann"}); code != http.StatusConflict {
		t.Fatalf("second create: %d, want 409", code)
	}
	// userName match without externalId; roles omitted keep the role.
	if code, id := create(map[string]any{"userName": "bob"}); code != http.StatusCreated || id != "usr_bob" {
		t.Fatalf("userName take-over: %d %q", code, id)
	}
	if u := managed("usr_bob"); !u.SCIMManaged || u.Role != "admin" {
		t.Fatalf("userName take-over row: %+v", u)
	}
	// A different externalId under the same name is someone else.
	if code, _ := create(map[string]any{"userName": "cat", "externalId": "sub-other"}); code != http.StatusConflict || managed("usr_cat").SCIMManaged {
		t.Fatalf("mismatched externalId: %d", code)
	}
	// Local accounts are never taken over.
	if code, _ := create(map[string]any{"userName": "dan"}); code != http.StatusConflict || managed("usr_dan").SCIMManaged {
		t.Fatalf("local account: %d", code)
	}
	// The take-over cannot grant admin with KY_SCIM_ALLOW_ADMIN off, and changes nothing.
	if code, _ := create(map[string]any{"userName": "eve", "externalId": "sub-eve", "roles": []any{map[string]any{"value": "admin"}}}); code != http.StatusBadRequest || managed("usr_eve").SCIMManaged {
		t.Fatalf("admin grant: %d", code)
	}
}
