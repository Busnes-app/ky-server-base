package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// KySignOnClient manages interactions with the central KySignOn identity provider.
type KySignOnClient struct {
	config config.SSOConfig
	store  store.Store
	flow   *oauthFlow
	// order serializes directory events, so the per-subject order check and the change it
	// guards cannot interleave. One server process per database is the supported deployment.
	order sync.Mutex
}

func NewKySignOnClient(cfg config.SSOConfig, st store.Store) *KySignOnClient {
	return &KySignOnClient{
		config: cfg,
		store:  st,
		flow:   newOAuthFlow(cfg.KySignOnIssuer, cfg.KySignOnClientID, cfg.KySignOnSecret),
	}
}

// BuildAuthURL generates the authorization code URL with PKCE for KySignOn.
func (k *KySignOnClient) BuildAuthURL(ctx context.Context, redirectURI, state, verifier, nonce string) (string, error) {
	return k.flow.authCodeURL(ctx, redirectURI, state, verifier, nonce)
}

// ExchangeCode exchanges the authorization code and verifier for identity claims.
func (k *KySignOnClient) ExchangeCode(ctx context.Context, code, verifier, redirectURI, expectedNonce string) (*IdentityClaims, error) {
	claims, err := k.flow.exchange(ctx, code, verifier, redirectURI, expectedNonce)
	if err != nil {
		return nil, err
	}
	claims.Provider = "kysignon"
	return claims, nil
}

// VerifyLogoutToken verifies a back-channel logout token signed by KySignOn for this client.
func (k *KySignOnClient) VerifyLogoutToken(ctx context.Context, raw string) (*LogoutClaims, error) {
	return k.flow.verifyLogout(ctx, raw)
}

// KySignOnSyncPayload defines the schema received during automatic directory replication webhooks.
type KySignOnSyncPayload struct {
	Event       string `json:"event"` // "user.created", "user.updated", "user.deactivated", "user.deleted"
	ID          string `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
	Timestamp   int64  `json:"timestamp"`
}

// HandleSyncWebhook processes inbound HMAC-SHA256 signed user updates from KySignOn server.
func (k *KySignOnClient) HandleSyncWebhook(ctx context.Context, body []byte, signature string) error {
	if len(k.config.KySignOnHMACSecret) < crypto.MinHMACKeyBytes {
		return fmt.Errorf("webhook HMAC secret must contain at least %d bytes", crypto.MinHMACKeyBytes)
	}

	if !crypto.VerifyHMACSHA256(body, k.config.KySignOnHMACSecret, signature) {
		return errors.New("invalid webhook signature")
	}

	var payload KySignOnSyncPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("invalid json payload: %w", err)
	}
	if payload.Timestamp == 0 || time.Since(time.Unix(payload.Timestamp, 0)).Abs() > 5*time.Minute {
		return errors.New("webhook timestamp is missing or expired")
	}
	if payload.ID == "" {
		return errors.New("webhook event names no subject")
	}
	// Per-subject ordering: an event older than the newest one taken for this subject is a
	// replay or arrived late, and applying it would undo a newer change. It is acknowledged
	// and dropped. The order is recorded before the change, so no crash or failure between
	// the two can let an older event through after a newer change committed; a failed event
	// is retried by the sender with the same timestamp, which is applied again.
	k.order.Lock()
	defer k.order.Unlock()
	users := k.store.Users()
	last, err := users.LastDirectoryEvent(ctx, "kysignon", payload.ID)
	if err != nil {
		return err
	}
	if payload.Timestamp < last {
		return nil
	}
	if err := users.RecordDirectoryEvent(ctx, "kysignon", payload.ID, payload.Timestamp); err != nil {
		return err
	}
	return k.apply(ctx, payload)
}

// AdoptSCIMUser links a SCIM-provisioned account to KyIdentity: the row whose externalId is
// this subject becomes a "kysignon" row and stays SCIM-managed, so back-channel logout and
// directory events reach it and the IdP keeps managing it by id. Sign-in and directory events
// both call it, so neither creates a second row for the same person. ErrNotFound when none;
// adopted reports whether this call made the link.
func AdoptSCIMUser(ctx context.Context, users store.UserStore, subject string) (user *store.User, adopted bool, err error) {
	if subject == "" { // SCIM rows sent without an externalId carry an empty subject
		return nil, false, store.ErrNotFound
	}
	user, err = users.GetUserBySSO(ctx, "scim", subject)
	if err != nil {
		return nil, false, err
	}
	if err := users.LinkSSOIdentity(ctx, user.ID, subject, "scim", "kysignon"); errors.Is(err, store.ErrConflict) {
		user, err = users.GetUserBySSO(ctx, "kysignon", subject) // a concurrent call linked it first
		return user, false, err
	} else if err != nil {
		return nil, false, err
	}
	user.SSOProvider = "kysignon"
	return user, true, nil
}

// apply makes the local account match one directory event.
func (k *KySignOnClient) apply(ctx context.Context, payload KySignOnSyncPayload) error {
	switch payload.Event {
	case "user.created", "user.updated":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kysignon", payload.ID)
		if errors.Is(err, store.ErrNotFound) {
			var adopted bool
			existing, adopted, err = AdoptSCIMUser(ctx, k.store.Users(), payload.ID)
			if adopted {
				_ = k.store.Audit().LogAudit(ctx, &store.AuditRecord{UserID: existing.ID, Action: "sso.scim_account_linked", Resource: existing.Username})
			}
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}

		role := payload.Role
		if role == "" {
			role = "user"
		}
		status := payload.Status
		if status == "" {
			status = "active"
		}

		if existing != nil {
			prevRole, prevStatus := existing.Role, existing.Status
			existing.Username = payload.Username
			existing.Email = payload.Email
			existing.DisplayName = payload.DisplayName
			existing.Role = role
			existing.Status = status
			// A role or status change revokes the user's sessions in the same transaction.
			return k.store.Users().UpdateUserProfile(ctx, existing, prevRole, prevStatus)
		}

		newUser := &store.User{
			ID:          fmt.Sprintf("usr_%s", crypto.RandomHex(12)),
			Username:    payload.Username,
			Email:       payload.Email,
			DisplayName: payload.DisplayName,
			Role:        role,
			Status:      status,
			SSOProvider: "kysignon",
			SSOSubject:  payload.ID,
		}
		return k.store.Users().CreateUser(ctx, newUser)

	case "user.deactivated":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kysignon", payload.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil // User might not exist locally
		}
		if err != nil {
			return err
		}
		// Sessions go even when the row is already inactive. They go first, so a failure
		// leaves nothing committed; a session created meanwhile is revoked by the status change.
		if err := k.store.Sessions().DeleteUserSessions(ctx, existing.ID); err != nil {
			return err
		}
		prevStatus := existing.Status
		existing.Status = "inactive"
		return k.store.Users().UpdateUserProfile(ctx, existing, existing.Role, prevStatus)

	case "user.deleted":
		existing, err := k.store.Users().GetUserBySSO(ctx, "kysignon", payload.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return k.store.Users().DeleteUser(ctx, existing.ID)
	}

	return nil
}
