package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// TakeOverForSCIM claims and updates an unmanaged KyIdentity row in one statement: a refused
// write (a taken name, an already managed row) changes nothing.
func TestTakeOverForSCIMIsAllOrNothing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, u := range []*store.User{
		{ID: "k1", Username: "kim", Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: "s1"},
		{ID: "l1", Username: "lee", Role: "user", Status: "active", SSOProvider: "local"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	u, _ := st.Users().GetUserByID(ctx, "k1")
	u.Username = "lee"
	if err := st.Users().TakeOverForSCIM(ctx, u, "user", "active"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("rename onto a taken name: %v", err)
	}
	if got, _ := st.Users().GetUserByID(ctx, "k1"); got.SCIMManaged || got.Username != "kim" {
		t.Fatalf("a refused take-over changed the row: %+v", got)
	}
	u.Username, u.Email = "kim", "kim@example.com"
	if err := st.Users().TakeOverForSCIM(ctx, u, "user", "active"); err != nil || !u.SCIMManaged {
		t.Fatal(err)
	}
	if got, _ := st.Users().GetUserByID(ctx, "k1"); !got.SCIMManaged || got.Email != "kim@example.com" || got.SSOProvider != "kysignon" {
		t.Fatalf("take-over not stored: %+v", got)
	}
	if err := st.Users().TakeOverForSCIM(ctx, u, "user", "active"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second take-over: %v, want ErrConflict", err)
	}
}

// UpdateUserRole writes the role alone: a profile edit made after the caller's read stays.
func TestUpdateUserRoleKeepsAConcurrentProfileEdit(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.Users().CreateUser(ctx, &store.User{ID: "m1", Username: "max", Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: "s2"}); err != nil {
		t.Fatal(err)
	}
	stale, _ := st.Users().GetUserByID(ctx, "m1")
	fresh, _ := st.Users().GetUserByID(ctx, "m1")
	fresh.Email = "max@example.com"
	if err := st.Users().UpdateUserProfile(ctx, fresh, "user", "active"); err != nil {
		t.Fatal(err)
	}
	stale.Role = "admin"
	if err := st.Users().UpdateUserRole(ctx, stale, "user"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Users().GetUserByID(ctx, "m1"); got.Role != "admin" || got.Email != "max@example.com" {
		t.Fatalf("role write undid the edit: %+v", got)
	}
	if err := st.Users().UpdateUserRole(ctx, stale, "user"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale role write: %v, want ErrConflict", err)
	}
}
