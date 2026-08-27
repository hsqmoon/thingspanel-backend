package service

import (
	"testing"
	"time"

	"project/internal/model"
	"project/pkg/errcode"
	"project/pkg/global"
	"project/pkg/utils"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestServiceAccessDeleteEnforcesTenantAndDependencyErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:service-access-delete?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ServiceAccess{}, &model.Device{}, &model.DeviceBatchOutbox{}))
	previousDB := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previousDB })

	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.ServiceAccess{
		ID: "access-1", Name: "access", ServicePluginID: "plugin", Voucher: "voucher",
		TenantID: "tenant-1", CreateAt: now, UpdateAt: now,
	}).Error)
	serviceAccessRefID := "access-1"
	require.NoError(t, db.Create(&model.DeviceBatchOutbox{
		EventID: "event-1", IdempotencyKey: "event-1", TenantID: "tenant-1",
		ServiceAccessID: "access-1", ServiceAccessRefID: &serviceAccessRefID,
		Destination: "plugin:8080", Payload: `{}`, Status: model.DeviceBatchDeliveryPending,
		NextRetryAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)

	deleteService := &ServiceAccess{}
	err = deleteService.Delete("access-1", nil)
	var codedError *errcode.Error
	require.ErrorAs(t, err, &codedError)
	require.Equal(t, errcode.CodeNoPermission, codedError.Code)
	err = deleteService.Delete("access-1", &utils.UserClaims{TenantID: "tenant-2"})
	require.ErrorAs(t, err, &codedError)
	require.Equal(t, errcode.CodeNoPermission, codedError.Code)
	err = deleteService.Delete("access-1", &utils.UserClaims{TenantID: "tenant-1"})
	require.ErrorAs(t, err, &codedError)
	require.Equal(t, errcode.CodeParamError, codedError.Code)

	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Where("event_id = ?", "event-1").
		Updates(map[string]interface{}{"status": model.DeviceBatchDeliveryDelivered, "delivered_at": now}).Error)
	deviceName := "device"
	require.NoError(t, db.Create(&model.Device{
		ID: "device-1", Name: &deviceName, Voucher: "device-voucher", TenantID: "tenant-1",
		DeviceNumber: "device-1", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessRefID,
	}).Error)
	err = deleteService.Delete("access-1", &utils.UserClaims{TenantID: "tenant-1"})
	require.ErrorAs(t, err, &codedError)
	require.Equal(t, 200064, codedError.Code)
}
