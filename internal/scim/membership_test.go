package scim_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

const patchOp = "urn:ietf:params:scim:api:messages:2.0:PatchOp"

func scimCall(t *testing.T, h http.Handler, token, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/scim+json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func scimUser(t *testing.T, h http.Handler, token, userName string) string {
	t.Helper()
	code, out := scimCall(t, h, token, "POST", "/scim/v2/Users", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": userName, "active": true,
	})
	if code != http.StatusCreated {
		t.Fatalf("create %s: %d %v", userName, code, out)
	}
	return out["id"].(string)
}

func groupMembers(t *testing.T, h http.Handler, token, id string) []string {
	t.Helper()
	code, out := scimCall(t, h, token, "GET", "/scim/v2/Groups/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("get group: %d %v", code, out)
	}
	var ids []string
	members, _ := out["members"].([]any)
	for _, m := range members {
		ids = append(ids, m.(map[string]any)["value"].(string))
	}
	slices.Sort(ids)
	return ids
}

func sorted(ids ...string) []string { slices.Sort(ids); return ids }

// An IdP looks a user up by userName before creating it; a substring match would hand it
// somebody else's id, and every later update or deprovisioning would land on that account.
func TestSCIMUserNameFilterIsExact(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	scimUser(t, h, token, "joann@corp.com")

	code, out := scimCall(t, h, token, "GET", `/scim/v2/Users?filter=userName%20eq%20%22ann@corp.com%22`, nil)
	if code != http.StatusOK || out["totalResults"].(float64) != 0 {
		t.Fatalf("ann@corp.com matched another user: %d %v", code, out)
	}
	code, out = scimCall(t, h, token, "GET", `/scim/v2/Users?filter=userName%20eq%20%22JoAnn@corp.com%22`, nil)
	if code != http.StatusOK || out["totalResults"].(float64) != 1 {
		t.Fatalf("case-insensitive exact match missing: %d %v", code, out)
	}
}

func TestSCIMGroupPatchAddsAndRemovesMembers(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	a, b, c := scimUser(t, h, token, "a"), scimUser(t, h, token, "b"), scimUser(t, h, token, "c")
	_, g := scimCall(t, h, token, "POST", "/scim/v2/Groups", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "G",
		"members": []any{map[string]any{"value": a}, map[string]any{"value": b}},
	})
	gid := g["id"].(string)

	patch := func(ops ...map[string]any) int {
		code, out := scimCall(t, h, token, "PATCH", "/scim/v2/Groups/"+gid, map[string]any{"schemas": []string{patchOp}, "Operations": ops})
		if code != http.StatusOK {
			t.Logf("patch: %d %v", code, out)
		}
		return code
	}
	if patch(map[string]any{"op": "add", "path": "members", "value": []any{map[string]any{"value": c}}}) != http.StatusOK {
		t.Fatal("add failed")
	}
	if got := groupMembers(t, h, token, gid); !slices.Equal(got, sorted(a, b, c)) {
		t.Fatalf("add replaced the membership: %v", got)
	}
	if patch(map[string]any{"op": "remove", "path": `members[value eq "` + c + `"]`}) != http.StatusOK {
		t.Fatal("filtered remove failed")
	}
	if patch(map[string]any{"op": "remove", "path": "members", "value": []any{map[string]any{"value": a}}}) != http.StatusOK {
		t.Fatal("valued remove failed")
	}
	if got := groupMembers(t, h, token, gid); !slices.Equal(got, []string{b}) {
		t.Fatalf("remove left %v, want [%s]", got, b)
	}
}

func TestSCIMGroupReplaceWithUnknownMemberChangesNothing(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	a, b := scimUser(t, h, token, "a"), scimUser(t, h, token, "b")
	_, g := scimCall(t, h, token, "POST", "/scim/v2/Groups", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "G",
		"members": []any{map[string]any{"value": a}, map[string]any{"value": b}},
	})
	gid := g["id"].(string)

	code, _ := scimCall(t, h, token, "PUT", "/scim/v2/Groups/"+gid, map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "Renamed",
		"members": []any{map[string]any{"value": a}, map[string]any{"value": "usr_missing"}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("unknown member got %d, want 400", code)
	}
	if got := groupMembers(t, h, token, gid); !slices.Equal(got, sorted(a, b)) {
		t.Fatalf("failed replace changed membership to %v", got)
	}
}

