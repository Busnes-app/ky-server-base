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
