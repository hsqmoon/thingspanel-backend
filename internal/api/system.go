package api

import (
	"context"
	"net/http"
	"project/pkg/global"
	"project/pkg/utils"
	"time"

	"github.com/gin-gonic/gin"
)

type SystemApi struct{}

// /api/v1/systime
func (*SystemApi) HandleSystime(c *gin.Context) {
	c.Set("data", map[string]interface{}{"systime": utils.GetSecondTimestamp()})
}

// 健康检查 /health
func (*SystemApi) HealthCheck(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if global.DB == nil || global.REDIS == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"data": gin.H{"status": "not-ready"}})
		return
	}
	sqlDB, err := global.DB.DB()
	if err != nil || sqlDB.PingContext(ctx) != nil || global.REDIS.Ping(ctx).Err() != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"data": gin.H{"status": "degraded"}})
		return
	}
	c.Set("data", gin.H{"status": "ready"})
}

func (*SystemApi) LivenessCheck(c *gin.Context) {
	c.Set("data", gin.H{"status": "alive"})
}

// 获取系统版本 /api/v1/sys_version
func (*SystemApi) HandleSysVersion(c *gin.Context) {
	c.Set("data", map[string]interface{}{"version": global.SYSTEM_VERSION})
}
