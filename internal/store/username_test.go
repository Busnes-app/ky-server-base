package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/store/migrations"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// Lookups compare LOWER(username), so two names differing only in case make sign-in pick one
// arbitrarily. New ones are refused whether or not the unique index could be built.
func TestCaseVariantUsernamesAreRefused(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u1", Username: "admin", Role: "admin", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u2", Username: "Admin", Role: "user", Status: "active", SSOProvider: "scim"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("case-variant create: %v, want ErrAlreadyExists", err)
	}
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u3", Username: "bob", Role: "user", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := st.Users().GetUserByID(ctx, "u3")
	bob.Username = "ADMIN"
	if err := st.Users().UpdateUserProfile(ctx, bob); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("case-variant rename: %v, want ErrAlreadyExists", err)
	}
	bob.Username = "Bob" // a user may change the case of their own name
	if err := st.Users().UpdateUserProfile(ctx, bob); err != nil {
		t.Fatalf("own-name case change: %v", err)
	}
}

// An installation that already holds case-variant duplicates keeps running: nothing is
// renamed, the clash is reported, no index is built, and new variants are still refused.
func TestExistingCaseVariantsSkipTheIndex(t *testing.T) {
	ctx := context.Background()
	cfg := testdb.Config(t)
	st, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u1", Username: "admin", Role: "admin", Status: "active", SSOProvider: "local"}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	db, err := sql.Open(map[string]string{"sqlite": "sqlite", "postgres": "pgx"}[cfg.Driver], cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Data written before this release: drop the index and add a variant behind its back.
	if _, err := db.ExecContext(ctx, `DROP INDEX idx_users_username_ci`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users (id, username, created_at, updated_at) VALUES ('u2', 'ADMIN', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	clashes, err := migrations.EnsureUsernameIndex(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(clashes) != 1 || len(clashes[0]) != 2 {
		t.Fatalf("clashes %v, want one pair", clashes)
	}

	st, err = store.Open(ctx, cfg) // startup must not fail
	if err != nil {
		t.Fatalf("startup with existing duplicates: %v", err)
	}
	defer st.Close()
	if u, err := st.Users().GetUserByID(ctx, "u2"); err != nil || u.Username != "ADMIN" {
		t.Fatalf("existing row renamed or lost: %+v %v", u, err)
	}
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u3", Username: "Admin", Role: "user", Status: "active", SSOProvider: "local"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("new variant beside existing duplicates: %v", err)
	}
}

func TestAdvanceDirectoryEventDropsOlderEvents(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	for _, tc := range []struct {
		ts   int64
		want bool
	}{{100, true}, {100, true}, {99, false}, {101, true}, {100, false}} {
		got, err := st.Users().AdvanceDirectoryEvent(ctx, "kysignon", "sub-1", tc.ts)
		if err != nil || got != tc.want {
			t.Fatalf("ts %d: got %v %v, want %v", tc.ts, got, err, tc.want)
		}
	}
	if ok, _ := st.Users().AdvanceDirectoryEvent(ctx, "kysignon", "sub-2", 1); !ok {
		t.Fatal("subjects are not independent")
	}
}
