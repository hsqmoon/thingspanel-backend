package dal

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"project/internal/model"
	"project/pkg/global"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func requireDeviceBatchPostgresURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("NSNR_TEST_BACKEND_DATABASE_URL")
	if databaseURL != "" {
		return databaseURL
	}
	if os.Getenv("NSNR_REQUIRE_BACKEND_POSTGRES_TESTS") == "1" {
		t.Fatal("NSNR_TEST_BACKEND_DATABASE_URL is required by the formal PostgreSQL test gate")
	}
	t.Skip("NSNR_TEST_BACKEND_DATABASE_URL is not set")
	return ""
}

func openDeviceBatchPostgresSchema(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	databaseURL := requireDeviceBatchPostgresURL(t)
	adminDB, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
	require.NoError(t, err)
	adminSQLDB, err := adminDB.DB()
	require.NoError(t, err)
	schema := "outbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.True(t, strings.HasPrefix(schema, "outbox_test_"))
	require.NoError(t, adminDB.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error)
	t.Cleanup(func() {
		require.NoError(t, adminDB.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema)).Error)
		require.NoError(t, adminSQLDB.Close())
	})

	parsedURL, err := url.Parse(databaseURL)
	require.NoError(t, err)
	require.NotEmpty(t, parsedURL.Scheme, "NSNR_TEST_BACKEND_DATABASE_URL must be a PostgreSQL URL")
	queryValues := parsedURL.Query()
	queryValues.Set("search_path", schema)
	parsedURL.RawQuery = queryValues.Encode()
	db, err := gorm.Open(postgres.Open(parsedURL.String()), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(12)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	if len(models) > 0 {
		require.NoError(t, db.AutoMigrate(models...))
	}
	return db
}

func TestDeviceBatchOutboxPostgresMigration25(t *testing.T) {
	databaseURL := requireDeviceBatchPostgresURL(t)
	if os.Getenv("NSNR_REQUIRE_BACKEND_MIGRATION25") != "1" {
		t.Skip("migration 25 validation is only required by the formal build gate")
	}
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	var tableName sql.NullString
	require.NoError(t, db.Raw(`SELECT to_regclass('public.device_batch_outbox')::text`).Scan(&tableName).Error)
	require.True(t, tableName.Valid)
	require.Equal(t, "device_batch_outbox", tableName.String)
	var constraints int64
	require.NoError(t, db.Raw(`
		SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'public.device_batch_outbox'::regclass
		  AND conname IN (
			'device_batch_outbox_pkey',
			'device_batch_outbox_idempotency_key_unique',
			'device_batch_outbox_status_check',
			'device_batch_outbox_pending_reference_check',
			'device_batch_outbox_service_access_fk'
		  )`).Scan(&constraints).Error)
	require.EqualValues(t, 5, constraints)
	var deliveryIndex int64
	require.NoError(t, db.Raw(`
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'public'
		  AND tablename = 'device_batch_outbox'
		  AND indexname = 'device_batch_outbox_delivery_idx'`).Scan(&deliveryIndex).Error)
	require.EqualValues(t, 1, deliveryIndex)
}

func TestDeviceBatchOutboxPostgresConcurrentClaimAndFencing(t *testing.T) {
	db := openDeviceBatchPostgresSchema(t, &model.DeviceBatchOutbox{})
	previousDB := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previousDB })
	databaseNow, err := GetDatabaseTime()
	require.NoError(t, err)
	require.NoError(t, db.Create([]model.DeviceBatchOutbox{
		{EventID: "event-a", IdempotencyKey: "event-a", TenantID: "tenant-1", ServiceAccessID: "access-1", Destination: "plugin:8080", Payload: `{}`, Status: model.DeviceBatchDeliveryPending, NextRetryAt: databaseNow, CreatedAt: databaseNow, UpdatedAt: databaseNow},
		{EventID: "event-b", IdempotencyKey: "event-b", TenantID: "tenant-1", ServiceAccessID: "access-1", Destination: "plugin:8080", Payload: `{}`, Status: model.DeviceBatchDeliveryPending, NextRetryAt: databaseNow, CreatedAt: databaseNow, UpdatedAt: databaseNow},
	}).Error)

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	var locked model.DeviceBatchOutbox
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_id = ?", "event-a").Take(&locked).Error)
	second, err := ClaimDeviceBatchOutbox("")
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, "event-b", second.EventID, "SKIP LOCKED must bypass the row held by another connection")
	require.NoError(t, lockTx.Rollback().Error)
	first, err := ClaimDeviceBatchOutbox("event-a")
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NotNil(t, first.ClaimToken)
	require.NotNil(t, second.ClaimToken)

	staleToken := *first.ClaimToken
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Where("event_id = ?", first.EventID).
		Update("next_retry_at", gorm.Expr("CURRENT_TIMESTAMP - INTERVAL '1 second'")).Error)
	reclaimed, err := ClaimDeviceBatchOutbox(first.EventID)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	require.NotEqual(t, staleToken, *reclaimed.ClaimToken)
	require.Equal(t, first.Attempts+1, reclaimed.Attempts)
	databaseNow, err = GetDatabaseTime()
	require.NoError(t, err)
	require.Error(t, MarkDeviceBatchOutboxDelivered(first.EventID, staleToken, first.Attempts, "stale:8080", databaseNow))
	require.NoError(t, MarkDeviceBatchOutboxDelivered(first.EventID, *reclaimed.ClaimToken, reclaimed.Attempts, "plugin:8080", databaseNow))
}

