package scim

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"

	protocol "github.com/elimity-com/scim"
	protocolErrors "github.com/elimity-com/scim/errors"
	"github.com/elimity-com/scim/optional"
	"github.com/elimity-com/scim/schema"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

type Server struct {
	config   config.SCIMConfig
	protocol http.Handler
}

func NewServer(st store.Store, cfg config.SCIMConfig, appURL string) *Server {
	userHandler := &userResourceHandler{store: st, allowAdmin: cfg.AllowAdmin}
	groupHandler := &groupResourceHandler{store: st}
	server, err := protocol.NewServer(&protocol.ServerArgs{
		ServiceProviderConfig: &protocol.ServiceProviderConfig{
			DocumentationURI: optional.NewString("https://busnes.app/docs/scim"),
			SupportPatch:     true, SupportFiltering: true, MaxResults: 200,
			AuthenticationSchemes: []protocol.AuthenticationScheme{{Type: protocol.AuthenticationTypeOauthBearerToken, Name: "OAuth Bearer Token", Description: "RFC 6750 bearer token", SpecURI: optional.NewString("https://www.rfc-editor.org/rfc/rfc6750"), Primary: true}},
		},
		ResourceTypes: []protocol.ResourceType{
			{ID: optional.NewString("User"), Name: "User", Endpoint: "/Users", Description: optional.NewString("User Account"), Schema: schema.CoreUserSchema(), Handler: userHandler},
			{ID: optional.NewString("Group"), Name: "Group", Endpoint: "/Groups", Description: optional.NewString("Group Resource"), Schema: schema.CoreGroupSchema(), Handler: groupHandler},
		},
	}, protocol.WithBaseURL(strings.TrimRight(appURL, "/")+"/scim/v2"))
	if err != nil {
		return &Server{config: cfg, protocol: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "SCIM initialization failed", http.StatusInternalServerError)
		})}
	}
	return &Server{config: cfg, protocol: server}
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	h := http.StripPrefix("/scim", s.protocol)
	mux.Handle("/scim/v2", h)
	mux.Handle("/scim/v2/", h)
}

func (s *Server) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/scim/v2") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.config.Enabled {
			writeAuthError(w, http.StatusForbidden, "SCIM provisioning is disabled")
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !constantTimeEqual(parts[1], s.config.BearerToken) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeAuthError(w, http.StatusUnauthorized, "Invalid or missing bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeAuthError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocolErrors.ScimError{Status: status, Detail: detail})
}

type userResourceHandler struct {
	store      store.Store
	allowAdmin bool
}

// errAdminGrant refuses a SCIM write that would make an account an administrator.
var errAdminGrant = protocolErrors.ScimError{ScimType: protocolErrors.ScimTypeInvalidValue, Status: http.StatusBadRequest,
	Detail: "SCIM may not grant the admin role (KY_SCIM_ALLOW_ADMIN is off)"}

// checkRole refuses a grant of admin, not an account that already holds it: an existing
// admin can still be renamed, deactivated or demoted through SCIM.
func (h *userResourceHandler) checkRole(oldRole, newRole string) error {
	if newRole == "admin" && oldRole != "admin" && !h.allowAdmin {
		return errAdminGrant
	}
	return nil
}

// managedUser loads an account SCIM may see. Every other account, local and KyIdentity ones
// included, is reported as not found, so the IdP cannot read, rename, disable or delete it.
func (h *userResourceHandler) managedUser(r *http.Request, id string) (*store.User, error) {
	user, err := h.store.Users().GetUserByID(r.Context(), id)
	if err != nil {
		return nil, scimStoreError(err, id)
	}
	if !user.SCIMManaged {
		return nil, protocolErrors.ScimErrorResourceNotFound(id)
	}
	return user, nil
}

func (h *userResourceHandler) Create(r *http.Request, attrs protocol.ResourceAttributes) (protocol.Resource, error) {
	username, _ := attrs["userName"].(string)
	user := &store.User{ID: "usr_" + crypto.RandomHex(12), Username: username, Email: primaryValue(attrs["emails"]), DisplayName: stringValue(attrs, "displayName", username), Role: primaryValue(attrs["roles"]), Status: statusFromActive(attrs), SSOProvider: "scim", SSOSubject: stringValue(attrs, "externalId", ""), SCIMManaged: true}
	if user.Role == "" {
		user.Role = "user"
	}
	if err := h.checkRole("", user.Role); err != nil {
		return protocol.Resource{}, err
	}
	if err := h.store.Users().CreateUser(r.Context(), user); err != nil {
		return protocol.Resource{}, scimStoreError(err, user.ID)
	}
	_ = h.store.Audit().LogAudit(r.Context(), &store.AuditRecord{UserID: user.ID, Action: "scim.user.create", Resource: user.Username})
	return userResource(user), nil
}

