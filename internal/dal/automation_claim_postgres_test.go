package dal

import (
	"sync"
	"testing"
	"time"

	"project/internal/model"
	"project/internal/query"

	"github.com/stretchr/testify/require"
)

func TestAutomationTaskPostgresConcurrentClaimLeaseAndFencing(t *testing.T) {
	db := openDeviceBatchPostgresSchema(t, &model.OneTimeTask{}, &model.PeriodicTask{})
	query.SetDefault(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Create(&model.OneTimeTask{
		ID: "once-claim", SceneAutomationID: "scene-1", ExecutionTime: now.Add(-time.Minute),
		ExecutingState: "NEX", Enabled: "Y",
	}).Error)
	require.NoError(t, db.Create(&model.PeriodicTask{
		ID: "periodic-claim", SceneAutomationID: "scene-1", TaskType: "CRON", Param: "*/30 * * * * *",
		ExecutionTime: now.Add(-time.Minute), Enabled: "Y",
	}).Error)

	var wait sync.WaitGroup
	type onceResult struct {
		tasks []*model.OneTimeTask
		err   error
	}
	type periodicResult struct {
		tasks []*model.PeriodicTask
		err   error
	}
	onceClaims := make(chan onceResult, 2)
	periodicClaims := make(chan periodicResult, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claimed, err := ClaimDueOneTimeTasks(1, now, time.Minute)
			onceClaims <- onceResult{tasks: claimed, err: err}
		}()
		wait.Add(1)
		go func() {
			defer wait.Done()
			claimed, err := ClaimDuePeriodicTasks(1, now, time.Minute)
			periodicClaims <- periodicResult{tasks: claimed, err: err}
		}()
	}
	wait.Wait()
	close(onceClaims)
	close(periodicClaims)
	var claimedOnce []*model.OneTimeTask
	for result := range onceClaims {
		require.NoError(t, result.err)
		claimedOnce = append(claimedOnce, result.tasks...)
	}
	var claimedPeriodic []*model.PeriodicTask
	for result := range periodicClaims {
		require.NoError(t, result.err)
		claimedPeriodic = append(claimedPeriodic, result.tasks...)
	}
	require.Len(t, claimedOnce, 1, "only one worker may claim a one-time task")
	require.Len(t, claimedPeriodic, 1, "only one worker may claim a periodic task")
	require.NotNil(t, claimedOnce[0].ClaimToken)
	require.NotNil(t, claimedPeriodic[0].ClaimToken)

	beforeExpiry, err := ClaimDueOneTimeTasks(1, now.Add(30*time.Second), time.Minute)
	require.NoError(t, err)
	require.Empty(t, beforeExpiry)
	reclaimedOnce, err := ClaimDueOneTimeTasks(1, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimedOnce, 1)
	require.NotEqual(t, *claimedOnce[0].ClaimToken, *reclaimedOnce[0].ClaimToken)
	require.ErrorContains(t, CompleteOneTimeTask(claimedOnce[0]), "claim is no longer owned")
	require.NoError(t, CompleteOneTimeTask(reclaimedOnce[0]))

	reclaimedPeriodic, err := ClaimDuePeriodicTasks(1, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimedPeriodic, 1)
	require.NotEqual(t, *claimedPeriodic[0].ClaimToken, *reclaimedPeriodic[0].ClaimToken)
	require.ErrorContains(t, AdvancePeriodicTask(claimedPeriodic[0]), "claim is no longer owned")
	require.NoError(t, AdvancePeriodicTask(reclaimedPeriodic[0]))
}
