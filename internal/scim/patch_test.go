package scim_test

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
)

const groupSchema = "urn:ietf:params:scim:schemas:core:2.0:Group"

func patchBody(ops ...map[string]any) map[string]any {
	return map[string]any{"schemas": []string{patchOp}, "Operations": ops}
}

// Group PATCH applies each operation by type, in the shapes Entra ID and Okta send.
func TestSCIMGroupPatchOperations(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	a, b, c, d := scimUser(t, h, token, "a"), scimUser(t, h, token, "b"), scimUser(t, h, token, "c"), scimUser(t, h, token, "d")
	_, g := scimCall(t, h, token, "POST", "/scim/v2/Groups", map[string]any{
		"schemas": []string{groupSchema}, "displayName": "G",
		"members": []any{map[string]any{"value": a}, map[string]any{"value": b}},
	})
	gid := g["id"].(string)
	patch := func(want int, ops ...map[string]any) map[string]any {
		t.Helper()
		code, out := scimCall(t, h, token, "PATCH", "/scim/v2/Groups/"+gid, patchBody(ops...))
		if code != want {
			t.Fatalf("patch %v: %d %v, want %d", ops, code, out, want)
		}
		return out
	}
	expect := func(step string, want ...string) {
		t.Helper()
		if got := groupMembers(t, h, token, gid); !slices.Equal(got, sorted(want...)) {
			t.Fatalf("%s: members %v, want %v", step, got, sorted(want...))
		}
	}

	patch(http.StatusOK, map[string]any{"op": "Add", "path": "members", "value": []any{map[string]any{"value": c}}})
	expect("add keeps existing members", a, b, c)
	patch(http.StatusOK, map[string]any{"op": "Remove", "path": "members", "value": []any{map[string]any{"value": b}}})
	expect("remove with a value list", a, c)
	patch(http.StatusOK, map[string]any{"op": "remove", "path": `members[value eq "` + c + `"]`})
	expect("remove members[value eq]", a)

	// Member sub-attributes and other member filters are refused, never a membership change.
	patch(http.StatusOK, map[string]any{"op": "add", "path": "members", "value": []any{map[string]any{"value": b}}})
	for _, op := range []map[string]any{
		{"op": "remove", "path": "members.display"},
		{"op": "remove", "path": `members[value eq "` + a + `"].display`},
		{"op": "replace", "path": `members[value eq "` + a + `"].display`, "value": "x"},
		{"op": "remove", "path": `members[display eq "x"]`},
		{"op": "add", "path": `members[value eq "` + d + `"]`, "value": map[string]any{"value": d}},
		{"op": "replace", "value": map[string]any{"members.display": "x"}},
	} {
		if out := patch(http.StatusBadRequest, map[string]any{"op": "replace", "path": "displayName", "value": "Renamed"}, op); out["scimType"] != "invalidPath" {
			t.Fatalf("%v: scimType %v, want invalidPath", op, out["scimType"])
		}
		expect("refused member path", a, b)
	}
	if _, out := scimCall(t, h, token, "GET", "/scim/v2/Groups/"+gid, nil); out["displayName"] != "G" {
		t.Fatalf("a refused PATCH applied its other operations: %v", out["displayName"])
	}

	patch(http.StatusOK, map[string]any{"op": "Replace", "path": "members", "value": []any{map[string]any{"value": c}, map[string]any{"value": d}}})
	expect("replace members", c, d)
	out := patch(http.StatusOK, map[string]any{"op": "Replace", "value": map[string]any{"id": gid, "displayName": "Sales", "externalId": "ext-1"}})
	if out["displayName"] != "Sales" || out["externalId"] != "ext-1" {
		t.Fatalf("pathless replace: %v", out)
	}
	expect("pathless replace keeps members", c, d)
	patch(http.StatusBadRequest, map[string]any{"op": "add", "path": "members", "value": []any{map[string]any{"value": "usr_missing"}}})
	expect("unknown member", c, d)
	patch(http.StatusOK, map[string]any{"op": "remove", "path": "members"})
	expect("remove bare members clears")
}

func TestSCIMGroupFilter(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	ids := map[string]string{}
	for _, g := range []struct{ name, ext string }{{"Sales", "ext-sales"}, {"Engineering", "ext-eng"}, {"Support", ""}} {
		code, out := scimCall(t, h, token, "POST", "/scim/v2/Groups", map[string]any{"schemas": []string{groupSchema}, "displayName": g.name, "externalId": g.ext})
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", g.name, code, out)
		}
		ids[g.name] = out["id"].(string)
	}
	list := func(filter string) (int, map[string]any, []string) {
		t.Helper()
		code, out := scimCall(t, h, token, "GET", "/scim/v2/Groups?filter="+url.QueryEscape(filter), nil)
		var got []string
		resources, _ := out["Resources"].([]any)
		for _, r := range resources {
			got = append(got, r.(map[string]any)["id"].(string))
		}
		return code, out, got
	}
	for _, c := range []struct {
		filter string
		want   []string
	}{
		{`displayName eq "Sales"`, []string{ids["Sales"]}},
		{`displayName eq "sALES"`, []string{ids["Sales"]}},
		{`displayName eq "Marketing"`, nil},
		{`displayName eq "Sal"`, nil},
		{`externalId eq "ext-eng"`, []string{ids["Engineering"]}},
		{`externalId eq "EXT-ENG"`, nil},
	} {
		code, out, got := list(c.filter)
		if code != http.StatusOK || !slices.Equal(got, c.want) || out["totalResults"].(float64) != float64(len(c.want)) {
			t.Errorf("%s: %d %v, want %v", c.filter, code, out, c.want)
		}
	}
	for _, f := range []string{`displayName co "Sa"`, `displayName sw "S"`, `displayName eq "Sales" or displayName eq "Support"`, `id eq "` + ids["Sales"] + `"`} {
		if code, out, _ := list(f); code != http.StatusBadRequest || out["scimType"] != "invalidFilter" {
			t.Errorf("%s: %d %v, want 400 invalidFilter", f, code, out)
		}
	}
	if code, out, got := list(""); code != http.StatusOK || len(got) != 3 {
		t.Errorf("unfiltered list: %d %v", code, out)
	}
}

