package httpserver

import "testing"

func TestWorkspaceForUserType(t *testing.T) {
	tests := []struct {
		name     string
		userType string
		want     string
		wantOK   bool
	}{
		{name: "personal user", userType: "personal_user", want: "portal", wantOK: true},
		{name: "operator", userType: "operator", want: "admin", wantOK: true},
		{name: "platform owner", userType: "platform_owner", want: "admin", wantOK: true},
		{name: "unknown", userType: "guest", want: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := workspaceForUserType(tt.userType)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("workspaceForUserType(%q) = %q, %v; want %q, %v", tt.userType, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestHasAdminPermission(t *testing.T) {
	operatorAllowed := []adminPermission{
		adminPermSelf,
		adminPermOverview,
		adminPermUsersRead,
		adminPermUsersReset,
		adminPermModels,
		adminPermPool,
		adminPermUpstream,
		adminPermProxies,
		adminPermOAuth,
		adminPermUsageRead,
		adminPermAudit,
	}

	for _, permission := range operatorAllowed {
		if !hasAdminPermission("operator", permission) {
			t.Fatalf("operator should have permission %q", permission)
		}
	}

	ownerOnly := []adminPermission{
		adminPermPlatformOwner,
	}
	for _, permission := range ownerOnly {
		if hasAdminPermission("operator", permission) {
			t.Fatalf("operator should not have permission %q", permission)
		}
		if !hasAdminPermission("platform_owner", permission) {
			t.Fatalf("platform_owner should have permission %q", permission)
		}
	}

	if hasAdminPermission("personal_user", adminPermSelf) {
		t.Fatal("personal_user should not have admin permission")
	}
}
