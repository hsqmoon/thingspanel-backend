package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"project/pkg/constant"
	"project/pkg/utils"

	"github.com/gin-gonic/gin"
)

func TestTenantBusinessPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/api/v1/device", want: true},
		{path: "/api/v1/device/123", want: true},
		{path: "/api/v1/device_config", want: true},
		{path: "/api/v1/user", want: false},
		{path: "/api/v1/system/metrics", want: false},
		{path: "/api/v1/devices", want: false},
	}

	for _, tt := range tests {
		if got := isTenantBusinessPath(tt.path); got != tt.want {
			t.Fatalf("isTenantBusinessPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestTenantResourceIDs(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/device/group/relation?group_id=group-query-id",
		bytes.NewBufferString(`{"id":"body-id","device_id_list":["device-a","device-b"],"name":"ignored"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	context.Request = request

	ids, err := tenantResourceIDs(context)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"group-query-id": true,
		"body-id":        true,
		"device-a":       true,
		"device-b":       true,
	}
	for _, id := range ids {
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("missing ids: %v; got %v", want, ids)
	}
}

func TestTenantResourceIDsRejectsInvalidURLEscapes(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		wantError string
	}{
		{name: "path", target: "/api/v1/device/%25ZZ", wantError: "请求路径包含无效转义"},
		{name: "query", target: "/api/v1/device?id=%ZZ", wantError: "请求查询参数包含无效转义"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodGet, tt.target, nil)
			context.Request = request

			_, err := tenantResourceIDs(context)
			if err == nil || err.Error() != tt.wantError {
				t.Fatalf("expected %q, got %v", tt.wantError, err)
			}
		})
	}
}

func TestTenantScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalTenantExists := tenantExists
	tenantExists = func(tenantID string) (bool, error) { return tenantID == "tenant-a", nil }
	t.Cleanup(func() { tenantExists = originalTenantExists })

	tests := []struct {
		name       string
		method     string
		path       string
		claims     utils.UserClaims
		header     string
		wantCalled bool
		wantTenant string
	}{
		{name: "system global read", method: http.MethodGet, path: "/api/v1/device", claims: utils.UserClaims{Authority: "SYS_ADMIN"}, wantCalled: true},
		{name: "system selected tenant", method: http.MethodGet, path: "/api/v1/device", claims: utils.UserClaims{Authority: "SYS_ADMIN"}, header: "tenant-a", wantCalled: true, wantTenant: "tenant-a"},
		{name: "system global write rejected", method: http.MethodPost, path: "/api/v1/device", claims: utils.UserClaims{Authority: "SYS_ADMIN"}, wantCalled: false},
		{name: "system global dashboard read", method: http.MethodGet, path: "/api/v1/thingsvis-dashboard/dash", claims: utils.UserClaims{Authority: "SYS_ADMIN"}, wantCalled: true, wantTenant: constant.DashboardSystemScope},
		{name: "system global dashboard delete", method: http.MethodDelete, path: "/api/v1/thingsvis-dashboard/dash", claims: utils.UserClaims{Authority: "SYS_ADMIN"}, wantCalled: true, wantTenant: constant.DashboardSystemScope},
		{name: "system global dashboard menu write", method: http.MethodPut, path: "/api/v1/dashboard-menu/dash", claims: utils.UserClaims{Authority: "SYS_ADMIN"}, wantCalled: true, wantTenant: constant.DashboardSystemScope},
		{name: "system selected dashboard tenant", method: http.MethodDelete, path: "/api/v1/thingsvis-dashboard/dash", claims: utils.UserClaims{Authority: "SYS_ADMIN"}, header: "tenant-a", wantCalled: true, wantTenant: "tenant-a"},
		{name: "tenant own scope", method: http.MethodGet, path: "/api/v1/device", claims: utils.UserClaims{Authority: "TENANT_ADMIN", TenantID: "tenant-a"}, header: "tenant-a", wantCalled: true, wantTenant: "tenant-a"},
		{name: "tenant dashboard own scope", method: http.MethodDelete, path: "/api/v1/thingsvis-dashboard/dash", claims: utils.UserClaims{Authority: "TENANT_ADMIN", TenantID: "tenant-a"}, wantCalled: true, wantTenant: "tenant-a"},
		{name: "tenant cross scope rejected", method: http.MethodGet, path: "/api/v1/device", claims: utils.UserClaims{Authority: "TENANT_ADMIN", TenantID: "tenant-a"}, header: "tenant-b", wantCalled: false},
		{name: "tenant user business access rejected", method: http.MethodGet, path: "/api/v1/device", claims: utils.UserClaims{Authority: "TENANT_USER", TenantID: "tenant-a"}, wantCalled: false},
		{name: "tenant user personal access allowed", method: http.MethodGet, path: "/api/v1/user/detail", claims: utils.UserClaims{Authority: "TENANT_USER", TenantID: "tenant-a"}, wantCalled: true, wantTenant: "tenant-a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			gotTenant := ""
			router := gin.New()
			router.Use(func(c *gin.Context) {
				claims := tt.claims
				c.Set("claims", &claims)
			})
			router.Use(TenantScope())
			router.Handle(tt.method, tt.path, func(c *gin.Context) {
				called = true
				gotTenant = c.MustGet("claims").(*utils.UserClaims).TenantID
			})

			request := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.header != "" {
				request.Header.Set(tenantScopeHeader, tt.header)
			}
			router.ServeHTTP(httptest.NewRecorder(), request)

			if called != tt.wantCalled {
				t.Fatalf("handler called = %v, want %v", called, tt.wantCalled)
			}
			if gotTenant != tt.wantTenant {
				t.Fatalf("tenant = %q, want %q", gotTenant, tt.wantTenant)
			}
		})
	}
}