// The library answers POST .search with 501 because neither handler is a ResourceSearcher.
func TestSCIMSearchIsNotImplemented(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	for _, p := range []string{"/scim/v2/Users/.search", "/scim/v2/Groups/.search"} {
		body := map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:SearchRequest"}, "filter": `displayName eq "x"`}
		if code, out := scimCall(t, h, token, "POST", p, body); code != http.StatusNotImplemented {
			t.Errorf("POST %s: %d %v, want 501", p, code, out)
		}
	}
}

func TestSCIMUserPatchFilteredAndRemovePaths(t *testing.T) {
	srv, mux, token := setupSCIMServer(t)
	h := srv.AuthMiddleware(mux)
	code, u := scimCall(t, h, token, "POST", "/scim/v2/Users", map[string]any{
		"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:User"}, "userName": "pat@corp.test", "displayName": "Pat",
		"emails": []any{map[string]any{"value": "old@corp.test", "type": "work", "primary": true}},
		"roles":  []any{map[string]any{"value": "manager"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, u)
	}
	id := u["id"].(string)
	patch := func(want int, ops ...map[string]any) map[string]any {
		t.Helper()
		code, out := scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, patchBody(ops...))
		if code != want {
			t.Fatalf("patch %v: %d %v, want %d", ops, code, out, want)
		}
		_, out = scimCall(t, h, token, "GET", "/scim/v2/Users/"+id, nil)
		return out
	}
	first := func(out map[string]any, attr string) any {
		values, _ := out[attr].([]any)
		if len(values) != 1 {
			return nil
		}
		return values[0].(map[string]any)["value"]
	}

	out := patch(http.StatusOK, map[string]any{"op": "Replace", "path": `emails[type eq "work"].value`, "value": "new@corp.test"})
	if first(out, "emails") != "new@corp.test" {
		t.Fatalf("filtered email replace: %v", out["emails"])
	}
	out = patch(http.StatusOK, map[string]any{"op": "replace", "path": `roles[primary eq "True"].value`, "value": "auditor"})
	if first(out, "roles") != "auditor" {
		t.Fatalf("filtered role replace: %v", out["roles"])
	}
	// A selector that misses the stored email is noTarget and fails the whole PATCH.
	code, bad := scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, patchBody(
		map[string]any{"op": "replace", "path": "displayName", "value": "Changed"},
		map[string]any{"op": "replace", "path": `emails[type eq "home"].value`, "value": "home@corp.test"},
	))
	if code != http.StatusBadRequest || bad["scimType"] != "noTarget" {
		t.Fatalf("home selector: %d %v, want 400 noTarget", code, bad)
	}

	// Entra's default mapping writes attributes this adapter does not store; they are ignored.
	before := patch(http.StatusOK, map[string]any{"op": "replace", "path": "active", "value": true})
	after := patch(http.StatusOK,
		map[string]any{"op": "Replace", "path": `phoneNumbers[type eq "work"].value`, "value": "+1 555 0100"},
		map[string]any{"op": "Add", "path": `addresses[type eq "work"].formatted`, "value": "1 Main St"},
		map[string]any{"op": "Replace", "path": "name.givenName", "value": "Patricia"},
		map[string]any{"op": "Replace", "path": `emails[type eq "work"].primary`, "value": true},
		map[string]any{"op": "Remove", "path": `phoneNumbers[type eq "mobile"]`},
	)
	delete(before, "meta")
	delete(after, "meta")
	if before["displayName"] != "Pat" || first(after, "emails") != "new@corp.test" || first(after, "roles") != "auditor" || after["displayName"] != "Pat" || len(after) != len(before) {
		t.Fatalf("unstored attributes changed the user: before %v after %v", before, after)
	}

	out = patch(http.StatusOK,
		map[string]any{"op": "Remove", "path": "emails"},
		map[string]any{"op": "Remove", "path": "roles"},
		map[string]any{"op": "remove", "path": "displayName"},
	)
	if out["emails"] != nil || first(out, "roles") != "user" || out["displayName"] != "pat@corp.test" {
		t.Fatalf("removes: %v", out)
	}
	if code, bad := scimCall(t, h, token, "PATCH", "/scim/v2/Users/"+id, patchBody(
		map[string]any{"op": "replace", "path": `emails[type eq "work"].value`, "value": "x@corp.test"},
	)); code != http.StatusBadRequest || bad["scimType"] != "noTarget" {
		t.Fatalf("replace on absent email: %d %v, want 400 noTarget", code, bad)
	}
	out = patch(http.StatusOK, map[string]any{"op": "Add", "path": `emails[type eq "work"].value`, "value": "added@corp.test"})
	if first(out, "emails") != "added@corp.test" {
		t.Fatalf("filtered add on absent email: %v", out["emails"])
	}
	out = patch(http.StatusOK, map[string]any{"op": "remove", "path": `emails[value eq "other@corp.test"]`})
	if first(out, "emails") != "added@corp.test" {
		t.Fatalf("non-matching remove cleared the email: %v", out["emails"])
	}
	out = patch(http.StatusOK, map[string]any{"op": "remove", "path": `emails[value eq "ADDED@corp.test"]`})
	if out["emails"] != nil {
		t.Fatalf("matching remove kept the email: %v", out["emails"])
	}
}
