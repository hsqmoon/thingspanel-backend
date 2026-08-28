package dal

import (
	"errors"
	"fmt"
	"time"

	model "project/internal/model"
	query "project/internal/query"
)

func CreateOneTimeTask(d model.OneTimeTask, tx *query.QueryTx) error {
	if tx != nil {
		return tx.OneTimeTask.Create(&d)
	}
	return query.OneTimeTask.Create(&d)
}

func SwitchOneTimeTask(sceneAutomationId, enabled string, tx *query.QueryTx) error {
	_, err := tx.OneTimeTask.
		Where(tx.OneTimeTask.SceneAutomationID.Eq(sceneAutomationId)).
		Update(tx.OneTimeTask.Enabled, enabled)
	return err
}

func GetOneTimeTask(sceneAutomationId string) ([]*model.OneTimeTask, error) {
	return query.OneTimeTask.Where(query.OneTimeTask.SceneAutomationID.Eq(sceneAutomationId)).Find()
}

func DeleteOneTimeTask(sceneAutomationId string, tx *query.QueryTx) error {
	if tx != nil {
		_, err := tx.OneTimeTask.Where(tx.OneTimeTask.SceneAutomationID.Eq(sceneAutomationId)).Delete()
		return err
	}
	_, err := query.OneTimeTask.Where(query.OneTimeTask.SceneAutomationID.Eq(sceneAutomationId)).Delete()
	return err
}

func ClaimDueOneTimeTasks(limit int, now time.Time, lease time.Duration) ([]*model.OneTimeTask, error) {
	if limit <= 0 || lease <= 0 {
		return nil, errors.New("one-time task claim limit and lease must be positive")
	}
	db := query.OneTimeTask.UnderlyingDB()
	if db == nil {
		return nil, errors.New("one-time task database is not initialized")
	}
	var tasks []*model.OneTimeTask
	err := db.Raw(`WITH candidates AS (
		SELECT id FROM one_time_tasks
		WHERE execution_time<=? AND enabled='Y' AND executing_state='NEX'
		  AND (claim_token IS NULL OR lease_expires_at IS NULL OR lease_expires_at<=?)
		ORDER BY execution_time,id
		FOR UPDATE SKIP LOCKED
		LIMIT ?
	)
	UPDATE one_time_tasks task
	SET claim_token=gen_random_uuid(),lease_expires_at=?,claim_attempts=claim_attempts+1,last_error=NULL
	FROM candidates WHERE task.id=candidates.id
	RETURNING task.*`, now, now, limit, now.Add(lease)).Scan(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("claim one-time tasks: %w", err)
	}
	return tasks, nil
}

func CompleteOneTimeTask(task *model.OneTimeTask) error {
	return updateClaimedOneTimeTask(task, "EXE")
}

func ExpireOneTimeTask(task *model.OneTimeTask) error {
	return updateClaimedOneTimeTask(task, "EXP")
}

func updateClaimedOneTimeTask(task *model.OneTimeTask, state string) error {
	if task == nil || task.ClaimToken == nil || *task.ClaimToken == "" {
		return errors.New("update one-time task: task has no claim token")
	}
	db := query.OneTimeTask.UnderlyingDB()
	if db == nil {
		return errors.New("update one-time task: database is not initialized")
	}
	result := db.Exec(`UPDATE one_time_tasks SET executing_state=?,claim_token=NULL,lease_expires_at=NULL,last_error=NULL
		WHERE id=? AND claim_token=? AND execution_time=?`, state, task.ID, *task.ClaimToken, task.ExecutionTime)
	if result.Error != nil {
		return fmt.Errorf("update one-time task %s: %w", task.ID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("update one-time task %s: claim is no longer owned by this worker", task.ID)
	}
	return nil
}

func FailOneTimeTask(task *model.OneTimeTask, retryAt time.Time, taskErr error) error {
	if task == nil || task.ClaimToken == nil || *task.ClaimToken == "" {
		return errors.New("fail one-time task: task has no claim token")
	}
	db := query.OneTimeTask.UnderlyingDB()
	if db == nil {
		return errors.New("fail one-time task: database is not initialized")
	}
	result := db.Exec(`UPDATE one_time_tasks SET lease_expires_at=?,last_error=? WHERE id=? AND claim_token=?`, retryAt, taskErr.Error(), task.ID, *task.ClaimToken)
	if result.Error != nil {
		return fmt.Errorf("fail one-time task %s: %w", task.ID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("fail one-time task %s: claim is no longer owned by this worker", task.ID)
	}
	return nil
}