func (h *userResourceHandler) Get(r *http.Request, id string) (protocol.Resource, error) {
	user, err := h.managedUser(r, id)
	if err != nil {
		return protocol.Resource{}, err
	}
	return userResource(user), nil
}

// SCIM equality on the attribute named. userName uses the exact lookup; for the others the
// LIKE search only narrows the candidates, and past maxFilterCandidates the filter is refused
// (tooMany) instead of reading the directory into memory.
const maxFilterCandidates = 1000

var equalityFilter = regexp.MustCompile(`(?i)^\s*(userName|emails\.value|emails|email|displayName)\s+eq\s+"([^"]+)"\s*$`)

func (h *userResourceHandler) GetAll(r *http.Request, params protocol.ListRequestParams) (protocol.Page, error) {
	raw := r.URL.Query().Get("filter")
	if raw == "" {
		users, total, err := h.store.Users().ListSCIMUsers(r.Context(), params.StartIndex-1, params.Count, "")
		if err != nil {
			return protocol.Page{}, err
		}
		return userPage(users, total), nil
	}
	match := equalityFilter.FindStringSubmatch(raw)
	if match == nil {
		return protocol.Page{}, protocolErrors.ScimErrorInvalidFilter
	}
	if strings.TrimSpace(match[2]) == "" {
		return protocol.Page{}, protocolErrors.ScimErrorInvalidFilter
	}
	var candidates []*store.User
	if strings.EqualFold(match[1], "userName") {
		user, err := h.store.Users().GetUserByUsername(r.Context(), match[2])
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return protocol.Page{}, err
		}
		if err == nil && user.SCIMManaged {
			candidates = []*store.User{user}
		}
	} else {
		var total int
		var err error
		candidates, total, err = h.store.Users().ListSCIMUsers(r.Context(), 0, maxFilterCandidates, match[2])
		if err != nil {
			return protocol.Page{}, err
		}
		if total > maxFilterCandidates {
			return protocol.Page{}, protocolErrors.ScimErrorTooMany
		}
	}
	var users []*store.User
	for _, user := range candidates {
		value := user.Email
		switch strings.ToLower(match[1]) {
		case "username":
			value = user.Username
		case "displayname":
			value = user.DisplayName
		}
		if strings.EqualFold(value, match[2]) {
			users = append(users, user)
		}
	}
	total := len(users)
	start := min(max(params.StartIndex-1, 0), total)
	end := total
	if params.Count > 0 {
		end = min(start+params.Count, total)
	}
	return userPage(users[start:end], total), nil
}

func userPage(users []*store.User, total int) protocol.Page {
	resources := make([]protocol.Resource, 0, len(users))
	for _, user := range users {
		resources = append(resources, userResource(user))
	}
	return protocol.Page{TotalResults: total, Resources: resources}
}

func (h *userResourceHandler) Replace(r *http.Request, id string, attrs protocol.ResourceAttributes) (protocol.Resource, error) {
	user, err := h.managedUser(r, id)
	if err != nil {
		return protocol.Resource{}, err
	}
	oldRole, oldStatus := user.Role, user.Status
	user.Username, _ = attrs["userName"].(string)
	user.Email = primaryValue(attrs["emails"])
	user.DisplayName = stringValue(attrs, "displayName", user.Username)
	if role := primaryValue(attrs["roles"]); role != "" {
		user.Role = role
	}
	user.Status = statusFromActive(attrs)
	if err := h.checkRole(oldRole, user.Role); err != nil {
		return protocol.Resource{}, err
	}
	if err := h.store.Users().UpdateUserProfile(r.Context(), user); err != nil {
		return protocol.Resource{}, scimStoreError(err, id)
	}
	h.revokeIfPrivilegesChanged(r, user, oldRole, oldStatus)
	return userResource(user), nil
}

func (h *userResourceHandler) Delete(r *http.Request, id string) error {
	if _, err := h.managedUser(r, id); err != nil {
		return err
	}
	return scimStoreError(h.store.Users().DeleteUser(r.Context(), id), id)
}

