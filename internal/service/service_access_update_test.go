package service

import (
	"encoding/json"
	"testing"
	"time"

	"project/internal/dal"
	"project/internal/model"
	"project/internal/query"
	"project/pkg/errcode"
	"project/pkg/global"
	"project/pkg/utils"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestServiceAccessUpdateIsTenantScopedAndDurable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:service-access-update?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ServicePlugin{}, &model.ServiceAccess{}, &model.DeviceBatchOutbox{}))
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
	pluginConfig := `{"http_address":"plugin:8080"}`
	require.NoError(t, db.Create(&model.ServicePlugin{
		ID: "plugin-1", Name: "plugin", ServiceIdentifier: "service", ServiceType: 2,
		ServiceConfig: &pluginConfig, CreateAt: now, UpdateAt: now,
	}).Error)
	accessConfig := `{"old":true}`
	require.NoError(t, db.Create(&model.ServiceAccess{
		ID: "access-1", Name: "old", ServicePluginID: "plugin-1", Voucher: "old-voucher",
		ServiceAccessConfig: &accessConfig, TenantID: "tenant-1", CreateAt: now, UpdateAt: now,
	}).Error)

	name := "new"
	voucher := `{"token":"new"}`
	nextConfig := `{"mode":"automatic"}`
	update := &model.UpdateAccessReq{
		ID: "access-1", IdempotencyKey: "6e39103e-965b-43dc-9b49-f0cc50b8f5ba",
		Name: &name, Voucher: &voucher, ServiceAccessConfig: &nextConfig,
	}
	serviceAccess := &ServiceAccess{}
	err = serviceAccess.Update(update, &utils.UserClaims{TenantID: "tenant-2"})
	var codedError *errcode.Error
	require.ErrorAs(t, err, &codedError)
	require.Equal(t, errcode.CodeNoPermission, codedError.Code)

	var unchanged model.ServiceAccess
	require.NoError(t, db.First(&unchanged, "id = ?", "access-1").Error)
	require.Equal(t, "old", unchanged.Name)
	var outboxCount int64
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.Zero(t, outboxCount)

	require.NoError(t, serviceAccess.Update(update, &utils.UserClaims{TenantID: "tenant-1"}))
	var updated model.ServiceAccess
	require.NoError(t, db.First(&updated, "id = ?", "access-1").Error)
	require.Equal(t, name, updated.Name)
	require.Equal(t, voucher, updated.Voucher)
	require.NotNil(t, updated.ServiceAccessConfig)
	require.Equal(t, nextConfig, *updated.ServiceAccessConfig)

	var event model.DeviceBatchOutbox
	require.NoError(t, db.First(&event).Error)
	require.Equal(t, "tenant-1", event.TenantID)
	require.Equal(t, "access-1", event.ServiceAccessID)
	require.Equal(t, model.DeviceBatchDeliveryPending, event.Status)
	var payload struct {
		EventID         string `json:"event_id"`
		ServiceAccessID string `json:"service_access_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(event.Payload), &payload))
	require.Equal(t, event.EventID, payload.EventID)
	require.Equal(t, "access-1", payload.ServiceAccessID)

	require.NoError(t, serviceAccess.Update(update, &utils.UserClaims{TenantID: "tenant-1"}))
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.EqualValues(t, 1, outboxCount)

	differentName := "must-not-apply"
	conflicting := *update
	conflicting.Name = &differentName
	err = serviceAccess.Update(&conflicting, &utils.UserClaims{TenantID: "tenant-1"})
	require.ErrorAs(t, err, &codedError)
	require.Equal(t, errcode.CodeParamError, codedError.Code)
	require.NoError(t, db.First(&updated, "id = ?", "access-1").Error)
	require.Equal(t, name, updated.Name)
}

func TestServiceAccessUpdateRollsBackWhenOutboxCannotBeWritten(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:service-access-update-rollback?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ServiceAccess{}, &model.DeviceBatchOutbox{}))
	previousDB := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previousDB })

	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.ServiceAccess{
		ID: "access-1", Name: "old", ServicePluginID: "plugin-1", Voucher: "voucher",
		TenantID: "tenant-1", CreateAt: now, UpdateAt: now,
	}).Error)
	serviceAccessRefID := "access-1"
	require.NoError(t, db.Create(&model.DeviceBatchOutbox{
		EventID: "duplicate", IdempotencyKey: "duplicate", TenantID: "tenant-1",
		ServiceAccessID: "access-1", ServiceAccessRefID: &serviceAccessRefID,
		Destination: "plugin:8080", Payload: `{"request":"first"}`, Status: model.DeviceBatchDeliveryPending,
		NextRetryAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)

	err = dal.UpdateServiceAccessWithOutbox(
		"access-1",
		"tenant-1",
		map[string]interface{}{"name": "must-rollback"},
		&model.DeviceBatchOutbox{
			EventID: "duplicate", IdempotencyKey: "duplicate", Destination: "plugin:8080", Payload: `{}`,
		},
	)
	require.Error(t, err)
	var retained model.ServiceAccess
	require.NoError(t, db.First(&retained, "id = ?", "access-1").Error)
	require.Equal(t, "old", retained.Name)
}
