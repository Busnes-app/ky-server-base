package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/sso"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"golang.org/x/oauth2"
)

func (s *Server) handleKySignOnLogin(w http.ResponseWriter, r *http.Request) {
	state := crypto.RandomHex(16)
	nonce := crypto.RandomHex(16)
	verifier := oauth2.GenerateVerifier()

	redirectURI := fmt.Sprintf("%s/api/sso/kysignon/callback", s.config.Server.AppURL)
	authURL, err := s.kysignon.BuildAuthURL(r.Context(), redirectURI, state, verifier, nonce)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Store verifier and nonce in short-lived cookies (__Host- on https, so Path=/).
	secure := s.config.Security.CookieSecure
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName("ky_pkce_"+state, secure),
		Value:    verifier,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName("ky_nonce_"+state, secure),
		Value:    nonce,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) handleKySignOnCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	cookie, err := r.Cookie(auth.CookieName("ky_pkce_"+state, s.config.Security.CookieSecure))
	if err != nil || cookie.Value == "" {
		s.writeError(w, http.StatusBadRequest, "Invalid or expired SSO state")
		return
	}
	verifier := cookie.Value
	nonceCookie, err := r.Cookie(auth.CookieName("ky_nonce_"+state, s.config.Security.CookieSecure))
	if err != nil || nonceCookie.Value == "" {
		s.writeError(w, http.StatusBadRequest, "Invalid or expired SSO nonce")
		return
	}

	redirectURI := fmt.Sprintf("%s/api/sso/kysignon/callback", s.config.Server.AppURL)
	claims, err := s.kysignon.ExchangeCode(r.Context(), code, verifier, redirectURI, nonceCookie.Value)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, fmt.Sprintf("SSO exchange failed: %v", err))
		return
	}

	mappedRole, roleSent := claims.LocalRole()
	user, err := s.store.Users().GetUserBySSO(r.Context(), "kysignon", claims.Subject)
	if errors.Is(err, store.ErrNotFound) {
		user, err = s.adoptSCIMUser(r, claims.Subject)
	}
	if errors.Is(err, store.ErrNotFound) {
		if !s.config.SSO.AutoProvision {
			s.writeError(w, http.StatusForbidden, "User account not provisioned")
			return
		}
		role := "user"
		if roleSent {
			role = mappedRole
		}
		user = &store.User{
			ID:          fmt.Sprintf("usr_%s", crypto.RandomHex(12)),
			Username:    claims.PreferredUsername,
			Email:       claims.Email,
			DisplayName: claims.Name,
			Role:        role,
			Status:      "active",
			SSOProvider: "kysignon",
			SSOSubject:  claims.Subject,
		}
		err = s.store.Users().CreateUser(r.Context(), user)
		if errors.Is(err, store.ErrAlreadyExists) {
			s.writeError(w, http.StatusConflict, fmt.Sprintf("An account named %q already exists here and is not linked to this KyIdentity sign-in. An administrator must rename or remove that account in its directory, or provision it through SCIM with your KyIdentity ID as its externalId.", claims.PreferredUsername))
			return
		}
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to provision SSO user")
		return
	}
	if user.Status != "active" {
		s.writeError(w, http.StatusForbidden, "Account is disabled")
		return
	}
	// The role follows KyIdentity on every sign-in; the change revokes the user's other
	// sessions in the same transaction. A token without role claims leaves it alone.
	if roleSent && user.Role != mappedRole {
		prevRole := user.Role
		user.Role = mappedRole
		if err := s.store.Users().UpdateUserProfile(r.Context(), user, prevRole, user.Status); errors.Is(err, store.ErrConflict) {
			s.writeError(w, http.StatusConflict, "Your account changed while you were signing in. Sign in again.")
			return
		} else if err != nil {
			s.writeError(w, http.StatusInternalServerError, "Failed to update the account role")
			return
		}
		_ = s.store.Audit().LogAudit(r.Context(), &store.AuditRecord{UserID: user.ID, Action: "sso.role_changed", Resource: user.Username, Details: fmt.Sprintf("from=%s to=%s", prevRole, mappedRole), IPAddress: s.requestIP(r)})
	}

	_, _, err = s.sessions.IssueSSOSession(r.Context(), w, r, user, claims.SessionID, claims.IssuedAt)
	if errors.Is(err, store.ErrSSOSessionEnded) {
		s.writeError(w, http.StatusUnauthorized, "SSO session already ended")
		return
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Session creation failed")
		return
	}

	http.Redirect(w, r, "/", http.StatusFound)
}

// adoptSCIMUser links the SCIM-provisioned account whose externalId is this subject (see
// sso.AdoptSCIMUser) and audits the link. ErrNotFound when none.
func (s *Server) adoptSCIMUser(r *http.Request, subject string) (*store.User, error) {
	user, adopted, err := sso.AdoptSCIMUser(r.Context(), s.store.Users(), subject)
	if adopted {
		_ = s.store.Audit().LogAudit(r.Context(), &store.AuditRecord{UserID: user.ID, Action: "sso.scim_account_linked", Resource: user.Username, IPAddress: s.requestIP(r)})
	}
	return user, err
}

func (s *Server) handleKySignOnSyncWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	sig := r.Header.Get("X-KySignOn-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Signature-SHA256")
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}

	if err := s.kysignon.HandleSyncWebhook(r.Context(), body, sig); err != nil {
		s.writeError(w, http.StatusUnauthorized, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]bool{"synced": true})
}

// ssoLogoutFence outlives the 5-minute login callback window, so a logout that lands
// while the browser is still finishing that login also ends it.
const ssoLogoutFence = 10 * time.Minute

// handleKySignOnBackchannelLogout is the OpenID Connect Back-Channel Logout receiver: KySignOn
// POSTs a signed logout_token when a login ends, and every local session of it ends here.
func (s *Server) handleKySignOnBackchannelLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	claims, err := s.kysignon.VerifyLogoutToken(r.Context(), r.PostFormValue("logout_token"))
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	fenceUntil := time.Now().Add(ssoLogoutFence)
	if claims.SessionID != "" {
		err = s.store.Sessions().EndSSOSession(r.Context(), claims.SessionID, fenceUntil)
	} else {
		// iat has whole-second precision: round up so a login from that same second ends too.
		err = s.store.Sessions().EndSSOSubject(r.Context(), "kysignon", claims.Subject, claims.IssuedAt.Add(time.Second), fenceUntil)
	}
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "logout failed")
		return
	}
	_ = s.store.Audit().LogAudit(r.Context(), &store.AuditRecord{
		Action:    "auth.sso_backchannel_logout",
		Resource:  "session",
		IPAddress: s.requestIP(r),
	})
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSAMLMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(s.saml.GenerateMetadata()))
}
