package api

import (
	"project/internal/service"
	"project/pkg/utils"

	"github.com/gin-gonic/gin"
)

type DashboardDeleteApi struct{}

func (*DashboardDeleteApi) GetDashboardDelete(c *gin.Context) {
	dashboardID := c.Param("dashboardId")
	claims := c.MustGet("claims").(*utils.UserClaims)
	tenantID, err := service.ResolveDashboardScope(claims)
	if err != nil {
		c.Error(err)
		return
	}
	data, err := service.GroupApp.DashboardDelete.Get(tenantID, dashboardID)
	if err != nil {
		c.Error(err)
		return
	}
	c.Set("data", data)
}

func (*DashboardDeleteApi) DeleteDashboard(c *gin.Context) {
	dashboardID := c.Param("dashboardId")
	claims := c.MustGet("claims").(*utils.UserClaims)
	tenantID, err := service.ResolveDashboardScope(claims)
	if err != nil {
		c.Error(err)
		return
	}
	data, err := service.GroupApp.DashboardDelete.Request(c, tenantID, dashboardID)
	if err != nil {
		c.Error(err)
		return
	}
	c.Set("data", data)
}
