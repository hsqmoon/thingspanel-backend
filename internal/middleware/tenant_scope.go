package middleware

import (
	"net/http"
	"strings"

	"project/internal/dal"
	"project/pkg/errcode"
	"project/pkg/utils"

	"github.com/gin-gonic/gin"
)

const tenantScopeHeader = "x-tenant-id"

var tenantExists = dal.TenantExists

var tenantBusinessPrefixes = []string{
	"/api/v1/alarm",
	"/api/v1/attribute",
	"/api/v1/board",
	"/api/v1/casbin",
	"/api/v1/command",
	"/api/v1/dashboard-menu",
	"/api/v1/data_script",
	"/api/v1/device",
	"/api/v1/device_config",
	"/api/v1/event",
	"/api/v1/expected",
	"/api/v1/message_push",
	"/api/v1/notification",
	"/api/v1/notification_group",
	"/api/v1/notification_history",
	"/api/v1/notification_services_config",
	"/api/v1/operation_logs",
	"/api/v1/ota",
	"/api/v1/role",
	"/api/v1/scene",
	"/api/v1/scene_automations",
	"/api/v1/service",
	"/api/v1/telemetry",
}

func TenantScope() gin.HandlerFunc {
	return func(c *gin.Context) {
		claims := c.MustGet("claims").(*utils.UserClaims)
		requestedTenantID := strings.TrimSpace(c.GetHeader(tenantScopeHeader))

		if claims.Authority != dal.SYS_ADMIN {
			if requestedTenantID != "" && requestedTenantID != claims.TenantID {
				c.Error(errcode.NewWithMessage(errcode.CodeNoPermission, "当前账号无权访问该租户"))
				c.Abort()
				return
			}
			c.Next()
			return
		}

		scopedClaims := *claims
		if requestedTenantID != "" {
			exists, err := tenantExists(requestedTenantID)
			if err != nil {
				c.Error(err)
				c.Abort()
				return
			}
			if !exists {
				c.Error(errcode.NewWithMessage(errcode.CodeParamError, "所选租户不存在"))
				c.Abort()
				return
			}
			scopedClaims.TenantID = requestedTenantID
		}
		c.Set("claims", &scopedClaims)

		if requestedTenantID == "" && c.Request.Method != http.MethodGet && isTenantBusinessPath(c.Request.URL.Path) {
			c.Error(errcode.NewWithMessage(errcode.CodeParamError, "修改租户资源前请先选择租户"))
			c.Abort()
			return
		}

		c.Next()
	}
}

func isTenantBusinessPath(path string) bool {
	for _, prefix := range tenantBusinessPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
