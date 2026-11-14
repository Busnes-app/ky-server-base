package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Busnes-app/ky-primitives/netallow"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

type ThemeUpdateRequest struct {
	Theme string `json:"theme"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	theme, _ := s.store.Settings().GetSetting(r.Context(), "site_theme")
	if theme == "" {
		theme = "patina"
	}

	// Public tier: what the login screen needs before a session exists.
	out := map[string]any{
		"app_name":         s.config.Server.AppName,
		"app_url":          s.config.Server.AppURL,
		"theme":            theme,
		"captcha_provider": s.config.Captcha.Provider,
		"sso_enabled":      s.config.SSO.Enabled,
	}

	user, _, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeJSON(w, http.StatusOK, out)
		return
	}

	out["scim_enabled"] = s.config.SCIM.Enabled
	out["db_driver"] = s.config.Database.Driver

	// extra_settings holds secrets such as the SCIM bearer token. The KyRecovery pairing
	// token is sealed at rest and never leaves the process in either form: an admin can see
	// that a recovery URL is set, never the token that authenticates to it.
	if user.Role == "admin" {
		settings, err := s.store.Settings().GetAllSettings(r.Context())
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "Failed to load settings")
			return
		}
		// By prefix, not by literal: the lib owns the token's key name, and a future
		// spelling must not leak by default.
		for k := range settings {
			if strings.HasPrefix(k, "kyrecovery_token") {
				delete(settings, k)
			}
		}
		out["extra_settings"] = settings
	}

	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetTheme(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req ThemeUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Theme == "" {
		s.writeError(w, http.StatusBadRequest, "Invalid theme")
		return
	}

	if err := s.store.Settings().SetSetting(r.Context(), "site_theme", req.Theme); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to save theme")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]string{"theme": req.Theme})
}

const exemptNetworksSetting = "rate_limit_exempt_networks"

// combineExemptNetworks parses the environment's networks and the admin's as one list.
func (s *Server) combineExemptNetworks(stored string) (*netallow.List, error) {
	return netallow.Parse(s.config.Security.RateLimitExemptNetworks + "," + stored)
}

// storedExemptNetworks is the admin's half of the allowlist; unset reads as empty.
func (s *Server) storedExemptNetworks(ctx context.Context) (string, error) {
	stored, err := s.store.Settings().GetSetting(ctx, exemptNetworksSetting)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return stored, err
}

// loadExemptNetworks builds the allowlist at startup. A stored value that cannot be read or
// no longer parses is dropped with a log line, leaving the environment's list: fewer exemptions, never more.
func (s *Server) loadExemptNetworks() {
	stored, err := s.storedExemptNetworks(context.Background())
	var list *netallow.List
	if err == nil {
		list, err = s.combineExemptNetworks(stored)
	}
	if err != nil {
		log.Printf("[SECURITY] stored rate-limit exempt networks ignored: %v", err)
		list, _ = s.combineExemptNetworks("")
	}
	s.exempt.Store(list)
}

func (s *Server) exemptNetworksStatus(stored string) map[string]any {
	return map[string]any{
		"networks":    stored,
		"environment": s.config.Security.RateLimitExemptNetworks,
		"exempted":    s.exempt.Load().Exempted(),
	}
}

func (s *Server) handleGetExemptNetworks(w http.ResponseWriter, r *http.Request) {
	stored, err := s.storedExemptNetworks(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to load settings")
		return
	}
	s.writeJSON(w, http.StatusOK, s.exemptNetworksStatus(stored))
}

// handleSetExemptNetworks replaces the admin's half of the allowlist. The environment's
// half is not editable here. The exempted count restarts with the new list.
func (s *Server) handleSetExemptNetworks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Networks string `json:"networks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid JSON request body")
		return
	}
	own, err := netallow.Parse(req.Networks)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "netallow: "))
		return
	}
	stored := own.String()
	combined, err := s.combineExemptNetworks(stored)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "netallow: "))
		return
	}
	ctx := context.WithoutCancel(r.Context()) // the saved list and its audit row land together
	previous, _ := s.storedExemptNetworks(ctx)
	if err := s.store.Settings().SetSetting(ctx, exemptNetworksSetting, stored); err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to save the networks")
		return
	}
	s.exempt.Store(combined)
	if err := s.store.Audit().LogAudit(ctx, &store.AuditRecord{
		UserID:    s.actorID(r),
		Action:    "admin.rate_limit_networks",
		Resource:  "settings",
		Details:   fmt.Sprintf("from=%q to=%q", previous, stored),
		IPAddress: s.requestIP(r),
	}); err != nil {
		log.Printf("[SECURITY] audit admin.rate_limit_networks not recorded: %v", err)
	}
	s.writeJSON(w, http.StatusOK, s.exemptNetworksStatus(stored))
}
