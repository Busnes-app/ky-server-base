package scim

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

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
	// mu serializes SCIM user writes, so two PATCHes read from the same row cannot each write
	// back the other's stale fields (the store compares only role, status and subject).
	mu sync.Mutex
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
	if user.SSOSubject != "" {
		existing, err := h.store.Users().GetUserBySSO(r.Context(), "kysignon", user.SSOSubject)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return protocol.Resource{}, err
		}
		if err == nil && !existing.SCIMManaged {
			return h.takeOver(r, existing, attrs)
		}
	}
	err := h.store.Users().CreateUser(r.Context(), user)
	if errors.Is(err, store.ErrAlreadyExists) && user.SSOSubject == "" {
		// Without an externalId, the account KyIdentity sign-in created under this name.
		existing, lookupErr := h.store.Users().GetUserByUsername(r.Context(), username)
		if lookupErr == nil && existing.SSOProvider == "kysignon" && !existing.SCIMManaged {
			return h.takeOver(r, existing, attrs)
		}
	}
	if err != nil {
		return protocol.Resource{}, scimStoreError(err, user.ID)
	}
	_ = h.store.Audit().LogAudit(r.Context(), &store.AuditRecord{UserID: user.ID, Action: "scim.user.create", Resource: user.Username})
	return userResource(user), nil
}

// takeOver answers a create for a person who already signed in through KyIdentity: their
// account becomes SCIM-managed (keeping its id and KyIdentity subject) and the create's
// attributes are applied as a PUT would apply them. Local accounts never reach here.
func (h *userResourceHandler) takeOver(r *http.Request, user *store.User, attrs protocol.ResourceAttributes) (protocol.Resource, error) {
	if roles, sent := attrs["roles"]; sent {
		if err := h.checkRole(user.Role, roleValue(roles)); err != nil {
			return protocol.Resource{}, err
		}
	}
	if err := h.store.Users().ClaimForSCIM(r.Context(), user.ID, user.SSOSubject); errors.Is(err, store.ErrConflict) {
		return protocol.Resource{}, protocolErrors.ScimErrorUniqueness
	} else if err != nil {
		return protocol.Resource{}, err
	}
	_ = h.store.Audit().LogAudit(r.Context(), &store.AuditRecord{UserID: user.ID, Action: "scim.user.adopt", Resource: user.Username})
	return h.Replace(r, user.ID, attrs)
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
	return h.update(r, id, func(user *store.User) error {
		user.Username, _ = attrs["userName"].(string)
		user.Email = primaryValue(attrs["emails"])
		user.DisplayName = stringValue(attrs, "displayName", user.Username)
		if roles, sent := attrs["roles"]; sent {
			user.Role = roleValue(roles)
		}
		user.Status = statusFromActive(attrs)
		user.SSOSubject = stringValue(attrs, "externalId", user.SSOSubject)
		return nil
	})
}

func (h *userResourceHandler) Delete(r *http.Request, id string) error {
	if _, err := h.managedUser(r, id); err != nil {
		return err
	}
	return scimStoreError(h.store.Users().DeleteUser(r.Context(), id), id)
}

func (h *userResourceHandler) Patch(r *http.Request, id string, operations []protocol.PatchOperation) (protocol.Resource, error) {
	return h.update(r, id, func(user *store.User) error {
		return applyUserPatch(user, operations)
	})
}

// applyUserPatch applies PATCH operations to user in order. A filtered emails/roles path
// applies when its selector matches the single value userResource emits; a replace that
// selects nothing is noTarget. Paths into attributes this adapter does not store
// (name.*, phoneNumbers, addresses, ...) are ignored, never refused.
func applyUserPatch(user *store.User, operations []protocol.PatchOperation) error {
	for _, op := range operations {
		if op.Path == nil {
			if op.Op != protocol.PatchOperationRemove {
				if values, ok := op.Value.(map[string]interface{}); ok {
					applyUserValues(user, values)
				}
			}
			continue
		}
		attr := strings.ToLower(op.Path.AttributePath.AttributeName)
		sub := op.Path.SubAttributeName()
		if sub == "" {
			sub = op.Path.AttributePath.SubAttributeName()
		}
		var stored map[string]interface{}
		switch attr {
		case "emails":
			if user.Email != "" {
				stored = map[string]interface{}{"value": user.Email, "type": "work", "primary": true}
			}
		case "roles":
			stored = map[string]interface{}{"value": user.Role, "primary": true}
		default:
			if op.Path.ValueExpression != nil || sub != "" {
				continue
			}
			if op.Op != protocol.PatchOperationRemove {
				applyUserValue(user, attr, op.Value)
			} else if attr == "displayname" {
				user.DisplayName = user.Username
			} else if attr == "externalid" {
				user.SSOSubject = "" // update keeps an adopted account's subject
			}
			continue
		}
		if sub != "" && !strings.EqualFold(sub, "value") {
			continue // type, primary and display are not stored
		}
		if op.Path.ValueExpression != nil {
			if stored == nil && op.Op == protocol.PatchOperationAdd {
				stored = map[string]interface{}{"value": primaryValue(op.Value), "type": "work", "primary": true}
			}
			if stored == nil || !selects(op.Path.ValueExpression, stored) {
				if op.Op == protocol.PatchOperationReplace {
					return protocolErrors.ScimErrorNoTarget
				}
				continue
			}
		}
		switch {
		case op.Op != protocol.PatchOperationRemove:
			applyUserValue(user, attr, op.Value)
		case attr == "emails":
			user.Email = ""
		default:
			user.Role = "user"
		}
	}
	return nil
}

