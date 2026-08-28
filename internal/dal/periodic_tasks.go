package dal

import (
	"errors"
	"fmt"
	"time"

	model "project/internal/model"
	query "project/internal/query"
	"project/pkg/common"
)

func CreatePeriodicTask(d model.PeriodicTask, tx *query.QueryTx) error {
	if tx != nil {
		return tx.PeriodicTask.Create(&d)
	}
	return query.PeriodicTask.Create(&d)
}

func SwitchPeriodicTask(sceneAutomationId, enabled string, tx *query.QueryTx) error {
	_, err := tx.PeriodicTask.
		Where(tx.PeriodicTask.SceneAutomationID.Eq(sceneAutomationId)).
		Update(tx.PeriodicTask.Enabled, enabled)
	return err
}

func GetPeriodicTask(sceneAutomationId string) ([]*model.PeriodicTask, error) {
	return query.PeriodicTask.Where(query.PeriodicTask.SceneAutomationID.Eq(sceneAutomationId)).Find()
}

func DeletePeriodicTask(sceneAutomationId string, tx *query.QueryTx) error {
	if tx != nil {
		_, err := tx.PeriodicTask.Where(tx.PeriodicTask.SceneAutomationID.Eq(sceneAutomationId)).Delete()
		return err
	}
	_, err := query.PeriodicTask.Where(query.PeriodicTask.SceneAutomationID.Eq(sceneAutomationId)).Delete()
	return err
}

func ClaimDuePeriodicTasks(limit int, now time.Time, lease time.Duration) ([]*model.PeriodicTask, error) {
	if limit <= 0 || lease <= 0 {
		return nil, errors.New("periodic task claim limit and lease must be positive")
	}
	db := query.PeriodicTask.UnderlyingDB()
	if db == nil {
		return nil, errors.New("periodic task database is not initialized")
	}
	var tasks []*model.PeriodicTask
	err := db.Raw(`WITH candidates AS (
		SELECT id FROM periodic_tasks
		WHERE execution_time<=? AND enabled='Y'
		  AND (claim_token IS NULL OR lease_expires_at IS NULL OR lease_expires_at<=?)
		ORDER BY execution_time,id
		FOR UPDATE SKIP LOCKED
		LIMIT ?
	)
	UPDATE periodic_tasks task
	SET claim_token=gen_random_uuid(),lease_expires_at=?,claim_attempts=claim_attempts+1,last_error=NULL
	FROM candidates WHERE task.id=candidates.id
	RETURNING task.*`, now, now, limit, now.Add(lease)).Scan(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("claim periodic tasks: %w", err)
	}
	return tasks, nil
}

func AdvancePeriodicTask(task *model.PeriodicTask) error {
	if task == nil || task.ClaimToken == nil || *task.ClaimToken == "" {
		return errors.New("advance periodic task: task has no claim token")
	}
	if task.TaskType == "HOUR" && len(task.Param) < 2 {
		return fmt.Errorf("calculate next execution time for periodic task %s: invalid hour parameter", task.ID)
	}
	nextExecuteTime, err := common.GetSceneExecuteTime(task.TaskType, task.Param)
	if err != nil {
		return fmt.Errorf("calculate next execution time for periodic task %s: %w", task.ID, err)
	}
	if nextExecuteTime.IsZero() {
		return fmt.Errorf("calculate next execution time for periodic task %s: result is zero", task.ID)
	}
	db := query.PeriodicTask.UnderlyingDB()
	if db == nil {
		return errors.New("advance periodic task: database is not initialized")
	}
	result := db.Exec(`UPDATE periodic_tasks SET execution_time=?,claim_token=NULL,lease_expires_at=NULL,last_error=NULL
		WHERE id=? AND claim_token=? AND execution_time=?`, nextExecuteTime.UTC(), task.ID, *task.ClaimToken, task.ExecutionTime)
	if result.Error != nil {
		return fmt.Errorf("advance periodic task %s: %w", task.ID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("advance periodic task %s: claim is no longer owned by this worker", task.ID)
	}
	return nil
}

func FailPeriodicTask(task *model.PeriodicTask, retryAt time.Time, taskErr error) error {
	if task == nil || task.ClaimToken == nil || *task.ClaimToken == "" {
		return errors.New("fail periodic task: task has no claim token")
	}
	db := query.PeriodicTask.UnderlyingDB()
	if db == nil {
		return errors.New("fail periodic task: database is not initialized")
	}
	result := db.Exec(`UPDATE periodic_tasks SET lease_expires_at=?,last_error=? WHERE id=? AND claim_token=?`, retryAt, taskErr.Error(), task.ID, *task.ClaimToken)
	if result.Error != nil {
		return fmt.Errorf("fail periodic task %s: %w", task.ID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("fail periodic task %s: claim is no longer owned by this worker", task.ID)
	}
	return nil
}