func TestDeviceBatchOutboxPostgresDeleteRecreateFencing(t *testing.T) {
	db := openDeviceBatchPostgresSchema(t,
		&model.Device{}, &model.Group{}, &model.RGroupDevice{}, &model.DeviceBatchOutbox{},
	)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX devices_test_number_unique ON devices(device_number)").Error)
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX r_group_device_test_unique ON r_group_device(group_id, device_id)").Error)

	now := time.Now().UTC()
	rootID := "0"
	require.NoError(t, db.Create(&model.Group{
		ID: "root", ParentID: &rootID, Name: "root", TenantID: "tenant-1", CreatedAt: now, UpdatedAt: now,
	}).Error)
	serviceAccessID := "access-1"
	deviceName := "device"
	require.NoError(t, db.Create(&model.Device{
		ID: "existing-device", Name: &deviceName, Voucher: "existing-voucher", TenantID: "tenant-1",
		DeviceNumber: "device-1", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID,
	}).Error)

	const advisoryKey int64 = 818251731
	require.NoError(t, db.Exec(fmt.Sprintf(`
		CREATE FUNCTION block_test_relation_insert() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(%d);
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER block_test_relation_insert
		BEFORE INSERT ON r_group_device
		FOR EACH ROW EXECUTE FUNCTION block_test_relation_insert()`, advisoryKey)).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	blocker, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, blocker.Close()) })
	_, err = blocker.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", advisoryKey)
	require.NoError(t, err)
	lockHeld := true
	t.Cleanup(func() {
		if lockHeld {
			_, _ = blocker.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryKey)
		}
	})

	type batchResult struct {
		devices []*model.Device
		outbox  *model.DeviceBatchOutbox
		err     error
	}
	resultChannel := make(chan batchResult, 1)
	requested := []*model.Device{{
		ID: "retry-device", Name: &deviceName, Voucher: "retry-voucher", TenantID: "tenant-1",
		DeviceNumber: "device-1", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID,
	}}
	go func() {
		devices, outbox, createErr := createDeviceBatch(db, requested, &model.DeviceBatchOutbox{
			TenantID: "tenant-1", ServiceAccessID: serviceAccessID, Destination: "plugin:8080",
			Status: model.DeviceBatchDeliveryPending,
		})
		resultChannel <- batchResult{devices, outbox, createErr}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int64
		require.NoError(t, db.Raw(`
			SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND objid = ? AND NOT granted`, advisoryKey).Scan(&waiting).Error)
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("batch creation did not reach the deterministic relation-insert barrier")
		}
		time.Sleep(20 * time.Millisecond)
	}

	deleteResults := make(chan *gorm.DB, 1)
	go func() {
		deleteResults <- db.Where("id = ?", "existing-device").Delete(&model.Device{})
	}()
	deadline = time.Now().Add(10 * time.Second)
	for {
		select {
		case earlyDelete := <-deleteResults:
			require.NoError(t, earlyDelete.Error)
			t.Fatal("a concurrent delete completed before the batch transaction released its device row lock")
		default:
		}
		var waiting int64
		require.NoError(t, db.Raw(`
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND query LIKE 'DELETE FROM "devices"%'
			  AND wait_event_type = 'Lock'`).Scan(&waiting).Error)
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent delete did not enter a database lock wait")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err = blocker.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryKey)
	require.NoError(t, err)
	lockHeld = false

	var created batchResult
	select {
	case created = <-resultChannel:
	case <-time.After(10 * time.Second):
		t.Fatal("batch creation did not complete after releasing the test barrier")
	}
	require.NoError(t, created.err)
	require.Len(t, created.devices, 1)
	require.Equal(t, "existing-device", created.devices[0].ID)
	require.NotNil(t, created.outbox)

	var deleteResult *gorm.DB
	select {
	case deleteResult = <-deleteResults:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent delete did not complete after batch creation committed")
	}
	require.NoError(t, deleteResult.Error)
	require.EqualValues(t, 1, deleteResult.RowsAffected)
	require.NoError(t, db.Where("device_id = ?", "existing-device").Delete(&model.RGroupDevice{}).Error)
	recreatedRequest := []*model.Device{{
		ID: "recreated-device", Name: &deviceName, Voucher: "recreated-voucher", TenantID: "tenant-1",
		DeviceNumber: "device-1", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID,
	}}
	recreated, recreatedOutbox, err := createDeviceBatch(db, recreatedRequest, &model.DeviceBatchOutbox{
		TenantID: "tenant-1", ServiceAccessID: serviceAccessID, Destination: "plugin:8080",
		Status: model.DeviceBatchDeliveryPending,
	})
	require.NoError(t, err)
	require.Equal(t, "recreated-device", recreated[0].ID)
	require.NotEqual(t, created.outbox.EventID, recreatedOutbox.EventID)
}

