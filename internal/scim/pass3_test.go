package scim_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/scim"
)

var patchSchema = []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"}

// An IdP that empties a user's roles takes the role away: a PUT with empty roles and a
// replace with an empty value both leave "user"; a PUT that omits roles leaves it alone.
func TestSCIMEmptyRoleReplacementDemotes(t *testing.T) {
	st, do := scopedSCIM(t, true)
	ctx := context.Background()
	admin := map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "ada", "roles": []any{map[string]any{"value": "admin"}}}
	w := do("POST", "/scim/v2/Users", admin)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body)
	}
	u, err := st.Users().GetUserByUsername(ctx, "ada")
	if err != nil || u.Role != "admin" {
		t.Fatal(u, err)
	}
	if w := do("PATCH", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": patchSchema, "Operations": []any{map[string]any{"op": "replace", "path": "roles", "value": []any{}}}}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if u, _ := st.Users().GetUserByID(ctx, u.ID); u.Role != "user" {
		t.Fatalf("an empty roles replace kept role %q", u.Role)
	}
	if w := do("PATCH", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": patchSchema, "Operations": []any{map[string]any{"op": "replace", "path": "roles", "value": []any{map[string]any{"value": "admin"}}}}}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if w := do("PUT", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "ada", "active": true}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if u, _ := st.Users().GetUserByID(ctx, u.ID); u.Role != "admin" {
		t.Fatalf("a PUT that omits roles changed the role to %q", u.Role)
	}
	if w := do("PUT", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "ada", "active": true, "roles": []any{}}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if u, _ := st.Users().GetUserByID(ctx, u.ID); u.Role != "user" {
		t.Fatalf("a PUT with empty roles kept role %q", u.Role)
	}
}

// externalId set after creation is stored while the row is still SCIM's own, so the user's
// first KyIdentity sign-in can adopt it; an adopted row keeps the subject it signs in with.
func TestSCIMExternalIDCanBeCorrectedBeforeAdoption(t *testing.T) {
	st, do := scopedSCIM(t, false)
	ctx := context.Background()
	if w := do("POST", "/scim/v2/Users", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "alice"}); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body)
	}
	u, _ := st.Users().GetUserByUsername(ctx, "alice")
	if w := do("PATCH", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": patchSchema, "Operations": []any{map[string]any{"op": "replace", "path": "externalId", "value": "sub-alice"}}}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if got, err := st.Users().GetUserBySSO(ctx, "scim", "sub-alice"); err != nil || got.ID != u.ID {
		t.Fatalf("externalId PATCH not stored: %v %v", got, err)
	}
	if w := do("PUT", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "alice", "externalId": "sub-alice-2"}); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body)
	}
	if got, err := st.Users().GetUserBySSO(ctx, "scim", "sub-alice-2"); err != nil || got.ID != u.ID {
		t.Fatalf("externalId PUT not stored: %v %v", got, err)
	}
	if err := st.Users().LinkSSOIdentity(ctx, u.ID, "sub-alice-2", "scim", "kysignon"); err != nil {
		t.Fatal(err)
	}
	if w := do("PATCH", "/scim/v2/Users/"+u.ID, map[string]any{"schemas": patchSchema, "Operations": []any{map[string]any{"op": "replace", "path": "externalId", "value": "someone-else"}}}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"externalId":"sub-alice-2"`) {
		t.Fatal(w.Code, w.Body)
	}
	if got, err := st.Users().GetUserBySSO(ctx, "kysignon", "sub-alice-2"); err != nil || got.ID != u.ID {
		t.Fatalf("an adopted row was relinked: %v %v", got, err)
	}
}

// Renaming a group onto a taken name is a SCIM uniqueness conflict, not a 500.
func TestSCIMGroupRenameOntoTakenNameIs409(t *testing.T) {
	_, do := scopedSCIM(t, false)
	for _, name := range []string{"Sales", "Ops"} {
		if w := do("POST", "/scim/v2/Groups", map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": name}); w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := do("GET", `/scim/v2/Groups?filter=displayName%20eq%20%22Ops%22`, nil)
	id := extractFirstID(t, w)
	if w := do("PATCH", "/scim/v2/Groups/"+id, map[string]any{"schemas": patchSchema, "Operations": []any{map[string]any{"op": "replace", "path": "displayName", "value": "Sales"}}}); w.Code != http.StatusConflict {
		t.Fatal(w.Code, w.Body)
	}
}

// Concurrent incremental PATCHes on one group keep every member they add.
func TestSCIMConcurrentGroupAddsKeepEveryMember(t *testing.T) {
	st, do := scopedSCIM(t, false)
	ctx := context.Background()
	var ids []string
	for _, name := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8"} {
		if w := do("POST", "/scim/v2/Users", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": name}); w.Code != http.StatusCreated {
			t.Fatal(w.Code, w.Body)
		}
		u, _ := st.Users().GetUserByUsername(ctx, name)
		ids = append(ids, u.ID)
	}
	if w := do("POST", "/scim/v2/Groups", map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "G"}); w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body)
	}
	gid := extractFirstID(t, do("GET", `/scim/v2/Groups?filter=displayName%20eq%20%22G%22`, nil))
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := do("PATCH", "/scim/v2/Groups/"+gid, map[string]any{"schemas": patchSchema, "Operations": []any{map[string]any{"op": "add", "path": "members", "value": []any{map[string]any{"value": id}}}}}); w.Code != http.StatusOK {
				t.Error(w.Code, w.Body)
			}
		}()
	}
	wg.Wait()
	g, err := st.Groups().GetGroupByID(ctx, gid)
	if err != nil || len(g.Members) != len(ids) {
		t.Fatalf("members after concurrent adds: %v %v", g, err)
	}
}

func extractFirstID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var page struct {
		Resources []struct{ ID string } `json:"Resources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Resources) != 1 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	return page.Resources[0].ID
}
