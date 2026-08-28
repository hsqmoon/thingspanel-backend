package service

import (
	"context"
	"io"
	"testing"
	"time"

	"project/internal/model"
	"project/internal/query"
	"project/pkg/global"
	"project/pkg/utils"

	"github.com/glebarez/sqlite"
	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type failingStatusPublisher struct{}

func (failingStatusPublisher) PublishStatusOffline(string, string) error { return nil }

func openErrorPropagationDB(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))
	global.DB = db
	query.SetDefault(db)
	return db
}

func TestGatewayRegisterRejectsCorruptStoredVoucher(t *testing.T) {
	db := openErrorPropagationDB(t, &model.Device{})
	require.NoError(t, db.Create(&model.Device{
		ID: "gateway-1", DeviceNumber: "serial-1", Voucher: "not-json", TenantID: "tenant-1",
		ActivateFlag: "active", IsEnabled: "enabled",
	}).Error)

	result, err := GroupApp.Device.GatewayRegister(model.GatewayRegisterReq{GatewayId: "serial-1"})
	require.Error(t, err)
	require.Empty(t, result.MqttUsername)
	require.Empty(t, result.MqttPassword)
}

func TestCheckDeviceNumberPropagatesDatabaseFailure(t *testing.T) {
	db := openErrorPropagationDB(t, &model.Device{})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	available, err := GroupApp.Device.CheckDeviceNumber("serial-1")
	require.Error(t, err)
	require.False(t, available)
}

func TestDeviceDebugDisablePropagatesRedisDeleteFailure(t *testing.T) {
	db := openErrorPropagationDB(t, &model.Device{})
	require.NoError(t, db.Create(&model.Device{
		ID: "device-1", DeviceNumber: "serial-1", Voucher: `{}`, TenantID: "tenant-1",
		ActivateFlag: "active", IsEnabled: "enabled",
	}).Error)
	previousRedis := global.REDIS
	global.REDIS = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond})
	t.Cleanup(func() {
		_ = global.REDIS.Close()
		global.REDIS = previousRedis
	})

	disabled := false
	_, err := GroupApp.DeviceDebug.SetDeviceDebug(context.Background(), "device-1", &model.SetDeviceDebugReq{Enabled: &disabled}, &utils.UserClaims{TenantID: "tenant-1"})
	require.Error(t, err)
}

func TestDeleteDataScriptKeepsDatabaseRowWhenCacheInvalidationFails(t *testing.T) {
	db := openErrorPropagationDB(t, &model.DataScript{})
	content := "return data"
	require.NoError(t, db.Create(&model.DataScript{
		ID: "script-1", Name: "script", DeviceConfigID: "config-1", ScriptType: "A", EnableFlag: "Y", Content: &content,
	}).Error)
	previousRedis := global.REDIS
	global.REDIS = redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond})
	t.Cleanup(func() {
		_ = global.REDIS.Close()
		global.REDIS = previousRedis
	})

	require.Error(t, GroupApp.DataScript.DeleteDataScript("script-1"))
	var count int64
	require.NoError(t, db.Model(&model.DataScript{}).Where("id = ?", "script-1").Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCheckMissingPluginsPropagatesDatabaseFailure(t *testing.T) {
	db := openErrorPropagationDB(t, &model.ServicePlugin{})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = checkMissingPlugins([]model.PluginDependency{{PluginName: "plugin-one"}})
	require.Error(t, err)
}

func TestHeartbeatStartFailsWhenRedisCannotBeConfigured(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	monitor := NewHeartbeatMonitor(client, failingStatusPublisher{}, logger)
	require.Error(t, monitor.Start())
}