func TestDeviceBatchOutboxPostgresOverlappingBatches(t *testing.T) {
	db := openDeviceBatchPostgresSchema(t, &model.Device{}, &model.Group{}, &model.DeviceBatchOutbox{})
	require.NoError(t, db.Exec("CREATE UNIQUE INDEX devices_test_number_unique ON devices(device_number)").Error)
	serviceAccessID := "access-1"

	type batchResult struct {
		devices []*model.Device
		outbox  *model.DeviceBatchOutbox
		err     error
	}
	for iteration := 0; iteration < 10; iteration++ {
		prefix := fmt.Sprintf("overlap-%02d", iteration)
		nameA, nameB, nameC := "A", "B", "C"
		batchA := []*model.Device{
			{ID: prefix + "-a-b", Name: &nameB, Voucher: prefix + "-voucher-b-a", TenantID: "tenant-1", DeviceNumber: prefix + "-b", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
			{ID: prefix + "-a-a", Name: &nameA, Voucher: prefix + "-voucher-a-a", TenantID: "tenant-1", DeviceNumber: prefix + "-a", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
		}
		batchB := []*model.Device{
			{ID: prefix + "-b-a", Name: &nameA, Voucher: prefix + "-voucher-a-b", TenantID: "tenant-1", DeviceNumber: prefix + "-a", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
			{ID: prefix + "-b-c", Name: &nameC, Voucher: prefix + "-voucher-c-b", TenantID: "tenant-1", DeviceNumber: prefix + "-c", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
			{ID: prefix + "-b-b", Name: &nameB, Voucher: prefix + "-voucher-b-b", TenantID: "tenant-1", DeviceNumber: prefix + "-b", IsEnabled: "enabled", ActivateFlag: "active", ServiceAccessID: &serviceAccessID},
		}

		start := make(chan struct{})
		results := make(chan batchResult, 2)
		for _, batch := range [][]*model.Device{batchA, batchB} {
			batch := batch
			go func() {
				<-start
				devices, outbox, createErr := createDeviceBatch(db, batch, &model.DeviceBatchOutbox{
					TenantID: "tenant-1", ServiceAccessID: serviceAccessID, Destination: "plugin:8080",
					Status: model.DeviceBatchDeliveryPending,
				})
				results <- batchResult{devices, outbox, createErr}
			}()
		}
		close(start)
		first, second := <-results, <-results
		require.NoError(t, first.err)
		require.NoError(t, second.err)
		require.NotEqual(t, first.outbox.EventID, second.outbox.EventID)

		persistedIDs := make(map[string]string)
		for _, result := range []batchResult{first, second} {
			for _, device := range result.devices {
				if existingID := persistedIDs[device.DeviceNumber]; existingID != "" {
					require.Equal(t, existingID, device.ID)
				} else {
					persistedIDs[device.DeviceNumber] = device.ID
				}
			}
		}
		require.Len(t, persistedIDs, 3)
	}

	var devices, outboxes int64
	require.NoError(t, db.Model(&model.Device{}).Count(&devices).Error)
	require.NoError(t, db.Model(&model.DeviceBatchOutbox{}).Count(&outboxes).Error)
	require.EqualValues(t, 30, devices)
	require.EqualValues(t, 20, outboxes)
}
