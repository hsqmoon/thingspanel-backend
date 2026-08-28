package dal

import (
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"project/internal/model"
	"project/pkg/global"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestDashboardDeleteJobPostgresMigration27(t *testing.T) {
	databaseURL := requireDeviceBatchPostgresURL(t)
	if os.Getenv("NSNR_REQUIRE_BACKEND_MIGRATION27") != "1" {
		t.Skip("migration 27 validation is only required by the formal build gate")
	}
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	var tableName sql.NullString
	require.NoError(t, db.Raw(`SELECT to_regclass('public.dashboard_delete_jobs')::text`).Scan(&tableName).Error)
	require.True(t, tableName.Valid)
	require.Equal(t, "dashboard_delete_jobs", tableName.String)
	var constraints int64
	require.NoError(t, db.Raw(`
		SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'public.dashboard_delete_jobs'::regclass
		  AND conname IN (
			'dashboard_delete_jobs_pkey',
			'dashboard_delete_jobs_tenant_dashboard_unique',
			'dashboard_delete_jobs_status_check',
			'dashboard_delete_jobs_attempts_check',
			'dashboard_delete_jobs_claim_check',
			'dashboard_delete_jobs_delivered_check'
		  )`).Scan(&constraints).Error)
	require.EqualValues(t, 6, constraints)
	var dueIndex int64
	require.NoError(t, db.Raw(`
		SELECT count(*) FROM pg_indexes
		WHERE schemaname = 'public'
		  AND tablename = 'dashboard_delete_jobs'
		  AND indexname = 'dashboard_delete_jobs_due_idx'`).Scan(&dueIndex).Error)
	require.EqualValues(t, 1, dueIndex)
}

func TestDashboardDeleteJobPostgresConcurrentClaimAndFencing(t *testing.T) {
	db := openDeviceBatchPostgresSchema(t, &model.TenantDashboardMenu{}, &model.DashboardDeleteJob{})
	previousDB := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previousDB })
	now := time.Now().UTC()
	require.NoError(t, db.Create([]model.TenantDashboardMenu{
		{ID: "menu-a", TenantID: "tenant-1", DashboardID: "dashboard-a", DashboardName: "A", MenuName: "A", ParentCode: "home", Sort: 1, Enabled: true, CreatedAt: now, UpdatedAt: now},
		{ID: "menu-b", TenantID: "tenant-1", DashboardID: "dashboard-b", DashboardName: "B", MenuName: "B", ParentCode: "home", Sort: 2, Enabled: true, CreatedAt: now, UpdatedAt: now},
	}).Error)
	jobA, err := EnqueueDashboardDelete("tenant-1", "dashboard-a")
	require.NoError(t, err)
	jobB, err := EnqueueDashboardDelete("tenant-1", "dashboard-b")
	require.NoError(t, err)

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	var locked model.DashboardDeleteJob
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobA.ID).Take(&locked).Error)
	claimedB, err := ClaimDashboardDelete("")
	require.NoError(t, err)
	require.NotNil(t, claimedB)
	require.Equal(t, jobB.ID, claimedB.ID, "SKIP LOCKED must bypass a dashboard delete job owned by another worker")
	require.NoError(t, lockTx.Rollback().Error)

	claimedA, err := ClaimDashboardDelete(jobA.ID)
	require.NoError(t, err)
	require.NotNil(t, claimedA)
	require.NotNil(t, claimedA.ClaimToken)
	staleToken := *claimedA.ClaimToken
	require.NoError(t, db.Model(&model.DashboardDeleteJob{}).Where("id = ?", jobA.ID).
		Update("lease_expires_at", gorm.Expr("CURRENT_TIMESTAMP - INTERVAL '1 second'")).Error)
	reclaimedA, err := ClaimDashboardDelete(jobA.ID)
	require.NoError(t, err)
	require.NotNil(t, reclaimedA)
	require.NotNil(t, reclaimedA.ClaimToken)
	require.NotEqual(t, staleToken, *reclaimedA.ClaimToken)
	require.Equal(t, claimedA.Attempts+1, reclaimedA.Attempts)

	databaseNow, err := GetDatabaseTime()
	require.NoError(t, err)
	err = FinalizeDashboardDelete(jobA.ID, staleToken, claimedA.Attempts, databaseNow)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrDashboardDeleteClaimLost))
	var menuCount int64
	require.NoError(t, db.Model(&model.TenantDashboardMenu{}).Where("dashboard_id = ?", "dashboard-a").Count(&menuCount).Error)
	require.EqualValues(t, 1, menuCount, "a stale worker must not delete the menu")

	require.NoError(t, FinalizeDashboardDelete(jobA.ID, *reclaimedA.ClaimToken, reclaimedA.Attempts, databaseNow))
	require.NoError(t, db.Model(&model.TenantDashboardMenu{}).Where("dashboard_id = ?", "dashboard-a").Count(&menuCount).Error)
	require.Zero(t, menuCount)
	jobA, err = GetDashboardDelete(jobA.ID)
	require.NoError(t, err)
	require.Equal(t, model.DashboardDeleteDelivered, jobA.Status)
	require.Nil(t, jobA.ClaimToken)
}
