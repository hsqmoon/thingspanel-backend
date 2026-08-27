package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"project/internal/model"
	"project/internal/query"
	"project/pkg/global"
	"project/pkg/utils"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type recordingDeviceBatchNotifier struct {
	err      error
	payloads []string
	hosts    []string
}

func (n *recordingDeviceBatchNotifier) Notify(ctx context.Context, _, payload, host string) ([]byte, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return nil, errors.New("notification context has no deadline")
	}
	n.payloads = append(n.payloads, payload)
	n.hosts = append(n.hosts, host)
	return []byte(`{"ok":true}`), n.err
}

func TestCreateDeviceBatchCommitsBeforeRetryableNotification(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:device-batch-service?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Device{}, &model.Group{}, &model.RGroupDevice{}, &model.DeviceBatchOutbox{},
		&model.ServiceAccess{}, &model.ServicePlugin{}, &model.DeviceConfig{},
	))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX devices_service_test_number_unique ON devices(device_number)").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX r_group_device_service_test_unique ON r_group_device(group_id, device_id)").Error)
	previousDB := global.DB
	global.DB = db
	query.SetDefault(db)
	t.Cleanup(func() { global.DB = previousDB })

	now := time.Now().UTC()
	root := "0"
	protocol := "TEST_PLUGIN"
	pluginConfig := `{"http_address":"plugin:8080"}`
	require.NoError(t, db.Create(&model.Group{
		ID: "root", ParentID: &root, Name: "root", TenantID: "tenant-1", CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.ServicePlugin{
		ID: "plugin-1", Name: "test", ServiceIdentifier: protocol, ServiceType: 2,
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

	notifier := &recordingDeviceBatchNotifier{err: errors.New("plugin unavailable")}
	deviceService := &Device{batchNotifier: notifier}
	req := model.BatchCreateDeviceReq{ServiceAccessId: "access-1"}
	req.DeviceList = append(req.DeviceList, model.BatchCreateDeviceItem{
		DeviceName: "Device A", DeviceNumber: "device-a", DeviceConfigId: "config-1",
	})
	claims := &utils.UserClaims{TenantID: "tenant-1"}

	data, err := deviceService.CreateDeviceBatch(req, claims)
	require.NoError(t, err)
	response := data.(model.BatchCreateDeviceRsp)
	require.Len(t, response.Devices, 1)
	require.Equal(t, model.DeviceBatchDeliveryPending, response.Delivery.Status)
	require.Equal(t, 1, response.Delivery.Attempts)
	require.NotNil(t, response.Delivery.LastError)
	require.Contains(t, *response.Delivery.LastError, "plugin unavailable")
	require.Len(t, notifier.payloads, 1)
	require.Equal(t, []string{"plugin:8080"}, notifier.hosts)

	var payload struct {
		EventID         string   `json:"event_id"`
		IdempotencyKey  string   `json:"idempotency_key"`
		ServiceAccessID string   `json:"service_access_id"`
		DeviceNumbers   []string `json:"device_numbers"`
	}
	require.NoError(t, json.Unmarshal([]byte(notifier.payloads[0]), &payload))
	require.Equal(t, response.Delivery.EventID, payload.EventID)
	require.Equal(t, payload.EventID, payload.IdempotencyKey)
	require.Equal(t, "access-1", payload.ServiceAccessID)
	require.Equal(t, []string{"device-a"}, payload.DeviceNumbers)

	var deviceCount, outboxCount int64
	require.NoError(t, db.Model(&model.Device{}).Count(&deviceCount).Error)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.EqualValues(t, 1, deviceCount)
	require.EqualValues(t, 1, outboxCount)

	notifier.err = nil
	updatedPluginConfig := `{"http_address":"plugin-new:9090"}`
	require.NoError(t, db.Model(&model.ServicePlugin{}).Where("id = ?", "plugin-1").
		Update("service_config", updatedPluginConfig).Error)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).
		Where("event_id = ?", response.Delivery.EventID).
		Update("next_retry_at", time.Now().UTC().Add(-time.Second)).Error)
	require.NoError(t, deviceService.DeliverPendingDeviceBatchNotifications(10))
	require.Len(t, notifier.payloads, 2)
	require.Equal(t, []string{"plugin:8080", "plugin-new:9090"}, notifier.hosts)

	var state model.DeviceBatchOutbox
	require.NoError(t, db.Where("event_id = ?", response.Delivery.EventID).Take(&state).Error)
	require.Equal(t, model.DeviceBatchDeliveryDelivered, state.Status)
	require.Equal(t, 2, state.Attempts)
	require.Equal(t, "plugin-new:9090", state.Destination)

	retryData, err := deviceService.CreateDeviceBatch(req, claims)
	require.NoError(t, err)
	retryResponse := retryData.(model.BatchCreateDeviceRsp)
	require.Equal(t, response.Devices[0].ID, retryResponse.Devices[0].ID)
	require.Equal(t, model.DeviceBatchDeliveryDelivered, retryResponse.Delivery.Status)
	require.Len(t, notifier.payloads, 2)
	require.NoError(t, db.Model(&model.Device{}).Count(&deviceCount).Error)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.EqualValues(t, 1, deviceCount)
	require.EqualValues(t, 1, outboxCount)

	conflictingReq := req
	conflictingReq.DeviceList = append([]model.BatchCreateDeviceItem(nil), req.DeviceList...)
	conflictingReq.DeviceList[0].DeviceName = "Different Name"
	_, err = deviceService.CreateDeviceBatch(conflictingReq, claims)
	require.Error(t, err)

	require.NoError(t, db.Where("id = ?", response.Devices[0].ID).Delete(&model.Device{}).Error)
	recreatedData, err := deviceService.CreateDeviceBatch(req, claims)
	require.NoError(t, err)
	recreated := recreatedData.(model.BatchCreateDeviceRsp)
	require.NotEqual(t, response.Devices[0].ID, recreated.Devices[0].ID)
	require.NotEqual(t, response.Delivery.EventID, recreated.Delivery.EventID)
	require.Equal(t, model.DeviceBatchDeliveryDelivered, recreated.Delivery.Status)
	require.Len(t, notifier.payloads, 3)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.EqualValues(t, 2, outboxCount)
}

func TestCreateDeviceBatchPrevalidatesDependencies(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:device-batch-prevalidate?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Device{}, &model.DeviceBatchOutbox{}, &model.ServiceAccess{}, &model.ServicePlugin{}, &model.DeviceConfig{},
	))
	previousDB := global.DB
	global.DB = db
	query.SetDefault(db)
	t.Cleanup(func() { global.DB = previousDB })

	now := time.Now().UTC()
	pluginConfig := `{"http_address":"plugin:8080"}`
	require.NoError(t, db.Create(&model.ServicePlugin{
		ID: "plugin-1", Name: "test", ServiceIdentifier: "TEST_PLUGIN", ServiceType: 2,
		ServiceConfig: &pluginConfig, CreateAt: now, UpdateAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.ServiceAccess{
		ID: "access-1", Name: "access", ServicePluginID: "plugin-1", Voucher: "voucher",
		TenantID: "tenant-1", CreateAt: now, UpdateAt: now,
	}).Error)

	req := model.BatchCreateDeviceReq{ServiceAccessId: "access-1"}
	req.DeviceList = append(req.DeviceList, model.BatchCreateDeviceItem{
		DeviceName: "Device A", DeviceNumber: "device-a", DeviceConfigId: "missing-config",
	})
	_, err = (&Device{batchNotifier: &recordingDeviceBatchNotifier{}}).
		CreateDeviceBatch(req, &utils.UserClaims{TenantID: "tenant-1"})
	require.Error(t, err)

	var deviceCount, outboxCount int64
	require.NoError(t, db.Model(&model.Device{}).Count(&deviceCount).Error)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.Zero(t, deviceCount)
	require.Zero(t, outboxCount)
}
