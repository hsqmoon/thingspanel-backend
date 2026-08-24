package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"project/internal/model"

	"github.com/gin-gonic/gin"
)

func TestBindAndValidateDeleteQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodDelete, "/api/v1/device/group/relation?group_id=group-1&device_id=device-1", nil)
	var request model.DeleteDeviceGroupRelationReq
	if !BindAndValidate(context, &request) {
		t.Fatal("DELETE query parameters should bind without a JSON body")
	}
	if request.GroupId != "group-1" || request.DeviceId != "device-1" {
		t.Fatalf("unexpected request: %+v", request)
	}
}
