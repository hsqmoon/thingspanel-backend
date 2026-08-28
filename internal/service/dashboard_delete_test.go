package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"project/internal/dal"
	"project/internal/model"
	"project/pkg/global"
	"project/pkg/utils"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResolveDashboardScope(t *testing.T) {
	t.Run("system admin global space", func(t *testing.T) {
		scope, err := ResolveDashboardScope(&utils.UserClaims{Authority: "SYS_ADMIN"})
		require.NoError(t, err)
		require.Equal(t, SystemAdminDashboardScopeID, scope)
	})

	t.Run("system admin selected tenant", func(t *testing.T) {
		scope, err := ResolveDashboardScope(&utils.UserClaims{Authority: "SYS_ADMIN", TenantID: "tenant-selected"})
		require.NoError(t, err)
		require.Equal(t, "tenant-selected", scope)
	})

	t.Run("tenant user own space", func(t *testing.T) {
		scope, err := ResolveDashboardScope(&utils.UserClaims{Authority: "TENANT_ADMIN", TenantID: "tenant-a"})
		require.NoError(t, err)
		require.Equal(t, "tenant-a", scope)
	})

	t.Run("tenant without scope rejected", func(t *testing.T) {
		_, err := ResolveDashboardScope(&utils.UserClaims{Authority: "TENANT_ADMIN"})
		require.Error(t, err)
	})

	t.Run("missing claims rejected", func(t *testing.T) {
		_, err := ResolveDashboardScope(nil)
		require.Error(t, err)
	})
}

func openDashboardDeleteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.TenantDashboardMenu{}, &model.DashboardDeleteJob{}))
	previousDB := global.DB
	global.DB = db
	t.Cleanup(func() {
		global.DB = previousDB
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		require.NoError(t, sqlDB.Close())
	})
	return db
}

func createDashboardDeleteTestMenu(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.TenantDashboardMenu{
		ID: "menu-1", TenantID: "tenant-1", DashboardID: "dashboard-1", DashboardName: "Dashboard",
		MenuName: "Operations", ParentCode: "home", Sort: 1, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}).Error)
}

func TestDashboardDeleteDefersMenuRemovalUntilThingsVisSucceeds(t *testing.T) {
	db := openDashboardDeleteTestDB(t)
	createDashboardDeleteTestMenu(t, db)
	var calls atomic.Int32
	thingsVis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/api/v1/dashboards/dashboard-1", r.URL.Path)
		require.Equal(t, "tenant-1", r.Header.Get("X-Tenant-ID"))
		if calls.Add(1) == 1 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(thingsVis.Close)
	service := DashboardDelete{thingsVis: NewThingsVisClientWithBaseURL(thingsVis.URL)}

	response, err := service.Request(context.Background(), "tenant-1", "dashboard-1")
	require.NoError(t, err)
	require.Equal(t, model.DashboardDeletePending, response.Status)
	require.Equal(t, 1, response.Attempts)
	status, err := service.Get("tenant-1", "dashboard-1")
	require.NoError(t, err)
	require.Equal(t, response, status)
	otherTenantStatus, err := service.Get("tenant-2", "dashboard-1")
	require.NoError(t, err)
	require.Nil(t, otherTenantStatus)
	var menuCount int64
	require.NoError(t, db.Model(&model.TenantDashboardMenu{}).Count(&menuCount).Error)
	require.EqualValues(t, 1, menuCount)
	job, err := dal.GetDashboardDelete(response.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.DashboardDeletePending, job.Status)
	require.NotNil(t, job.LastError)

	now, err := dal.GetDatabaseTime()
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.DashboardDeleteJob{}).Where("id = ?", response.OperationID).
		Update("next_retry_at", now.Add(-time.Second)).Error)
	require.NoError(t, service.DeliverPending(1))
	job, err = dal.GetDashboardDelete(response.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.DashboardDeleteDelivered, job.Status)
	require.Equal(t, 2, job.Attempts)
	status, err = service.Get("tenant-1", "dashboard-1")
	require.NoError(t, err)
	require.Equal(t, model.DashboardDeleteDelivered, status.Status)
	require.Equal(t, 2, status.Attempts)
	require.NoError(t, db.Model(&model.TenantDashboardMenu{}).Count(&menuCount).Error)
	require.Zero(t, menuCount)
}

func TestDashboardDeleteTreatsThingsVisNotFoundAsDelivered(t *testing.T) {
	db := openDashboardDeleteTestDB(t)
	createDashboardDeleteTestMenu(t, db)
	var calls atomic.Int32
	thingsVis := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.NotFound(w, nil)
	}))
	t.Cleanup(thingsVis.Close)
	service := DashboardDelete{thingsVis: NewThingsVisClientWithBaseURL(thingsVis.URL)}

	response, err := service.Request(context.Background(), "tenant-1", "dashboard-1")
	require.NoError(t, err)
	require.Equal(t, model.DashboardDeleteDelivered, response.Status)
	require.Equal(t, int32(1), calls.Load())
	var menuCount int64
	require.NoError(t, db.Model(&model.TenantDashboardMenu{}).Where("tenant_id = ? AND dashboard_id = ?", "tenant-1", "dashboard-1").Count(&menuCount).Error)
	require.Zero(t, menuCount)

	repeated, err := service.Request(context.Background(), "tenant-1", "dashboard-1")
	require.NoError(t, err)
	require.Equal(t, response.OperationID, repeated.OperationID)
	require.Equal(t, model.DashboardDeleteDelivered, repeated.Status)
	require.Equal(t, int32(1), calls.Load(), "a delivered idempotency key must not call ThingsVis again")
	status, err := service.Get("tenant-1", "dashboard-1")
	require.NoError(t, err)
	require.Equal(t, repeated, status)
}

func TestDashboardDeleteCleanupRetainsPendingAndRecentJobs(t *testing.T) {
	db := openDashboardDeleteTestDB(t)
	now, err := dal.GetDatabaseTime()
	require.NoError(t, err)
	oldDeliveredAt := now.Add(-31 * 24 * time.Hour)
	recentDeliveredAt := now.Add(-29 * 24 * time.Hour)
	require.NoError(t, db.Create([]model.DashboardDeleteJob{
		{ID: "old", TenantID: "tenant-1", DashboardID: "old", Status: model.DashboardDeleteDelivered, Attempts: 1, NextRetryAt: now, CreatedAt: oldDeliveredAt, UpdatedAt: oldDeliveredAt, DeliveredAt: &oldDeliveredAt},
		{ID: "recent", TenantID: "tenant-1", DashboardID: "recent", Status: model.DashboardDeleteDelivered, Attempts: 1, NextRetryAt: now, CreatedAt: recentDeliveredAt, UpdatedAt: recentDeliveredAt, DeliveredAt: &recentDeliveredAt},
		{ID: "pending", TenantID: "tenant-1", DashboardID: "pending", Status: model.DashboardDeletePending, NextRetryAt: now, CreatedAt: oldDeliveredAt, UpdatedAt: oldDeliveredAt},
	}).Error)

	require.NoError(t, (&DashboardDelete{}).CleanupDelivered(30*24*time.Hour))
	var ids []string
	require.NoError(t, db.Model(&model.DashboardDeleteJob{}).Order("id").Pluck("id", &ids).Error)
	require.Equal(t, []string{"pending", "recent"}, ids)
}
