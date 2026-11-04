package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

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
	if err := st.Users().UpdateUserProfile(ctx, bob, "user", "active"); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("case-variant rename: %v, want ErrAlreadyExists", err)
	}
	bob.Username = "Bob" // a user may change the case of their own name
	if err := st.Users().UpdateUserProfile(ctx, bob, "user", "active"); err != nil {
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
	// Either account of the pair can still be updated and deprovisioned under its own name.
	u2, _ := st.Users().GetUserByID(ctx, "u2")
	u2.Status = "inactive"
	if err := st.Users().UpdateUserProfile(ctx, u2, u2.Role, "active"); err != nil {
		t.Fatalf("deprovisioning one of an existing pair: %v", err)
	}
	u2.Username = "Admin" // a case change of its own name adds no clash
	if err := st.Users().UpdateUserProfile(ctx, u2, u2.Role, u2.Status); err != nil {
		t.Fatalf("case change of an existing pair member's own name: %v", err)
	}
	// "Own name" is decided by the database's LOWER, not Go's Unicode folding: EqualFold("ſ",
	// "s") is true, but renaming ſ to s beside S would add a new clash while the index is off.
	for _, u := range []*store.User{
		{ID: "u4", Username: "ſ", Role: "user", Status: "active", SSOProvider: "local"},
		{ID: "u5", Username: "S", Role: "user", Status: "active", SSOProvider: "local"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	u4, _ := st.Users().GetUserByID(ctx, "u4")
	u4.Username = "s"
	if err := st.Users().UpdateUserProfile(ctx, u4, u4.Role, u4.Status); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("rename to a case variant of another account: %v", err)
	}
}

// Adoption moves an account's provider; a profile write from a read taken before it must not
// move it back, or back-channel logout by subject would miss the account.
func TestProfileWritesLeaveTheSSOLinkAlone(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u1", Username: "frank", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "f", SCIMManaged: true}); err != nil {
		t.Fatal(err)
	}
	stale, _ := st.Users().GetUserByID(ctx, "u1")
	if err := st.Users().LinkSSOIdentity(ctx, "u1", "scim", "kysignon"); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().LinkSSOIdentity(ctx, "u1", "scim", "kysignon"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second link: %v, want ErrConflict", err)
	}
	stale.DisplayName = "Frank"
	if err := st.Users().UpdateUserProfile(ctx, stale, stale.Role, stale.Status); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.Users().GetUserBySSO(ctx, "kysignon", "f"); u == nil || u.DisplayName != "Frank" {
		t.Fatalf("stale profile write undid the link: %+v", u)
	}
}

// A profile write from a stale read cannot restore a role or status changed since that read,
// and a role or status change takes the user's sessions with it in the same transaction.
func TestUpdateUserProfileIsACompareAndSwapOnPrivileges(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.Users().CreateUser(ctx, &store.User{ID: "u1", Username: "dana", Role: "admin", Status: "active", SSOProvider: "scim", SCIMManaged: true}); err != nil {
		t.Fatal(err)
	}
	stale, _ := st.Users().GetUserByID(ctx, "u1")

	demote, _ := st.Users().GetUserByID(ctx, "u1")
	demote.Role = "user"
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "s1", UserID: "u1", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().UpdateUserProfile(ctx, demote, "admin", "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sessions().GetSession(ctx, "s1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("demotion left the session: %v", err)
	}

	stale.Email = "dana@corp.test" // an unrelated change from a read taken before the demotion
	if err := st.Users().UpdateUserProfile(ctx, stale, "admin", "active"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale write: %v, want ErrConflict", err)
	}
	if u, _ := st.Users().GetUserByID(ctx, "u1"); u.Role != "user" {
		t.Fatalf("stale write restored role %q", u.Role)
	}
}
