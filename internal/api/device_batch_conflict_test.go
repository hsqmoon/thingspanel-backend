package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"project/internal/middleware/response"
	"project/internal/model"
	"project/internal/query"
	"project/pkg/errcode"
	"project/pkg/global"
	"project/pkg/utils"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateDeviceBatchConflictResponses(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		existingTenant  string
		existingAccess  string
		existingName    string
		requestedName   string
		expectedCode    int
		expectedKind    string
		expectedMessage string
	}{
		{"ownership", "tenant-2", "access-2", "Device", "Device", errcode.CodeDeviceBatchOwnershipConflict, "ownership_conflict", "设备编号已存在"},
		{"attributes", "tenant-1", "access-1", "Existing", "Changed", errcode.CodeDeviceBatchAttributeConflict, "attribute_conflict", "设备编号与已有设备的属性冲突"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			databaseName := strings.ReplaceAll(t.Name(), "/", "-")
			db, err := gorm.Open(sqlite.Open("file:"+databaseName+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.AutoMigrate(
				&model.Device{}, &model.Group{}, &model.RGroupDevice{}, &model.DeviceBatchOutbox{},
				&model.ServiceAccess{}, &model.ServicePlugin{}, &model.DeviceConfig{},
			))
			require.NoError(t, db.Exec("CREATE UNIQUE INDEX devices_api_batch_number_unique ON devices(device_number)").Error)
			previousDB := global.DB
			global.DB = db
			query.SetDefault(db)
			t.Cleanup(func() {
				global.DB = previousDB
				if previousDB != nil {
					query.SetDefault(previousDB)
				}
			})

			now := time.Now().UTC()
			protocol := "TEST_PLUGIN"
			pluginConfig := `{"http_address":"plugin:8080"}`
			require.NoError(t, db.Create(&model.ServicePlugin{
				ID: "plugin-1", Name: "plugin", ServiceIdentifier: protocol, ServiceType: 2,
				ServiceConfig: &pluginConfig, CreateAt: now, UpdateAt: now,
			}).Error)
			require.NoError(t, db.Create(&model.ServiceAccess{
				ID: "access-1", Name: "access", ServicePluginID: "plugin-1", Voucher: "voucher",
				TenantID: "tenant-1", CreateAt: now, UpdateAt: now,
			}).Error)
			require.NoError(t, db.Create(&model.DeviceConfig{
				ID: "config-1", Name: "config", DeviceType: "1", ProtocolType: &protocol,
				TenantID: "tenant-1", CreatedAt: now, UpdatedAt: now,
			}).Error)
			existingName := testCase.existingName
			existingAccess := testCase.existingAccess
			require.NoError(t, db.Create(&model.Device{
				ID: "existing", Name: &existingName, Voucher: "voucher", TenantID: testCase.existingTenant,
				DeviceNumber: "device-conflict", ActivateFlag: "active", ServiceAccessID: &existingAccess,
			}).Error)

			handler, err := response.NewHandler("../../configs/messages.yaml", "../../configs/messages_str.yaml")
			require.NoError(t, err)
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.Use(handler.Middleware())
			router.POST("/api/v1/device/service/access/batch", func(c *gin.Context) {
				c.Set("claims", &utils.UserClaims{TenantID: "tenant-1"})
				(&DeviceApi{}).CreateDeviceBatch(c)
			})
			body, err := json.Marshal(model.BatchCreateDeviceReq{
				ServiceAccessId: "access-1",
				DeviceList: []model.BatchCreateDeviceItem{
					{DeviceName: testCase.requestedName, DeviceNumber: "device-conflict", DeviceConfigId: "config-1"},
					{DeviceName: "Rollback", DeviceNumber: "device-would-roll-back", DeviceConfigId: "config-1"},
				},
			})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/device/service/access/batch", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)

			require.Equal(t, http.StatusOK, recorder.Code)
			var result struct {
				Code    int                    `json:"code"`
				Message string                 `json:"message"`
				Data    map[string]interface{} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Equal(t, testCase.expectedCode, result.Code)
			require.Equal(t, testCase.expectedMessage, result.Message)
			require.Equal(t, testCase.expectedKind, result.Data["conflict_kind"])
			require.Equal(t, "device-conflict", result.Data["device_number"])

			var count int64
			require.NoError(t, db.Model(&model.Device{}).Where("device_number = ?", "device-would-roll-back").Count(&count).Error)
			require.Zero(t, count)
			require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}
