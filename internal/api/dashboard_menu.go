package api

import (
	model "project/internal/model"
	service "project/internal/service"
	utils "project/pkg/utils"

	"github.com/gin-gonic/gin"
)

type DashboardMenuApi struct{}

func (*DashboardMenuApi) GetDashboardMenu(c *gin.Context) {
	dashboardID := c.Param("dashboardId")
	userClaims := c.MustGet("claims").(*utils.UserClaims)
	tenantID, err := service.ResolveDashboardScope(userClaims)
	if err != nil {
		c.Error(err)
		return
	}

	data, err := service.GroupApp.DashboardMenu.GetTenantDashboardMenu(tenantID, dashboardID)
	if err != nil {
		c.Error(err)
		return
	}

	c.Set("data", data)
}

func (*DashboardMenuApi) SaveDashboardMenu(c *gin.Context) {
	dashboardID := c.Param("dashboardId")
	var req model.UpsertTenantDashboardMenuReq
	if !BindAndValidate(c, &req) {
		return
	}

	userClaims := c.MustGet("claims").(*utils.UserClaims)
	data, err := service.GroupApp.DashboardMenu.UpsertTenantDashboardMenu(userClaims, dashboardID, &req)
	if err != nil {
		c.Error(err)
		return
	}

	c.Set("data", data)
}

func (*DashboardMenuApi) DeleteDashboardMenu(c *gin.Context) {
	dashboardID := c.Param("dashboardId")
	userClaims := c.MustGet("claims").(*utils.UserClaims)
	tenantID, err := service.ResolveDashboardScope(userClaims)
	if err != nil {
		c.Error(err)
		return
	}

	err = service.GroupApp.DashboardMenu.DeleteTenantDashboardMenu(tenantID, dashboardID)
	if err != nil {
		c.Error(err)
		return
	}

	c.Set("data", nil)
}
