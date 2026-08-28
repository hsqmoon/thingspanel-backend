package service

import (
	"errors"
	"testing"
	"time"

	"project/internal/model"

	"github.com/stretchr/testify/require"
)

func TestRunPeriodicTasksAdvancesOnlySuccessfulExecutions(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	executeErr := errors.New("action failed")
	advanceErr := errors.New("database unavailable")
	tasks := []*model.PeriodicTask{
		{ID: "execution-fails", SceneAutomationID: "scene-fails", ExecutionTime: now.Add(-time.Minute)},
		{ID: "advance-fails", SceneAutomationID: "scene-advance-fails", ExecutionTime: now.Add(-time.Minute)},
		{ID: "succeeds", SceneAutomationID: "scene-succeeds", ExecutionTime: now.Add(-time.Minute)},
	}
	var advanced []string

	var failed []string
	err := runPeriodicTasks(tasks, now, func(sceneAutomationID, executionKey string) error {
		require.NotEmpty(t, executionKey)
		if sceneAutomationID == "scene-fails" {
			return executeErr
		}
		return nil
	}, func(task *model.PeriodicTask) error {
		advanced = append(advanced, task.ID)
		if task.ID == "advance-fails" {
			return advanceErr
		}
		return nil
	}, func(task *model.PeriodicTask, _ time.Time, _ error) error {
		failed = append(failed, task.ID)
		return nil
	})

	require.ErrorIs(t, err, executeErr)
	require.ErrorIs(t, err, advanceErr)
	require.Equal(t, []string{"advance-fails", "succeeds"}, advanced)
	require.Equal(t, []string{"execution-fails"}, failed)
}

func TestRunOneTimeTasksReturnsExecutionAndExpirationErrors(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	executeErr := errors.New("action failed")
	expireErr := errors.New("expiration update failed")
	tasks := []*model.OneTimeTask{
		{ID: "execute", SceneAutomationID: "scene", ExecutionTime: now.Add(-time.Minute)},
		{ID: "expired", SceneAutomationID: "expired-scene", ExecutionTime: now.Add(-time.Hour), ExpirationTime: 1},
	}

	var completed []string
	err := runOneTimeTasks(tasks, now, func(sceneAutomationID, executionKey string) error {
		require.Equal(t, "scene", sceneAutomationID)
		require.NotEmpty(t, executionKey)
		return executeErr
	}, func(task *model.OneTimeTask) error {
		completed = append(completed, task.ID)
		return nil
	}, func(task *model.OneTimeTask) error {
		require.Equal(t, "expired", task.ID)
		return expireErr
	}, func(*model.OneTimeTask, time.Time, error) error {
		return nil
	})

	require.ErrorIs(t, err, executeErr)
	require.ErrorIs(t, err, expireErr)
	require.Empty(t, completed)
}

func TestRunOneTimeTasksCompletesOnlySuccessfulExecution(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	completeErr := errors.New("completion update failed")
	tasks := []*model.OneTimeTask{
		{ID: "complete-fails", SceneAutomationID: "scene", ExecutionTime: now.Add(-time.Minute)},
	}

	err := runOneTimeTasks(tasks, now, func(string, string) error {
		return nil
	}, func(task *model.OneTimeTask) error {
		require.Equal(t, "complete-fails", task.ID)
		return completeErr
	}, func(*model.OneTimeTask) error {
		t.Fatal("expiration update must not run")
		return nil
	}, func(*model.OneTimeTask, time.Time, error) error {
		return nil
	})

	require.ErrorIs(t, err, completeErr)
}

func TestAutomationExecutionKeyIsStableAcrossLeaseReclaim(t *testing.T) {
	executionTime := time.Date(2026, 8, 28, 9, 0, 0, 123, time.UTC)
	first := automationExecutionKey("periodic", "task-1", executionTime)
	second := automationExecutionKey("periodic", "task-1", executionTime)
	require.Equal(t, first, second)
	require.Equal(t, automationActionMessageID(first, "action-1", "device-1"), automationActionMessageID(second, "action-1", "device-1"))
	require.NotEqual(t, automationActionMessageID(first, "action-1", "device-1"), automationActionMessageID(first, "action-1", "device-2"))
}

func TestOneTimeTaskCrashReplayReusesExecutionKey(t *testing.T) {
	now := time.Date(2026, 8, 28, 9, 0, 0, 0, time.UTC)
	firstToken := "00000000-0000-4000-8000-000000000001"
	secondToken := "00000000-0000-4000-8000-000000000002"
	first := &model.OneTimeTask{ID: "task-1", SceneAutomationID: "scene-1", ExecutionTime: now.Add(-time.Minute), ClaimToken: &firstToken}
	second := &model.OneTimeTask{ID: first.ID, SceneAutomationID: first.SceneAutomationID, ExecutionTime: first.ExecutionTime, ClaimToken: &secondToken}
	var executionKeys []string
	execute := func(_ string, executionKey string) error {
		executionKeys = append(executionKeys, executionKey)
		return nil
	}
	completionLost := errors.New("database unavailable after external action")
	require.ErrorIs(t, runOneTimeTasks([]*model.OneTimeTask{first}, now, execute, func(*model.OneTimeTask) error {
		return completionLost
	}, func(*model.OneTimeTask) error { return nil }, func(*model.OneTimeTask, time.Time, error) error { return nil }), completionLost)
	require.NoError(t, runOneTimeTasks([]*model.OneTimeTask{second}, now.Add(2*time.Minute), execute, func(*model.OneTimeTask) error {
		return nil
	}, func(*model.OneTimeTask) error { return nil }, func(*model.OneTimeTask, time.Time, error) error { return nil }))
	require.Len(t, executionKeys, 2)
	require.Equal(t, executionKeys[0], executionKeys[1], "lease reclaim must replay the same device idempotency key")
}
