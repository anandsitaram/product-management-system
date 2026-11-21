package rbac

import "testing"

func TestMatrix(t *testing.T) {
	cases := []struct {
		role string
		p    Permission
		want bool
	}{
		{RoleAdmin, UserManage, true}, {RoleManager, UserManage, false}, {RoleViewer, UserManage, false},
		{RoleAdmin, ProductDelete, true}, {RoleManager, ProductDelete, false},
		{RoleManager, ProductWrite, true}, {RoleViewer, ProductWrite, false},
		{RoleManager, MediaManage, true}, {RoleViewer, MediaManage, false},
		{RoleViewer, CatalogRead, true}, {RoleManager, AuditRead, false}, {RoleAdmin, AuditRead, true},
		{"UNKNOWN", CatalogRead, false}, {"", CatalogRead, false},
	}
	for _, c := range cases {
		if got := Can(c.role, c.p); got != c.want {
			t.Errorf("Can(%q,%q)=%v want %v", c.role, c.p, got, c.want)
		}
	}
}
