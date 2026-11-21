// Package rbac maps roles to permissions and enforces them per route.
package rbac

import (
	"net/http"

	"pms/internal/middleware/auth"
	"pms/internal/shared/apperrors"
	"pms/internal/shared/response"
)

const (
	RoleAdmin   = "ADMIN"
	RoleManager = "MANAGER"
	RoleViewer  = "VIEWER"
)

var Roles = []string{RoleAdmin, RoleManager, RoleViewer}

type Permission string

const (
	CatalogRead   Permission = "catalog:read"  // products, categories, labels, image metadata
	ProductWrite  Permission = "product:write" // create/update
	ProductDelete Permission = "product:delete"
	CatalogManage Permission = "catalog:manage" // categories + labels mutations
	MediaManage   Permission = "media:manage"   // upload/delete/primary
	UserManage    Permission = "user:manage"
	AuditRead     Permission = "audit:read"
)

var matrix = map[string]map[Permission]bool{
	RoleAdmin:   {CatalogRead: true, ProductWrite: true, ProductDelete: true, CatalogManage: true, MediaManage: true, UserManage: true, AuditRead: true},
	RoleManager: {CatalogRead: true, ProductWrite: true, CatalogManage: true, MediaManage: true},
	RoleViewer:  {CatalogRead: true},
}

func Can(role string, p Permission) bool { return matrix[role][p] }

func ValidRole(role string) bool { _, ok := matrix[role]; return ok }

// Require returns 403 unless the authenticated principal's role grants p. Must run after auth.
func Require(p Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pr, ok := auth.From(r.Context())
			if !ok {
				response.Error(w, r, apperrors.Unauthorized("Authentication required"))
				return
			}
			if !Can(pr.Role, p) {
				response.Error(w, r, apperrors.Forbidden("You do not have permission to perform this action"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
