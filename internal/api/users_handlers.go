package api

import (
	"net/http"
	"strconv"
	"time"
)

// adminUser is what the Directory page lists: no credentials, MFA state or SSO subjects.
type adminUser struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	SSOProvider string     `json:"sso_provider"`
	SCIMManaged bool       `json:"scim_managed"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

const maxUserPage = 200

// handleListUsers is GET /api/admin/users?offset=&limit=&search=, newest first.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, maxUserPage)
	offset = max(offset, 0)
	users, total, err := s.store.Users().ListUsers(r.Context(), offset, limit, q.Get("search"))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Failed to list users")
		return
	}
	out := make([]adminUser, 0, len(users))
	for _, u := range users {
		out = append(out, adminUser{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, Role: u.Role,
			Status: u.Status, SSOProvider: u.SSOProvider, SCIMManaged: u.SCIMManaged, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": out, "total": total, "offset": offset, "limit": limit})
}