func (h *userResourceHandler) Patch(r *http.Request, id string, operations []protocol.PatchOperation) (protocol.Resource, error) {
	user, err := h.managedUser(r, id)
	if err != nil {
		return protocol.Resource{}, err
	}
	oldRole, oldStatus := user.Role, user.Status
	for _, op := range operations {
		if op.Op == protocol.PatchOperationRemove {
			continue
		}
		if op.Path == nil {
			if values, ok := op.Value.(map[string]interface{}); ok {
				applyUserValues(user, values)
			}
			continue
		}
		applyUserValue(user, strings.ToLower(op.Path.String()), op.Value)
	}
	if err := h.checkRole(oldRole, user.Role); err != nil {
		return protocol.Resource{}, err
	}
	if err := h.store.Users().UpdateUserProfile(r.Context(), user); err != nil {
		return protocol.Resource{}, scimStoreError(err, id)
	}
	h.revokeIfPrivilegesChanged(r, user, oldRole, oldStatus)
	return userResource(user), nil
}

func (h *userResourceHandler) revokeIfPrivilegesChanged(r *http.Request, user *store.User, oldRole, oldStatus string) {
	if user.Role != oldRole || user.Status != oldStatus {
		_ = h.store.Sessions().DeleteUserSessions(r.Context(), user.ID)
	}
}

func applyUserValues(user *store.User, values map[string]interface{}) {
	for key, value := range values {
		applyUserValue(user, strings.ToLower(key), value)
	}
}
func applyUserValue(user *store.User, path string, value interface{}) {
	switch path {
	case "active":
		if active, ok := value.(bool); ok {
			if active {
				user.Status = "active"
			} else {
				user.Status = "inactive"
			}
		}
	case "displayname":
		if v, ok := value.(string); ok {
			user.DisplayName = v
		}
	case "username":
		if v, ok := value.(string); ok {
			user.Username = v
		}
	case "roles", "role":
		if v := primaryValue(value); v != "" {
			user.Role = v
		}
	case "emails":
		user.Email = primaryValue(value)
	}
}

func userResource(user *store.User) protocol.Resource {
	attrs := protocol.ResourceAttributes{"userName": user.Username, "displayName": user.DisplayName, "active": user.Status == "active"}
	if user.Email != "" {
		attrs["emails"] = []interface{}{map[string]interface{}{"value": user.Email, "type": "work", "primary": true}}
	}
	if user.Role != "" {
		attrs["roles"] = []interface{}{map[string]interface{}{"value": user.Role, "primary": true}}
	}
	return protocol.Resource{ID: user.ID, ExternalID: optional.NewString(user.SSOSubject), Attributes: attrs, Meta: protocol.Meta{Created: &user.CreatedAt, LastModified: &user.UpdatedAt}}
}

type groupResourceHandler struct{ store store.Store }

func (h *groupResourceHandler) Create(r *http.Request, attrs protocol.ResourceAttributes) (protocol.Resource, error) {
	group := &store.Group{ID: "grp_" + crypto.RandomHex(12), DisplayName: stringValue(attrs, "displayName", ""), ExternalID: stringValue(attrs, "externalId", "")}
	members := memberValues(attrs["members"])
	if err := h.checkMembers(r, nil, members); err != nil {
		return protocol.Resource{}, err
	}
	if err := h.store.Groups().CreateGroup(r.Context(), group); err != nil {
		return protocol.Resource{}, scimStoreError(err, group.ID)
	}
	if err := h.replaceMembers(r, group.ID, nil, members); err != nil {
		return protocol.Resource{}, err
	}
	group.Members = members
	return groupResource(group), nil
}
func (h *groupResourceHandler) Get(r *http.Request, id string) (protocol.Resource, error) {
	group, err := h.store.Groups().GetGroupByID(r.Context(), id)
	if err != nil {
		return protocol.Resource{}, scimStoreError(err, id)
	}
	return groupResource(group), nil
}
func (h *groupResourceHandler) GetAll(r *http.Request, params protocol.ListRequestParams) (protocol.Page, error) {
	groups, total, err := h.store.Groups().ListGroups(r.Context(), params.StartIndex-1, params.Count)
	if err != nil {
		return protocol.Page{}, err
	}
	resources := make([]protocol.Resource, 0, len(groups))
	for _, group := range groups {
		resources = append(resources, groupResource(group))
	}
	return protocol.Page{TotalResults: total, Resources: resources}, nil
}
func (h *groupResourceHandler) Replace(r *http.Request, id string, attrs protocol.ResourceAttributes) (protocol.Resource, error) {
	group, err := h.store.Groups().GetGroupByID(r.Context(), id)
	if err != nil {
		return protocol.Resource{}, scimStoreError(err, id)
	}
	old := group.Members
	group.DisplayName = stringValue(attrs, "displayName", group.DisplayName)
	group.ExternalID = stringValue(attrs, "externalId", group.ExternalID)
	group.Members = memberValues(attrs["members"])
	if err := h.checkMembers(r, old, group.Members); err != nil {
		return protocol.Resource{}, err
	}
	if err := h.store.Groups().UpdateGroup(r.Context(), group); err != nil {
		return protocol.Resource{}, scimStoreError(err, id)
	}
	if err := h.replaceMembers(r, id, old, group.Members); err != nil {
		return protocol.Resource{}, err
	}
	return groupResource(group), nil
}
func (h *groupResourceHandler) Delete(r *http.Request, id string) error {
	return scimStoreError(h.store.Groups().DeleteGroup(r.Context(), id), id)
}

