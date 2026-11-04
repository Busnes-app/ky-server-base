package sso

import (
	"errors"
	"time"
)

var (
	ErrInvalidState       = errors.New("invalid or expired SSO state")
	ErrInvalidIDToken     = errors.New("invalid ID token")
	ErrProviderDisabled   = errors.New("SSO provider is not enabled")
	ErrInvalidLogoutToken = errors.New("invalid logout token")
)

// IdentityClaims holds the unified identity extracted from any SSO provider.
type IdentityClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	Name              string `json:"name"`
	PreferredUsername string `json:"preferred_username"`
	Role              string `json:"role,omitempty"`
	// Roles is KyIdentity's per-app `roles` claim; nil when the ID token has none.
	Roles    []string `json:"roles,omitempty"`
	Provider string   `json:"provider"` // "kysignon", "oidc", "saml"
	// SessionID is the provider's ID token `sid`; back-channel logout names the login by it.
	SessionID string `json:"sid,omitempty"`
	// IssuedAt is the ID token's iat: when the provider vouched for this login.
	IssuedAt time.Time `json:"-"`
}

// LocalRole maps KyIdentity's role claims to this app's role: "admin" in the per-app
// `roles` means admin, otherwise user. Without `roles` the legacy IdP-wide `role` is used.
// ok is false when the token carries neither, and then the stored role must not change.
func (c *IdentityClaims) LocalRole() (role string, ok bool) {
	switch {
	case c.Roles != nil:
		ok = true
		for _, r := range c.Roles {
			if r == "admin" {
				return "admin", true
			}
		}
	case c.Role != "":
		ok = true
		if c.Role == "admin" {
			return "admin", true
		}
	}
	if ok {
		return "user", true
	}
	return "", false
}

// SSOState represents ephemeral state held during OAuth/OIDC authorization flow.
type SSOState struct {
	State        string    `json:"state"`
	Verifier     string    `json:"verifier"`
	Provider     string    `json:"provider"`
	RedirectBack string    `json:"redirect_back"`
	CreatedAt    time.Time `json:"created_at"`
}
