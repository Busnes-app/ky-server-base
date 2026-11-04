package sso

import (
	"context"
	"errors"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// KySignOnClient manages interactions with the central KySignOn identity provider.
type KySignOnClient struct {
	flow *oauthFlow
}

func NewKySignOnClient(cfg config.SSOConfig, _ store.Store) *KySignOnClient {
	return &KySignOnClient{
		flow: newOAuthFlow(cfg.KySignOnIssuer, cfg.KySignOnClientID, cfg.KySignOnSecret),
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

// AdoptSCIMUser links a SCIM-provisioned account to KyIdentity: the row whose externalId is
// this subject becomes a "kysignon" row and stays SCIM-managed, so back-channel logout and
// SCIM updates reach it and the IdP keeps managing it by id. Sign-in calls this before
// provisioning so it never creates a second row for the same person. ErrNotFound when none;
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
