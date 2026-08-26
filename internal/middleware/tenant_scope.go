package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
			if !guardTenantResources(c, claims.TenantID) {
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
		if !guardTenantResources(c, scopedClaims.TenantID) {
			return
		}

		c.Next()
	}
}

func guardTenantResources(c *gin.Context, tenantID string) bool {
	if tenantID == "" || !isTenantBusinessPath(c.Request.URL.Path) {
		return true
	}

	ids, err := tenantResourceIDs(c)
	if err != nil {
		c.Error(errcode.NewWithMessage(errcode.CodeParamError, err.Error()))
		c.Abort()
		return false
	}
	resourceType, resourceID, found, err := dal.FindForeignTenantResource(ids, tenantID)
	if err != nil {
		c.Error(err)
		c.Abort()
		return false
	}
	if found {
		c.Error(errcode.NewWithMessage(errcode.CodeNoPermission, fmt.Sprintf("无权访问其他租户的%s资源 %s", resourceType, resourceID)))
		c.Abort()
		return false
	}
	return true
}

func tenantResourceIDs(c *gin.Context) ([]string, error) {
	ids := make([]string, 0, 8)
	for _, segment := range strings.Split(c.Request.URL.Path, "/") {
		segment, _ = url.PathUnescape(segment)
		if len(segment) >= 8 {
			ids = append(ids, segment)
		}
	}
	for key, values := range c.Request.URL.Query() {
		if !isResourceIDKey(key) {
			continue
		}
		ids = append(ids, values...)
	}

	contentType := strings.ToLower(c.GetHeader("Content-Type"))
	if c.Request.Body == nil || (!strings.Contains(contentType, "application/json") && !strings.Contains(contentType, "+json")) {
		return ids, nil
	}
	const maxBodySize = 16 << 20
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败")
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > maxBodySize {
		return nil, fmt.Errorf("请求体超过 16 MiB 限制")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return ids, nil
	}
	var payload interface{}
	if json.Unmarshal(body, &payload) != nil {
		return ids, nil
	}
	collectResourceIDs(payload, false, &ids)
	return ids, nil
}

func collectResourceIDs(value interface{}, collect bool, ids *[]string) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			collectResourceIDs(child, isResourceIDKey(key), ids)
		}
	case []interface{}:
		for _, child := range typed {
			collectResourceIDs(child, collect, ids)
		}
	case string:
		if collect && typed != "" {
			*ids = append(*ids, typed)
		}
	}
}

func isResourceIDKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "id" || key == "action_target" || key == "trigger_source" || strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "id") || strings.HasSuffix(key, "_ids") || strings.HasSuffix(key, "ids") || strings.HasSuffix(key, "_id_list")
}

func isTenantBusinessPath(path string) bool {
	for _, prefix := range tenantBusinessPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}
