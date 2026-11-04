package sso_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

const orderSecret = "webhook-secret-that-is-long-enough-123"

func sendEvent(t *testing.T, c *sso.KySignOnClient, p sso.KySignOnSyncPayload) error {
	t.Helper()
	body, _ := json.Marshal(p)
	return c.HandleSyncWebhook(context.Background(), body, crypto.ComputeHMACSHA256(body, orderSecret))
}

func openOrderStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// A replayed or late webhook must not undo a newer change for the same subject.
func TestKySignOnWebhookDropsOlderEventsPerSubject(t *testing.T) {
	st := openOrderStore(t)
	c := sso.NewKySignOnClient(config.SSOConfig{KySignOnHMACSecret: orderSecret}, st)
	now := time.Now().Unix()
	for _, ev := range []struct {
		role string
		ts   int64
	}{{"user", now - 60}, {"admin", now - 30}, {"user", now - 10}, {"admin", now - 30}} {
		if err := sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.updated", ID: "ext-9", Username: "erin", Role: ev.role, Timestamp: ev.ts}); err != nil {
			t.Fatal(err)
		}
	}
	u, err := st.Users().GetUserBySSO(context.Background(), "kysignon", "ext-9")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != "user" {
		t.Fatalf("a replayed older event re-promoted the user: role %q", u.Role)
	}

	// Deactivation and deletion are ordered too: a late update cannot revive the account.
	if err := sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.deactivated", ID: "ext-9", Timestamp: now - 5}); err != nil {
		t.Fatal(err)
	}
	if err := sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.updated", ID: "ext-9", Username: "erin", Status: "active", Timestamp: now - 8}); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.Users().GetUserBySSO(context.Background(), "kysignon", "ext-9"); u.Status != "inactive" {
		t.Fatalf("a late update reactivated a deactivated user: status %q", u.Status)
	}
	if err := sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.deleted", ID: "ext-9", Timestamp: now - 2}); err != nil {
		t.Fatal(err)
	}
	if err := sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.created", ID: "ext-9", Username: "erin", Timestamp: now - 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Users().GetUserBySSO(context.Background(), "kysignon", "ext-9"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a late create recreated a deleted user: %v", err)
	}
	if err := sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.updated", Username: "nobody", Timestamp: now}); err == nil {
		t.Fatal("an event without a subject was accepted")
	}
}

type failingProfileWrites struct {
	store.UserStore
	fail bool
}

func (f *failingProfileWrites) UpdateUserProfile(ctx context.Context, u *store.User, role, status string) error {
	if f.fail {
		return errors.New("database unavailable")
	}
	return f.UserStore.UpdateUserProfile(ctx, u, role, status)
}

type failingUsers struct {
	store.Store
	users *failingProfileWrites
}

func (s failingUsers) Users() store.UserStore { return s.users }

// A failed event still moves the order, so nothing older can slip in before the sender
// retries it; the retry carries the same timestamp and is applied.
func TestFailedWebhookIsAppliedOnRetry(t *testing.T) {
	st := openOrderStore(t)
	users := &failingProfileWrites{UserStore: st.Users()}
	c := sso.NewKySignOnClient(config.SSOConfig{KySignOnHMACSecret: orderSecret}, failingUsers{st, users})
	send := func(role string, ts int64) error {
		return sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.updated", ID: "ext-7", Username: "gus", Role: role, Timestamp: ts})
	}
	now := time.Now().Unix()
	if err := send("user", now-60); err != nil {
		t.Fatal(err)
	}
	users.fail = true
	if err := send("user", now-10); err == nil { // a demotion that failed to apply
		t.Fatal("a failed update was acknowledged")
	}
	users.fail = false
	if err := send("admin", now-30); err != nil { // an older promotion arriving late
		t.Fatal(err)
	}
	if u, _ := st.Users().GetUserBySSO(context.Background(), "kysignon", "ext-7"); u.Role != "user" {
		t.Fatalf("an event older than a failed newer one was applied: role %q", u.Role)
	}
	if err := send("manager", now-10); err != nil { // the sender's retry of the newer event
		t.Fatal(err)
	}
	if u, _ := st.Users().GetUserBySSO(context.Background(), "kysignon", "ext-7"); u.Role != "manager" {
		t.Fatalf("the retried event was not applied: role %q", u.Role)
	}
}

// A directory event for a subject SCIM already provisioned adopts that row instead of
// creating a second account (or failing on its username), as sign-in does.
func TestKySignOnWebhookAdoptsTheSCIMRow(t *testing.T) {
	st := openOrderStore(t)
	ctx := context.Background()
	scimRow := &store.User{ID: "usr_scim", Username: "alice", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "sub-a", SCIMManaged: true}
	if err := st.Users().CreateUser(ctx, scimRow); err != nil {
		t.Fatal(err)
	}
	c := sso.NewKySignOnClient(config.SSOConfig{KySignOnHMACSecret: orderSecret}, st)
	if err := sendEvent(t, c, sso.KySignOnSyncPayload{Event: "user.created", ID: "sub-a", Username: "alice", DisplayName: "Alice A", Timestamp: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().GetUserBySSO(ctx, "kysignon", "sub-a")
	if err != nil || u.ID != "usr_scim" || !u.SCIMManaged || u.DisplayName != "Alice A" {
		t.Fatalf("the SCIM row was not adopted: %+v %v", u, err)
	}
	if n, _ := st.Users().CountUsers(ctx); n != 1 {
		t.Fatalf("%d accounts, want 1", n)
	}
}
