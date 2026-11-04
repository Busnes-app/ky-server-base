package scim_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/scim"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// SCIM creating someone who already signed in through KyIdentity takes that account over
// (201 with its id) instead of a permanent 409: by externalId = KyIdentity subject first,
// by userName when no account has that externalId. Local accounts still get 409, a refused
// take-over changes nothing, and the take-over cannot grant admin.
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
	// userName match without externalId; an existing admin's admin role is no grant.
	if code, id := create(map[string]any{"userName": "bob", "roles": []any{map[string]any{"value": "admin"}}}); code != http.StatusCreated || id != "usr_bob" {
		t.Fatalf("userName take-over: %d %q", code, id)
	}
	if u := managed("usr_bob"); !u.SCIMManaged || u.Role != "admin" {
		t.Fatalf("userName take-over row: %+v", u)
	}
	// An unmatched externalId falls back to userName (owner decision Q13 B).
	if code, id := create(map[string]any{"userName": "cat", "externalId": "sub-other"}); code != http.StatusCreated || id != "usr_cat" || managed("usr_cat").SSOSubject != "sub-cat" {
		t.Fatalf("userName take-over with another externalId: %d %q", code, id)
	}
	// The externalId of an account SCIM already manages, under a new name: 409, no second account.
	if code, _ := create(map[string]any{"userName": "ann.other", "externalId": "sub-ann"}); code != http.StatusConflict {
		t.Fatalf("managed externalId under a new name: %d, want 409", code)
	}
	// A take-over the PUT rules refuse (renaming onto a taken name) changes nothing.
	if code, _ := create(map[string]any{"userName": "dan", "externalId": "sub-eve"}); code != http.StatusConflict || managed("usr_eve").SCIMManaged || managed("usr_eve").Username != "eve" {
		t.Fatalf("refused take-over: %d %+v", code, managed("usr_eve"))
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

// Removing externalId unlinks the account, so a KyIdentity sign-in under the old subject can
// no longer adopt it (it answered 200 and kept the link).
func TestSCIMExternalIDRemoveUnlinks(t *testing.T) {
	st, do := scopedSCIM(t, false)
	ctx := context.Background()
	if w := do("POST", "/scim/v2/Users", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "fay", "externalId": "sub-fay"}); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body)
	}
	u, _ := st.Users().GetUserByUsername(ctx, "fay")
	if w := do("PATCH", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": patchSchema, "Operations": []any{map[string]any{"op": "remove", "path": "externalId"}}}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := st.Users().GetUserBySSO(ctx, "scim", "sub-fay"); err == nil {
		t.Fatal("externalId remove kept the link")
	}
}

// Concurrent PATCHes of different attributes of one user both take effect.
func TestSCIMConcurrentUserPatchesKeepEveryChange(t *testing.T) {
	st, do := scopedSCIM(t, false)
	ctx := context.Background()
	if w := do("POST", "/scim/v2/Users", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "gus"}); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body)
	}
	u, _ := st.Users().GetUserByUsername(ctx, "gus")
	const n = 40
	op := func(i int, attr string) map[string]any {
		if attr == "emails" {
			return map[string]any{"op": "replace", "path": "emails", "value": []any{map[string]any{"value": fmt.Sprintf("gus%d@example.com", i)}}}
		}
		return map[string]any{"op": "replace", "path": "displayName", "value": fmt.Sprintf("Gus %d", i)}
	}
	var wg sync.WaitGroup
	for _, attr := range []string{"displayName", "emails"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range n {
				if w := do("PATCH", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": patchSchema, "Operations": []any{op(i, attr)}}); w.Code != http.StatusOK {
					t.Error(w.Code, w.Body)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got, _ := st.Users().GetUserByID(ctx, u.ID); got.DisplayName != fmt.Sprintf("Gus %d", n-1) || got.Email != fmt.Sprintf("gus%d@example.com", n-1) {
		t.Fatalf("a concurrent PATCH was undone: %q %q", got.DisplayName, got.Email)
	}
}