func (h *groupResourceHandler) Patch(r *http.Request, id string, operations []protocol.PatchOperation) (protocol.Resource, error) {
	group, err := h.store.Groups().GetGroupByID(r.Context(), id)
	if err != nil {
		return protocol.Resource{}, scimStoreError(err, id)
	}
	attrs := protocol.ResourceAttributes{"displayName": group.DisplayName, "members": memberMaps(group.Members)}
	for _, op := range operations {
		if op.Path != nil {
			attrs[op.Path.String()] = op.Value
		}
	}
	return h.Replace(r, id, attrs)
}

// checkMembers refuses unknown users before anything is written, so a bad id cannot leave a
// group half-rewritten.
func (h *groupResourceHandler) checkMembers(r *http.Request, old, next []string) error {
	for _, id := range next {
		if slices.Contains(old, id) {
			continue
		}
		user, err := h.store.Users().GetUserByID(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !user.SCIMManaged) {
			return protocolErrors.ScimErrorInvalidValue
		} else if err != nil {
			return err
		}
	}
	return nil
}

// replaceMembers writes only the difference between old and next.
func (h *groupResourceHandler) replaceMembers(r *http.Request, groupID string, old, next []string) error {
	for _, id := range old {
		if slices.Contains(next, id) {
			continue
		}
		if err := h.store.Groups().RemoveGroupMember(r.Context(), groupID, id); err != nil {
			return err
		}
	}
	for _, id := range next {
		if slices.Contains(old, id) {
			continue
		}
		if err := h.store.Groups().AddGroupMember(r.Context(), groupID, id); err != nil {
			return err
		}
	}
	return nil
}

func groupResource(group *store.Group) protocol.Resource {
	return protocol.Resource{ID: group.ID, ExternalID: optional.NewString(group.ExternalID), Attributes: protocol.ResourceAttributes{"displayName": group.DisplayName, "members": memberMaps(group.Members)}, Meta: protocol.Meta{Created: &group.CreatedAt, LastModified: &group.UpdatedAt}}
}
func memberMaps(ids []string) []interface{} {
	out := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]interface{}{"value": id})
	}
	return out
}
func memberValues(value interface{}) []string {
	var out []string
	for _, item := range interfaceSlice(value) {
		if m, ok := item.(map[string]interface{}); ok {
			if v, ok := m["value"].(string); ok && v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}
func primaryValue(value interface{}) string {
	for _, item := range interfaceSlice(value) {
		if m, ok := item.(map[string]interface{}); ok {
			if v, ok := m["value"].(string); ok {
				return v
			}
		}
		if v, ok := item.(string); ok {
			return v
		}
	}
	if v, ok := value.(string); ok {
		return v
	}
	return ""
}

func interfaceSlice(value interface{}) []interface{} {
	if values, ok := value.([]interface{}); ok {
		return values
	}
	return nil
}
func stringValue(attrs protocol.ResourceAttributes, key, fallback string) string {
	if value, ok := attrs[key].(string); ok && value != "" {
		return value
	}
	return fallback
}
func statusFromActive(attrs protocol.ResourceAttributes) string {
	if active, ok := attrs["active"].(bool); ok && !active {
		return "inactive"
	}
	return "active"
}
func scimStoreError(err error, id string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return protocolErrors.ScimErrorResourceNotFound(id)
	}
	if errors.Is(err, store.ErrAlreadyExists) {
		return protocolErrors.ScimErrorUniqueness
	}
	return err
}