// eqSelector matches a value filter of the single form `attr eq value`, as the library's
// filter AST prints it (string values Go-quoted).
var eqSelector = regexp.MustCompile(`^(\w+) eq ("(?:[^"\\]|\\.)*"|true|false)$`)

func selector(e interface{}) (attr, value string, ok bool) {
	m := eqSelector.FindStringSubmatch(fmt.Sprint(e))
	if m == nil {
		return "", "", false
	}
	value = m[2]
	if v, err := strconv.Unquote(value); err == nil {
		value = v
	}
	return strings.ToLower(m[1]), value, true
}

// selects reports whether filter e selects element; Entra's `primary eq "True"` matches.
func selects(e interface{}, element map[string]interface{}) bool {
	attr, value, ok := selector(e)
	got, present := element[attr]
	return ok && present && strings.EqualFold(fmt.Sprint(got), value)
}

// update reads a managed user, applies change and writes it back. The write is a
// compare-and-swap on role and status, revoking sessions when either changes; when a
// concurrent write got there first, it re-reads and re-applies, so a stale read can never put
// back a role or status another request just took away.
func (h *userResourceHandler) update(r *http.Request, id string, change func(*store.User) error) (protocol.Resource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for attempt := 0; ; attempt++ {
		user, err := h.managedUser(r, id)
		if err != nil {
			return protocol.Resource{}, err
		}
		oldRole, oldStatus, oldSubject := user.Role, user.Status, user.SSOSubject
		if err := change(user); err != nil {
			return protocol.Resource{}, err
		}
		if err := h.checkRole(oldRole, user.Role); err != nil {
			return protocol.Resource{}, err
		}
		// externalId is the subject KyIdentity adoption matches. It is written with the profile,
		// compared against the subject read, and only while the row is SCIM's own: a stale write
		// never puts back an old value, a refused one changes nothing, and an adopted account
		// keeps its sign-in subject.
		if user.SSOProvider != "scim" {
			user.SSOSubject = oldSubject
		}
		err = h.store.Users().UpdateSCIMProfile(r.Context(), user, oldRole, oldStatus, oldSubject)
		if errors.Is(err, store.ErrConflict) && attempt < 3 {
			continue
		}
		if err != nil {
			return protocol.Resource{}, scimStoreError(err, id)
		}
		return userResource(user), nil
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
	case "externalid": // stored only while the row is SCIM's own (update)
		if v, ok := value.(string); ok {
			user.SSOSubject = v
		}
	case "roles", "role":
		user.Role = roleValue(value)
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

// groupResourceHandler serializes membership writes in this process (mu): PATCH computes the
// new membership from a read, so two PATCHes interleaving would drop one's change. One server
// process per database is the supported deployment.
type groupResourceHandler struct {
	store store.Store
	mu    sync.Mutex
}

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

// groupFilter is the one group filter an IdP sends before creating a group. displayName
// matches case-insensitively through GetGroupByName; externalId (caseExact) scans at most
// maxFilterCandidates groups.
var groupFilter = regexp.MustCompile(`(?i)^\s*(displayName|externalId)\s+eq\s+"([^"]+)"\s*$`)

func (h *groupResourceHandler) GetAll(r *http.Request, params protocol.ListRequestParams) (protocol.Page, error) {
	var groups []*store.Group
	var total int
	var err error
	if raw := r.URL.Query().Get("filter"); raw == "" {
		groups, total, err = h.store.Groups().ListGroups(r.Context(), params.StartIndex-1, params.Count)
	} else {
		groups, err = h.filterGroups(r, raw)
		total = len(groups)
		start := min(max(params.StartIndex-1, 0), total)
		end := total
		if params.Count > 0 {
			end = min(start+params.Count, total)
		}
		groups = groups[start:end]
	}
	if err != nil {
		return protocol.Page{}, err
	}
	resources := make([]protocol.Resource, 0, len(groups))
	for _, group := range groups {
		resources = append(resources, groupResource(group))
	}
	return protocol.Page{TotalResults: total, Resources: resources}, nil
}

func (h *groupResourceHandler) filterGroups(r *http.Request, raw string) ([]*store.Group, error) {
	match := groupFilter.FindStringSubmatch(raw)
	if match == nil || strings.TrimSpace(match[2]) == "" {
		return nil, protocolErrors.ScimErrorInvalidFilter
	}
	if strings.EqualFold(match[1], "displayName") {
		group, err := h.store.Groups().GetGroupByName(r.Context(), match[2])
		if errors.Is(err, store.ErrNotFound) || (err == nil && !strings.EqualFold(group.DisplayName, match[2])) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []*store.Group{group}, nil
	}
	var out []*store.Group
	for offset := 0; ; {
		page, total, err := h.store.Groups().ListGroups(r.Context(), offset, 200)
		if err != nil {
			return nil, err
		}
		if total > maxFilterCandidates {
			return nil, protocolErrors.ScimErrorTooMany
		}
		for _, group := range page {
			if group.ExternalID == match[2] {
				out = append(out, group)
			}
		}
		offset += len(page)
		if len(page) == 0 || offset >= total {
			return out, nil
		}
	}
}

func (h *groupResourceHandler) Replace(r *http.Request, id string, attrs protocol.ResourceAttributes) (protocol.Resource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.replace(r, id, attrs)
}

func (h *groupResourceHandler) replace(r *http.Request, id string, attrs protocol.ResourceAttributes) (protocol.Resource, error) {
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

// Patch applies each operation by type to the current group, then stores the result through
// Replace (member check, difference-only writes). Member sub-attribute paths and any member
// filter but `members[value eq "id"]` with remove are refused before anything is written.
func (h *groupResourceHandler) Patch(r *http.Request, id string, operations []protocol.PatchOperation) (protocol.Resource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	group, err := h.store.Groups().GetGroupByID(r.Context(), id)
	if err != nil {
		return protocol.Resource{}, scimStoreError(err, id)
	}
	displayName, externalID, members := group.DisplayName, group.ExternalID, slices.Clone(group.Members)
	apply := func(op, attr string, value interface{}) {
		switch attr {
		case "displayname":
			if v, ok := value.(string); ok && op != protocol.PatchOperationRemove {
				displayName = v
			}
		case "externalid":
			if v, ok := value.(string); ok && op != protocol.PatchOperationRemove {
				externalID = v
			}
		case "members":
			switch op {
			case protocol.PatchOperationAdd:
				for _, m := range memberValues(value) {
					if !slices.Contains(members, m) {
						members = append(members, m)
					}
				}
			case protocol.PatchOperationRemove:
				if value == nil {
					members = nil
				}
				gone := memberValues(value)
				members = slices.DeleteFunc(members, func(m string) bool { return slices.Contains(gone, m) })
			default:
				members = memberValues(value)
			}
		}
	}
	for _, op := range operations {
		switch {
		case op.Path == nil:
			values, _ := op.Value.(map[string]interface{})
			for key, value := range values {
				attr := strings.ToLower(key)
				if attr != "members" && strings.Contains(attr, "members") {
					return protocol.Resource{}, protocolErrors.ScimErrorInvalidPath
				}
				apply(op.Op, attr, value)
			}
		case strings.EqualFold(op.Path.AttributePath.AttributeName, "members"):
			if op.Path.SubAttribute != nil || op.Path.AttributePath.SubAttribute != nil {
				return protocol.Resource{}, protocolErrors.ScimErrorInvalidPath
			}
			if op.Path.ValueExpression == nil {
				apply(op.Op, "members", op.Value)
				continue
			}
			attr, target, ok := selector(op.Path.ValueExpression)
			if !ok || attr != "value" || op.Op != protocol.PatchOperationRemove {
				return protocol.Resource{}, protocolErrors.ScimErrorInvalidPath
			}
			apply(op.Op, "members", []interface{}{map[string]interface{}{"value": target}})
		case op.Path.ValueExpression == nil && op.Path.SubAttribute == nil:
			apply(op.Op, strings.ToLower(op.Path.AttributePath.AttributeName), op.Value)
		}
	}
	return h.replace(r, id, protocol.ResourceAttributes{"displayName": displayName, "externalId": externalID, "members": memberMaps(members)})
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

// roleValue is the role a sent roles value grants: an empty one means "user", so a PUT or
// replace that empties the roles takes the role away instead of leaving it in place. A PUT
// that omits roles altogether leaves the role alone (IdPs that do not map roles omit it).
func roleValue(value interface{}) string {
	if v := primaryValue(value); v != "" {
		return v
	}
	return "user"
}

func primaryValue(value interface{}) string {
	if m, ok := value.(map[string]interface{}); ok {
		v, _ := m["value"].(string)
		return v
	}
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
