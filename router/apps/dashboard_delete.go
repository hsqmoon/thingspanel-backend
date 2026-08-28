package apps

import (
	"project/internal/api"

	"github.com/gin-gonic/gin"
)

type DashboardDelete struct{}

func (*DashboardDelete) Init(router *gin.RouterGroup) {
	url := router.Group("thingsvis-dashboard")
	url.GET(":dashboardId", api.Controllers.DashboardDeleteApi.GetDashboardDelete)
	url.DELETE(":dashboardId", api.Controllers.DashboardDeleteApi.DeleteDashboard)
}
