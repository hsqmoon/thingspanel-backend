package dal

import (
	"context"
	"testing"
	"time"

	"project/internal/model"
	"project/internal/query"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdvancePeriodicTaskUsesCompareAndSwap(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:periodic-task-advance?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PeriodicTask{}))
	query.SetDefault(db)

	executionTime := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	claimToken := "00000000-0000-4000-8000-000000000001"
	task := &model.PeriodicTask{
		ID: "periodic-1", SceneAutomationID: "scene-1", TaskType: "CRON", Param: "*/30 * * * * *",
		ExecutionTime: executionTime, Enabled: "Y", ClaimToken: &claimToken,
	}
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, AdvancePeriodicTask(task))

	var updated model.PeriodicTask
	require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
	require.True(t, updated.ExecutionTime.After(executionTime))
	require.ErrorContains(t, AdvancePeriodicTask(task), "claim is no longer owned")
}

func TestAdvancePeriodicTaskRejectsMalformedHourWithoutPanic(t *testing.T) {
	claimToken := "00000000-0000-4000-8000-000000000001"
	err := AdvancePeriodicTask(&model.PeriodicTask{ID: "periodic-1", TaskType: "HOUR", Param: "", ClaimToken: &claimToken})
	require.ErrorContains(t, err, "invalid hour parameter")
	err = AdvancePeriodicTask(&model.PeriodicTask{ID: "periodic-1", TaskType: "WEEK", Param: "|09:00:00+08:00", ClaimToken: &claimToken})
	require.ErrorContains(t, err, "result is zero")
}

func TestCompleteOneTimeTaskUsesCompareAndSwap(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:one-time-task-complete?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.OneTimeTask{}))
	query.SetDefault(db)
	claimToken := "00000000-0000-4000-8000-000000000001"

	task := &model.OneTimeTask{
		ID: "one-time-1", SceneAutomationID: "scene-1", ExecutionTime: time.Now().UTC(),
		ExecutingState: "NEX", Enabled: "Y", ClaimToken: &claimToken,
	}
	require.NoError(t, db.Create(task).Error)
	require.NoError(t, CompleteOneTimeTask(task))

	var updated model.OneTimeTask
	require.NoError(t, db.First(&updated, "id = ?", task.ID).Error)
	require.Equal(t, "EXE", updated.ExecutingState)
	require.ErrorContains(t, CompleteOneTimeTask(task), "claim is no longer owned")

	expiredTask := &model.OneTimeTask{
		ID: "one-time-expired", SceneAutomationID: "scene-1", ExecutionTime: time.Now().UTC().Add(-time.Hour),
		ExecutingState: "NEX", Enabled: "Y", ClaimToken: &claimToken,
	}
	require.NoError(t, db.Create(expiredTask).Error)
	require.NoError(t, ExpireOneTimeTask(expiredTask))
	updated = model.OneTimeTask{}
	require.NoError(t, db.First(&updated, "id = ?", expiredTask.ID).Error)
	require.Equal(t, "EXP", updated.ExecutingState)
}

func TestSceneAutomationQueriesReturnDatabaseErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:automation-query-errors?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	query.SetDefault(db)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = CheckSceneAutomationHasClose("scene-1")
	require.Error(t, err)
	_, err = GetSceneAutomationTenantID(context.Background(), "scene-1")
	require.Error(t, err)
}
