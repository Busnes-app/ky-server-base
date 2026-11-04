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

type unavailableSSOLookup struct{ store.UserStore }

func (unavailableSSOLookup) GetUserBySSO(context.Context, string, string) (*store.User, error) {
	return nil, errors.New("database unavailable")
}

type unavailableUsers struct{ store.Store }

func (s unavailableUsers) Users() store.UserStore { return unavailableSSOLookup{s.Store.Users()} }

// A deprovisioning event that could not be applied must fail, so KySignOn sees it and can
// retry; acknowledging it would leave the account and its sessions live.
func TestKySignOnDeprovisioningReportsStoreErrors(t *testing.T) {
	st, err := store.Open(context.Background(), testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret := "webhook-secret-that-is-long-enough-123"
	for _, event := range []string{"user.deactivated", "user.deleted"} {
		body, _ := json.Marshal(sso.KySignOnSyncPayload{Event: event, ID: "ext-1", Timestamp: time.Now().Unix()})
		sig := crypto.ComputeHMACSHA256(body, secret)
		cfg := config.SSOConfig{KySignOnHMACSecret: secret}
		if err := sso.NewKySignOnClient(cfg, st).HandleSyncWebhook(context.Background(), body, sig); err != nil {
			t.Fatalf("%s for an unknown user: %v", event, err)
		}
		if err := sso.NewKySignOnClient(cfg, unavailableUsers{st}).HandleSyncWebhook(context.Background(), body, sig); err == nil {
			t.Fatalf("%s acknowledged although the lookup failed", event)
		}
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

// Only an applied event advances the per-subject ordering: when the newest event fails, an
// older one delivered after it is still applied instead of being dropped as stale.
func TestFailedWebhookDoesNotAdvanceTheOrdering(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret := "webhook-secret-that-is-long-enough-123"
	users := &failingProfileWrites{UserStore: st.Users()}
	client := sso.NewKySignOnClient(config.SSOConfig{KySignOnHMACSecret: secret}, failingUsers{st, users})
	send := func(role string, ts int64) error {
		body, _ := json.Marshal(sso.KySignOnSyncPayload{Event: "user.updated", ID: "ext-7", Username: "gus", Role: role, Timestamp: ts})
		return client.HandleSyncWebhook(ctx, body, crypto.ComputeHMACSHA256(body, secret))
	}
	now := time.Now().Unix()
	if err := send("user", now-60); err != nil {
		t.Fatal(err)
	}
	users.fail = true
	if err := send("admin", now-10); err == nil {
		t.Fatal("a failed update was acknowledged")
	}
	users.fail = false
	if err := send("manager", now-30); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.Users().GetUserBySSO(ctx, "kysignon", "ext-7"); u.Role != "manager" {
		t.Fatalf("an older event after a failed newer one was dropped: role %q", u.Role)
	}
}
