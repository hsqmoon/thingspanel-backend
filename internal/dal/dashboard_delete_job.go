package dal

import (
	"errors"
	"fmt"
	"time"

	"project/internal/model"
	"project/pkg/global"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const dashboardDeleteLease = time.Minute

var ErrDashboardDeleteClaimLost = errors.New("dashboard delete claim lost")

func EnqueueDashboardDelete(tenantID, dashboardID string) (*model.DashboardDeleteJob, error) {
	if global.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var persisted model.DashboardDeleteJob
	err := global.DB.Transaction(func(tx *gorm.DB) error {
		now, err := databaseTime(tx)
		if err != nil {
			return err
		}
		job := model.DashboardDeleteJob{
			ID:          uuid.NewString(),
			TenantID:    tenantID,
			DashboardID: dashboardID,
			Status:      model.DashboardDeletePending,
			NextRetryAt: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "dashboard_id"}},
			DoNothing: true,
		}).Create(&job).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.DashboardDeleteJob{}).
			Where("tenant_id = ? AND dashboard_id = ? AND status = ?", tenantID, dashboardID, model.DashboardDeletePending).
			Updates(map[string]interface{}{"next_retry_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Where("tenant_id = ? AND dashboard_id = ?", tenantID, dashboardID).Take(&persisted).Error
	})
	if err != nil {
		return nil, err
	}
	return &persisted, nil
}

func ClaimDashboardDelete(jobID string) (*model.DashboardDeleteJob, error) {
	if global.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var claimed model.DashboardDeleteJob
	found := false
	err := global.DB.Transaction(func(tx *gorm.DB) error {
		now, err := databaseTime(tx)
		if err != nil {
			return err
		}
		var claimedID string
		query := tx.Model(&model.DashboardDeleteJob{}).
			Select("id").
			Where("(status = ? AND next_retry_at <= ?) OR (status = ? AND lease_expires_at <= ?)",
				model.DashboardDeletePending, now, model.DashboardDeleteProcessing, now).
			Order("CASE WHEN status = 'pending' THEN next_retry_at ELSE lease_expires_at END ASC, created_at ASC, id ASC").
			Limit(1).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if jobID != "" {
			query = query.Where("id = ?", jobID)
		}
		if err := query.Scan(&claimedID).Error; err != nil || claimedID == "" {
			return err
		}

		claimToken := uuid.NewString()
		leaseExpiresAt := now.Add(dashboardDeleteLease)
		result := tx.Model(&model.DashboardDeleteJob{}).
			Where("id = ?", claimedID).
			Updates(map[string]interface{}{
				"status":           model.DashboardDeleteProcessing,
				"claim_token":      claimToken,
				"attempts":         gorm.Expr("attempts + 1"),
				"lease_expires_at": leaseExpiresAt,
				"updated_at":       now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("dashboard delete job %s was not claimed", claimedID)
		}
		found = true
		return tx.Where("id = ?", claimedID).Take(&claimed).Error
	})
	if err != nil || !found {
		return nil, err
	}
	return &claimed, nil
}

func MarkDashboardDeleteFailed(jobID, claimToken string, attempt int, failure string, nextRetryAt, now time.Time) error {
	if global.DB == nil {
		return fmt.Errorf("database is not initialized")
	}
	result := global.DB.Model(&model.DashboardDeleteJob{}).
		Where("id = ? AND status = ? AND claim_token = ? AND attempts = ?", jobID, model.DashboardDeleteProcessing, claimToken, attempt).
		Updates(map[string]interface{}{
			"status":           model.DashboardDeletePending,
			"claim_token":      nil,
			"lease_expires_at": nil,
			"last_error":       failure,
			"next_retry_at":    nextRetryAt,
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: job %s", ErrDashboardDeleteClaimLost, jobID)
	}
	return nil
}

func FinalizeDashboardDelete(jobID, claimToken string, attempt int, now time.Time) error {
	if global.DB == nil {
		return fmt.Errorf("database is not initialized")
	}
	return global.DB.Transaction(func(tx *gorm.DB) error {
		var job model.DashboardDeleteJob
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND claim_token = ? AND attempts = ?", jobID, model.DashboardDeleteProcessing, claimToken, attempt).
			Limit(1).Find(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: job %s", ErrDashboardDeleteClaimLost, jobID)
		}
		if err := tx.Where("tenant_id = ? AND dashboard_id = ?", job.TenantID, job.DashboardID).
			Delete(&model.TenantDashboardMenu{}).Error; err != nil {
			return err
		}
		result = tx.Model(&model.DashboardDeleteJob{}).
			Where("id = ? AND status = ? AND claim_token = ? AND attempts = ?", jobID, model.DashboardDeleteProcessing, claimToken, attempt).
			Updates(map[string]interface{}{
				"status":           model.DashboardDeleteDelivered,
				"claim_token":      nil,
				"lease_expires_at": nil,
				"last_error":       nil,
				"delivered_at":     now,
				"updated_at":       now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: job %s", ErrDashboardDeleteClaimLost, jobID)
		}
		return nil
	})
}

func GetDashboardDelete(jobID string) (*model.DashboardDeleteJob, error) {
	if global.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var job model.DashboardDeleteJob
	if err := global.DB.Where("id = ?", jobID).Take(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

func GetDashboardDeleteByTarget(tenantID, dashboardID string) (*model.DashboardDeleteJob, error) {
	if global.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var job model.DashboardDeleteJob
	result := global.DB.Where("tenant_id = ? AND dashboard_id = ?", tenantID, dashboardID).Limit(1).Find(&job)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	return &job, nil
}

func CleanupDeliveredDashboardDeletes(retention time.Duration) error {
	if global.DB == nil {
		return fmt.Errorf("database is not initialized")
	}
	if retention <= 0 {
		return fmt.Errorf("dashboard delete retention must be positive")
	}
	now, err := GetDatabaseTime()
	if err != nil {
		return err
	}
	return global.DB.Where("status = ? AND delivered_at < ?", model.DashboardDeleteDelivered, now.Add(-retention)).
		Delete(&model.DashboardDeleteJob{}).Error
}
