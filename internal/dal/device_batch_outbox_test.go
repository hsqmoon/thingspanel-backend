package dal

import (
	"errors"
	"testing"
	"time"

	"project/internal/model"
	"project/pkg/global"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCreateDeviceBatchIsAtomicAndIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:device-batch-atomic?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Device{}, &model.Group{}, &model.RGroupDevice{}, &model.DeviceBatchOutbox{},
	))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX devices_test_number_unique ON devices(device_number)").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX r_group_device_test_unique ON r_group_device(group_id, device_id)").Error)

	now := time.Now().UTC()
	root := "0"
	require.NoError(t, db.Create(&model.Group{
		ID: "root", ParentID: &root, Name: "root", TenantID: "tenant-1", CreatedAt: now, UpdatedAt: now,
	}).Error)
	serviceAccessID := "access-1"
	newBatch := func(idPrefix string) ([]*model.Device, *model.DeviceBatchOutbox) {
		nameA, nameB := "A", "B"
		return []*model.Device{
				{ID: idPrefix + "-a", Name: &nameA, Voucher: idPrefix + "-voucher-a", TenantID: "tenant-1", DeviceNumber: "device-a", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
				{ID: idPrefix + "-b", Name: &nameB, Voucher: idPrefix + "-voucher-b", TenantID: "tenant-1", DeviceNumber: "device-b", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
			}, &model.DeviceBatchOutbox{
				TenantID: "tenant-1", ServiceAccessID: serviceAccessID, Destination: "plugin:8080",
				Status:      model.DeviceBatchDeliveryPending,
				NextRetryAt: now, CreatedAt: now, UpdatedAt: now,
			}
	}

	devices, outbox := newBatch("first")
	created, createdOutbox, err := createDeviceBatch(db, devices, outbox)
	require.NoError(t, err)
	require.Len(t, created, 2)
	require.Len(t, createdOutbox.EventID, 64)

	retryDevices, retryOutbox := newBatch("retry")
	retried, retriedOutbox, err := createDeviceBatch(db, retryDevices, retryOutbox)
	require.NoError(t, err)
	require.Equal(t, []string{"first-a", "first-b"}, []string{retried[0].ID, retried[1].ID})
	require.Equal(t, createdOutbox.EventID, retriedOutbox.EventID)

	var deviceCount, outboxCount, relationCount int64
	require.NoError(t, db.Model(&model.Device{}).Count(&deviceCount).Error)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.NoError(t, db.Model(&model.RGroupDevice{}).Count(&relationCount).Error)
	require.EqualValues(t, 2, deviceCount)
	require.EqualValues(t, 1, outboxCount)
	require.EqualValues(t, 2, relationCount)

	otherAccess := "other-access"
	conflictName := "conflict"
	require.NoError(t, db.Create(&model.Device{
		ID: "conflict", Name: &conflictName, Voucher: "conflict-voucher", TenantID: "tenant-2",
		DeviceNumber: "device-conflict", ActivateFlag: "active", ServiceAccessID: &otherAccess,
	}).Error)
	newName := "new"
	conflictingDevices := []*model.Device{
		{ID: "would-roll-back", Name: &newName, Voucher: "new-voucher", TenantID: "tenant-1", DeviceNumber: "device-new", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
		{ID: "ignored", Name: &conflictName, Voucher: "ignored-voucher", TenantID: "tenant-1", DeviceNumber: "device-conflict", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
	}
	conflictingOutbox := *outbox
	_, _, err = createDeviceBatch(db, conflictingDevices, &conflictingOutbox)
	require.ErrorIs(t, err, ErrDeviceBatchOwnershipConflict)
	var batchErr *DeviceBatchError
	require.ErrorAs(t, err, &batchErr)
	require.Equal(t, DeviceBatchOwnershipConflict, batchErr.Kind)
	require.Equal(t, "device-conflict", batchErr.DeviceNumber)
	require.NoError(t, db.Model(&model.Device{}).Where("device_number = ?", "device-new").Count(&deviceCount).Error)
	require.Zero(t, deviceCount)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.EqualValues(t, 1, outboxCount)

	attributeConflictName := "different-name"
	attributeConflictDevices := []*model.Device{
		{ID: "attribute-new", Name: &attributeConflictName, Voucher: "new-voucher", TenantID: "tenant-1", DeviceNumber: "device-a", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
		{ID: "attribute-would-roll-back", Name: &newName, Voucher: "new-voucher-2", TenantID: "tenant-1", DeviceNumber: "device-attribute-new", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
	}
	attributeConflictOutbox := *outbox
	_, _, err = createDeviceBatch(db, attributeConflictDevices, &attributeConflictOutbox)
	require.ErrorIs(t, err, ErrDeviceBatchAttributeConflict)
	require.ErrorAs(t, err, &batchErr)
	require.Equal(t, DeviceBatchAttributeConflict, batchErr.Kind)
	require.Equal(t, "device-a", batchErr.DeviceNumber)
	require.NoError(t, db.Model(&model.Device{}).Where("device_number = ?", "device-attribute-new").Count(&deviceCount).Error)
	require.Zero(t, deviceCount)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxCount).Error)
	require.EqualValues(t, 1, outboxCount)

	_, _, err = createDeviceBatch(db, nil, outbox)
	require.ErrorIs(t, err, ErrDeviceBatchInvalidInput)
	require.False(t, errors.Is(err, ErrDeviceBatchOwnershipConflict))
	require.ErrorAs(t, err, &batchErr)
	require.Equal(t, DeviceBatchInvalidInput, batchErr.Kind)
	require.Equal(t, "devices", batchErr.Field)
}

func TestCreateDeviceBatchDoesNotClassifyDatabaseFailureAsConflict(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:device-batch-database-error?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Device{}, &model.DeviceBatchOutbox{}))
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX devices_database_error_number_unique ON devices(device_number)").Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	serviceAccessID := "access-1"
	name := "Device"
	_, _, err = createDeviceBatch(db, []*model.Device{{
		ID: "device-1", Name: &name, Voucher: "voucher", TenantID: "tenant-1",
		DeviceNumber: "device-1", ActivateFlag: "active", ServiceAccessID: &serviceAccessID,
	}}, &model.DeviceBatchOutbox{TenantID: "tenant-1", ServiceAccessID: serviceAccessID})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrDeviceBatchInvalidInput))
	require.False(t, errors.Is(err, ErrDeviceBatchOwnershipConflict))
	require.False(t, errors.Is(err, ErrDeviceBatchAttributeConflict))
}

