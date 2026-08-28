package service

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"project/internal/dal"
	model "project/internal/model"

	"github.com/google/uuid"
	"github.com/spf13/viper"
)

const (
	automationTaskLease      = 5 * time.Minute
	automationTaskRetryDelay = 30 * time.Second
)

var automationExecutionNamespace = uuid.MustParse("c9f8f749-9bd0-4cf5-bfe8-e35d55afe17d")

type AutomateTask struct {
	onceMu     sync.Mutex
	periodicMu sync.Mutex
}

func automationExecutionKey(kind, taskID string, executionTime time.Time) string {
	return uuid.NewSHA1(automationExecutionNamespace, []byte(kind+":"+taskID+":"+executionTime.UTC().Format(time.RFC3339Nano))).String()
}

func (t *AutomateTask) OnceTaskExecute() error {
	if !t.onceMu.TryLock() {
		return nil
	}
	defer t.onceMu.Unlock()
	limit := viper.GetInt("automation_task_confg.once_task_limit")
	if limit <= 0 {
		return errors.New("once task limit must be positive")
	}
	var taskErrors []error
	for range limit {
		result, err := dal.ClaimDueOneTimeTasks(1, time.Now().UTC(), automationTaskLease)
		if err != nil {
			return errors.Join(append(taskErrors, err)...)
		}
		if len(result) == 0 {
			break
		}
		if err = runOneTimeTasks(result, time.Now().UTC(), t.TaskAutomationExecute, dal.CompleteOneTimeTask, dal.ExpireOneTimeTask, dal.FailOneTimeTask); err != nil {
			taskErrors = append(taskErrors, err)
		}
	}
	return errors.Join(taskErrors...)
}

func runOneTimeTasks(
	result []*model.OneTimeTask,
	now time.Time,
	execute func(string, string) error,
	complete func(*model.OneTimeTask) error,
	expire func(*model.OneTimeTask) error,
	fail func(*model.OneTimeTask, time.Time, error) error,
) error {
	var taskErrors []error
	for _, task := range result {
		if task.ExpirationTime > 0 && task.ExecutionTime.Add(time.Duration(task.ExpirationTime)*time.Minute).Before(now) {
			if err := expire(task); err != nil {
				taskErrors = append(taskErrors, err)
			}
			continue
		}
		executionKey := automationExecutionKey("once", task.ID, task.ExecutionTime)
		if err := execute(task.SceneAutomationID, executionKey); err != nil {
			persistErr := fail(task, now.Add(automationTaskRetryDelay), err)
			taskErrors = append(taskErrors, errors.Join(fmt.Errorf("execute one-time task %s: %w", task.ID, err), persistErr))
			continue
		}
		if err := complete(task); err != nil {
			taskErrors = append(taskErrors, err)
		}
	}
	return errors.Join(taskErrors...)
}

func (*AutomateTask) TaskAutomationExecute(sceneAutomationID, executionKey string) error {
	closed, err := GroupApp.CheckSceneAutomationHasClose(sceneAutomationID)
	if err != nil {
		return fmt.Errorf("query scene automation %s status: %w", sceneAutomationID, err)
	}
	if closed {
		return nil
	}
	actions, err := dal.GetActionInfoListBySceneAutomationId([]string{sceneAutomationID})
	if err != nil {
		return fmt.Errorf("query actions for scene automation %s: %w", sceneAutomationID, err)
	}
	var deviceIDs, deviceConfigIDs []string
	for _, action := range actions {
		if action.ActionType == model.AUTOMATE_ACTION_TYPE_MULTIPLE && action.ActionTarget != nil {
			deviceConfigIDs = append(deviceConfigIDs, *action.ActionTarget)
		}
	}
	if len(deviceConfigIDs) > 0 {
		deviceIDs, err = dal.GetDeviceIdsByDeviceConfigId(deviceConfigIDs)
		if err != nil {
			return fmt.Errorf("query devices for scene automation %s: %w", sceneAutomationID, err)
		}
	}
	if err := GroupApp.SceneAutomateExecuteWithKey(sceneAutomationID, deviceIDs, actions, executionKey); err != nil {
		return fmt.Errorf("execute scene automation %s: %w", sceneAutomationID, err)
	}
	return nil
}

func (t *AutomateTask) PeriodicTaskExecute() error {
	if !t.periodicMu.TryLock() {
		return nil
	}
	defer t.periodicMu.Unlock()
	limit := viper.GetInt("automation_task_confg.periodic_task_limit")
	if limit <= 0 {
		return errors.New("periodic task limit must be positive")
	}
	var taskErrors []error
	for range limit {
		result, err := dal.ClaimDuePeriodicTasks(1, time.Now().UTC(), automationTaskLease)
		if err != nil {
			return errors.Join(append(taskErrors, err)...)
		}
		if len(result) == 0 {
			break
		}
		if err = runPeriodicTasks(result, time.Now().UTC(), t.TaskAutomationExecute, dal.AdvancePeriodicTask, dal.FailPeriodicTask); err != nil {
			taskErrors = append(taskErrors, err)
		}
	}
	return errors.Join(taskErrors...)
}

func runPeriodicTasks(
	result []*model.PeriodicTask,
	now time.Time,
	execute func(string, string) error,
	advance func(*model.PeriodicTask) error,
	fail func(*model.PeriodicTask, time.Time, error) error,
) error {
	var taskErrors []error
	for _, task := range result {
		if task.ExecutionTime.IsZero() || (task.ExpirationTime > 0 && task.ExecutionTime.Add(time.Duration(task.ExpirationTime)*time.Minute).Before(now)) {
			if err := advance(task); err != nil {
				taskErrors = append(taskErrors, err)
			}
			continue
		}
		executionKey := automationExecutionKey("periodic", task.ID, task.ExecutionTime)
		if err := execute(task.SceneAutomationID, executionKey); err != nil {
			persistErr := fail(task, now.Add(automationTaskRetryDelay), err)
			taskErrors = append(taskErrors, errors.Join(fmt.Errorf("execute periodic task %s: %w", task.ID, err), persistErr))
			continue
		}
		if err := advance(task); err != nil {
			taskErrors = append(taskErrors, err)
		}
	}
	return errors.Join(taskErrors...)
}
