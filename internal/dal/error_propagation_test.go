package dal

import (
	"testing"
	"time"

	"project/internal/model"
	"project/internal/query"
	"project/pkg/global"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openDALErrorPropagationDB(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))
	global.DB = db
	query.SetDefault(db)
	return db
}

func TestDeleteDataScriptPropagatesDatabaseFailure(t *testing.T) {
	db := openDALErrorPropagationDB(t, &model.DataScript{})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	require.Error(t, DeleteDataScript("script-1"))
}

func TestAlarmHistoryRejectsMalformedDeviceList(t *testing.T) {
	db := openDALErrorPropagationDB(t, &model.AlarmHistory{}, &model.Device{})
	require.NoError(t, db.Create(&model.AlarmHistory{
		ID: "history-1", AlarmConfigID: "alarm-1", GroupID: "group-1", SceneAutomationID: "scene-1",
		Name: "alarm", AlarmStatus: "H", TenantID: "tenant-1", CreateAt: time.Now().UTC(), AlarmDeviceList: "not-json",
	}).Error)

	_, err := GetAlarmInfoHistoryByID("history-1")
	require.Error(t, err)
	_, err = GetDeviceIdsByAlarmConfigId("alarm-1")
	require.Error(t, err)
}

func TestGetDeviceAlarmStatusPropagatesDatabaseFailure(t *testing.T) {
	db := openDALErrorPropagationDB(t, &model.AlarmHistory{})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	status, err := GetDeviceAlarmStatus(&model.GetDeviceAlarmStatusReq{DeviceId: "device-1"})
	require.Error(t, err)
	require.False(t, status)
}

func TestGetAlarmNameFailsWithoutRedisInsteadOfReturningEmptyName(t *testing.T) {
	previousRedis := global.REDIS
	global.REDIS = nil
	t.Cleanup(func() { global.REDIS = previousRedis })

	name, err := GetAlarmNameWithCache("alarm-1")
	require.Error(t, err)
	require.Empty(t, name)
}
