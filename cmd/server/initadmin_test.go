package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func TestInitAdminNeverPromotesSilently(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, u := range []*store.User{
		{ID: "usr_bob", Username: "bob", Role: "user", Status: "active", SSOProvider: "local"},
		{ID: "usr_sso", Username: "carol", Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: "c"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	const pass = "ReplacementPass123!"

	var out bytes.Buffer
	if _, err := initAdmin(ctx, st, "bob", pass, false, &out); err == nil || !strings.Contains(err.Error(), "-promote") {
		t.Fatalf("a non-admin was reset without -promote: %v", err)
	}
	if !strings.Contains(out.String(), "role=user status=active") {
		t.Fatalf("previous role and status not printed: %q", out.String())
	}
	if bob, _ := st.Users().GetUserByID(ctx, "usr_bob"); bob.Role != "user" {
		t.Fatalf("refused reset still changed the role to %q", bob.Role)
	}
	if _, err := initAdmin(ctx, st, "carol", pass, true, &out); err == nil {
		t.Fatal("an SSO account was turned into a local admin")
	}
	if _, err := initAdmin(ctx, st, "bob", pass, true, &out); err != nil {
		t.Fatalf("-promote: %v", err)
	}
	if bob, _ := st.Users().GetUserByID(ctx, "usr_bob"); bob.Role != "admin" || !bob.MustChangePassword {
		t.Fatalf("-promote did not make bob a flagged admin: %+v", bob)
	}
	// An existing admin needs no flag; a new name is created.
	if _, err := initAdmin(ctx, st, "BOB", pass, false, &out); err != nil {
		t.Fatalf("resetting an admin: %v", err)
	}
	if _, err := initAdmin(ctx, st, "newadmin", pass, false, &out); err != nil {
		t.Fatalf("create: %v", err)
	}
}