func TestSCIMUserPatchFilteredEmailPath(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	id := scimUser(t, h, token, "mail")
	// A filtered replace on an absent email selects nothing (noTarget); add creates it.
	if code, out := scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, map[string]any{"schemas": []string{patchOp}, "Operations": []any{
		map[string]any{"op": "replace", "path": `emails[type eq "work"].value`, "value": "new@corp.com"},
	}}); code != http.StatusBadRequest || out["scimType"] != "noTarget" {
		t.Fatalf("replace on absent email: %d %v", code, out)
	}
	code, out := scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, map[string]any{"schemas": []string{patchOp}, "Operations": []any{
		map[string]any{"op": "add", "path": `emails[type eq "work"].value`, "value": "new@corp.com"},
		map[string]any{"op": "replace", "path": `emails[type eq "work"].type`, "value": "work"},
	}})
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, out)
	}
	email := func(out map[string]any) any {
		emails, _ := out["emails"].([]any)
		if len(emails) != 1 {
			return nil
		}
		return emails[0].(map[string]any)["value"]
	}
	if email(out) != "new@corp.com" {
		t.Fatalf("email not updated: %v", out["emails"])
	}
	// A filter that does not select the stored (work) email is noTarget and changes nothing.
	code, out = scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, map[string]any{"schemas": []string{patchOp}, "Operations": []any{
		map[string]any{"op": "replace", "path": `emails[type eq "home"].value`, "value": "home@corp.com"},
	}})
	if code != http.StatusBadRequest {
		t.Fatalf("home selector: %d %v", code, out)
	}
	if _, out = scimCall(t, h, token, "GET", "/scim/v2/Users/"+id, nil); email(out) != "new@corp.com" {
		t.Fatalf("home selector overwrote the work email: %v", out["emails"])
	}
	// Replacing the selected complex value keeps its value member.
	code, out = scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, map[string]any{"schemas": []string{patchOp}, "Operations": []any{
		map[string]any{"op": "replace", "path": `emails[type eq "work"]`, "value": map[string]any{"value": "third@corp.com", "type": "work"}},
	}})
	if code != http.StatusOK || email(out) != "third@corp.com" {
		t.Fatalf("complex replace: %d %v", code, out["emails"])
	}
}

func TestSCIMGroupPatchExternalID(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	_, g := scimCall(t, h, token, "POST", "/scim/v2/Groups", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "G", "externalId": "old",
	})
	code, out := scimCall(t, h, token, "PATCH", "/scim/v2/Groups/"+g["id"].(string), map[string]any{"schemas": []string{patchOp}, "Operations": []any{
		map[string]any{"op": "replace", "path": "externalId", "value": "new"},
	}})
	if code != http.StatusOK || out["externalId"] != "new" {
		t.Fatalf("externalId not updated: %d %v", code, out)
	}
}

func TestSCIMUserPatchFilteredRolePath(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	id := scimUser(t, h, token, "roles")
	code, out := scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, map[string]any{"schemas": []string{patchOp}, "Operations": []any{
		map[string]any{"op": "replace", "path": `roles[primary eq "True"].value`, "value": "manager"},
	}})
	roles, _ := out["roles"].([]any)
	if code != http.StatusOK || len(roles) != 1 || roles[0].(map[string]any)["value"] != "manager" {
		t.Fatalf("role not updated: %d %v", code, out)
	}
}

// Blank or wildcard values must not turn an equality filter into a whole-directory read.
func TestSCIMFilterRefusesBlankAndMatchesWildcardsLiterally(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	scimUser(t, h, token, "someone")
	if code, _ := scimCall(t, h, token, "GET", `/scim/v2/Users?filter=userName%20eq%20%22%20%22`, nil); code != http.StatusBadRequest {
		t.Fatalf("blank value got %d, want 400", code)
	}
	code, out := scimCall(t, h, token, "GET", `/scim/v2/Users?filter=userName%20eq%20%22%25%22`, nil)
	if code != http.StatusOK || out["totalResults"].(float64) != 0 {
		t.Fatalf("%% matched as a wildcard: %d %v", code, out)
	}
}