func TestDeviceBatchOutboxClaimRetryAndComplete(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:device-batch-claim?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.DeviceBatchOutbox{}))
	previousDB := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previousDB })

	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.DeviceBatchOutbox{
		EventID: "event-claim", IdempotencyKey: "event-claim", TenantID: "tenant-1", ServiceAccessID: "access-1",
		Destination: "plugin:8080", Payload: `{}`, Status: model.DeviceBatchDeliveryPending,
		NextRetryAt: now.Add(-time.Second), CreatedAt: now, UpdatedAt: now,
	}).Error)

	claimed, err := ClaimDeviceBatchOutbox("event-claim")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, 1, claimed.Attempts)
	require.Equal(t, model.DeviceBatchDeliveryProcessing, claimed.Status)
	require.NotNil(t, claimed.ClaimToken)

	require.NoError(t, MarkDeviceBatchOutboxFailed("event-claim", *claimed.ClaimToken, claimed.Attempts, "plugin:8080", "temporary", now.Add(-time.Second), now))
	claimed, err = ClaimDeviceBatchOutbox("event-claim")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, 2, claimed.Attempts)
	require.NoError(t, MarkDeviceBatchOutboxDelivered("event-claim", *claimed.ClaimToken, claimed.Attempts, "plugin:8080", now))

	state, err := GetDeviceBatchOutbox("event-claim")
	require.NoError(t, err)
	require.Equal(t, model.DeviceBatchDeliveryDelivered, state.Status)
	require.Nil(t, state.LastError)
	claimed, err = ClaimDeviceBatchOutbox("event-claim")
	require.NoError(t, err)
	require.Nil(t, claimed)

	staleClaimToken := "stale-claim"
	require.NoError(t, db.Create(&model.DeviceBatchOutbox{
		EventID: "event-crashed", IdempotencyKey: "event-crashed", TenantID: "tenant-1", ServiceAccessID: "access-1",
		Destination: "plugin:8080", Payload: `{}`, Status: model.DeviceBatchDeliveryProcessing, ClaimToken: &staleClaimToken, Attempts: 3,
		NextRetryAt: now.Add(-time.Second), CreatedAt: now, UpdatedAt: now,
	}).Error)
	claimed, err = ClaimDeviceBatchOutbox("event-crashed")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, "event-crashed", claimed.EventID)
	require.Equal(t, 4, claimed.Attempts)
	require.Equal(t, model.DeviceBatchDeliveryProcessing, claimed.Status)
	require.NotEqual(t, staleClaimToken, *claimed.ClaimToken)
	require.Error(t, MarkDeviceBatchOutboxDelivered("event-crashed", staleClaimToken, 3, "stale:8080", now))
	require.NoError(t, MarkDeviceBatchOutboxDelivered("event-crashed", *claimed.ClaimToken, claimed.Attempts, "plugin:8080", now))

	oldDeliveredAt := now.Add(-31 * 24 * time.Hour)
	recentDeliveredAt := now.Add(-24 * time.Hour)
	require.NoError(t, db.Create([]model.DeviceBatchOutbox{
		{EventID: "event-old", IdempotencyKey: "event-old", TenantID: "tenant-1", ServiceAccessID: "access-1", Destination: "plugin:8080", Payload: `{}`, Status: model.DeviceBatchDeliveryDelivered, DeliveredAt: &oldDeliveredAt, NextRetryAt: now, CreatedAt: now, UpdatedAt: now},
		{EventID: "event-recent", IdempotencyKey: "event-recent", TenantID: "tenant-1", ServiceAccessID: "access-1", Destination: "plugin:8080", Payload: `{}`, Status: model.DeviceBatchDeliveryDelivered, DeliveredAt: &recentDeliveredAt, NextRetryAt: now, CreatedAt: now, UpdatedAt: now},
	}).Error)
	require.NoError(t, CleanupDeliveredDeviceBatchOutbox(30*24*time.Hour))
	var count int64
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Where("event_id = ?", "event-old").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Where("event_id = ?", "event-recent").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestDeleteServiceAccessChecksTenantDevicesAndOutboxAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:service-access-outbox?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ServiceAccess{}, &model.Device{}, &model.DeviceBatchOutbox{}))
	previousDB := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previousDB })

	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.ServiceAccess{
		ID: "access-pending", Name: "access", ServicePluginID: "plugin", Voucher: "voucher",
		TenantID: "tenant-1", CreateAt: now, UpdateAt: now,
	}).Error)
	serviceAccessRefID := "access-pending"
	require.NoError(t, db.Create(&model.DeviceBatchOutbox{
		EventID: "event-pending", IdempotencyKey: "event-pending", TenantID: "tenant-1",
		ServiceAccessID: "access-pending", ServiceAccessRefID: &serviceAccessRefID, Destination: "plugin:8080", Payload: `{}`,
		Status: model.DeviceBatchDeliveryPending, NextRetryAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error)

	require.ErrorIs(t, DeleteServiceAccess("access-pending", "tenant-2"), gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, db.Model(&model.ServiceAccess{}).Where("id = ?", "access-pending").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.ErrorIs(t, DeleteServiceAccess("access-pending", "tenant-1"), ErrPendingDeviceBatchDelivery)

	deliveredAt := now
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Where("event_id = ?", "event-pending").
		Updates(map[string]interface{}{"status": model.DeviceBatchDeliveryDelivered, "delivered_at": deliveredAt}).Error)
	deviceName := "device"
	require.NoError(t, db.Create(&model.Device{
		ID: "device-1", Name: &deviceName, Voucher: "voucher", TenantID: "tenant-1", DeviceNumber: "device-1",
		IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessRefID,
	}).Error)
	require.ErrorIs(t, DeleteServiceAccess("access-pending", "tenant-1"), ErrServiceAccessHasDevices)
	var retained model.DeviceBatchOutbox
	require.NoError(t, db.Where("event_id = ?", "event-pending").Take(&retained).Error)
	require.NotNil(t, retained.ServiceAccessRefID, "a rejected delete must not detach delivered history")
	require.NoError(t, db.Where("id = ? AND tenant_id = ?", "device-1", "tenant-1").Delete(&model.Device{}).Error)

	require.NoError(t, DeleteServiceAccess("access-pending", "tenant-1"))
	require.NoError(t, db.Model(&model.ServiceAccess{}).Where("id = ?", "access-pending").Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Where("event_id = ?", "event-pending").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Where("event_id = ?", "event-pending").Take(&retained).Error)
	require.Nil(t, retained.ServiceAccessRefID)
}
