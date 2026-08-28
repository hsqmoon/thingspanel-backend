package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"project/internal/dal"
	"project/internal/model"
	"project/pkg/errcode"
)

type DashboardDelete struct {
	clientOnce sync.Once
	thingsVis  *ThingsVisClient
}

type dashboardDeleteAttempt int

const (
	dashboardDeleteNotClaimed dashboardDeleteAttempt = iota
	dashboardDeleteDelivered
	dashboardDeleteDeferred
)

func (s *DashboardDelete) client() *ThingsVisClient {
	s.clientOnce.Do(func() {
		if s.thingsVis == nil {
			s.thingsVis = NewThingsVisClient()
		}
	})
	return s.thingsVis
}

func (s *DashboardDelete) Request(ctx context.Context, tenantID, dashboardID string) (*model.DashboardDeleteRsp, error) {
	tenantID = strings.TrimSpace(tenantID)
	dashboardID = strings.TrimSpace(dashboardID)
	if err := validateDashboardMenuAccess(tenantID, dashboardID); err != nil {
		return nil, err
	}
	job, err := dal.EnqueueDashboardDelete(tenantID, dashboardID)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeDBError, map[string]interface{}{
			"operation": "enqueue_dashboard_delete",
			"error":     err.Error(),
		})
	}
	attempt, attemptErr := s.try(ctx, job.ID)
	if attemptErr != nil && attempt != dashboardDeleteDeferred {
		return nil, errcode.WithData(errcode.CodeDBError, map[string]interface{}{
			"operation": "attempt_dashboard_delete",
			"error":     attemptErr.Error(),
		})
	}
	job, err = dal.GetDashboardDelete(job.ID)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeDBError, map[string]interface{}{
			"operation": "get_dashboard_delete",
			"error":     err.Error(),
		})
	}
	return job.ToRsp(), nil
}

func (*DashboardDelete) Get(tenantID, dashboardID string) (*model.DashboardDeleteRsp, error) {
	tenantID = strings.TrimSpace(tenantID)
	dashboardID = strings.TrimSpace(dashboardID)
	if err := validateDashboardMenuAccess(tenantID, dashboardID); err != nil {
		return nil, err
	}
	job, err := dal.GetDashboardDeleteByTarget(tenantID, dashboardID)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeDBError, map[string]interface{}{
			"operation": "get_dashboard_delete",
			"error":     err.Error(),
		})
	}
	if job == nil {
		return nil, nil
	}
	return job.ToRsp(), nil
}

func (s *DashboardDelete) DeliverPending(limit int) error {
	if limit <= 0 {
		limit = 20
	}
	var failures []error
	for processed := 0; processed < limit; processed++ {
		attempt, err := s.try(context.Background(), "")
		if attempt == dashboardDeleteNotClaimed && err == nil {
			break
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (*DashboardDelete) CleanupDelivered(retention time.Duration) error {
	return dal.CleanupDeliveredDashboardDeletes(retention)
}

func (s *DashboardDelete) try(ctx context.Context, jobID string) (dashboardDeleteAttempt, error) {
	job, err := dal.ClaimDashboardDelete(jobID)
	if err != nil {
		return dashboardDeleteNotClaimed, err
	}
	if job == nil {
		return dashboardDeleteNotClaimed, nil
	}
	if job.ClaimToken == nil {
		return dashboardDeleteNotClaimed, fmt.Errorf("claimed dashboard delete job %s has no claim token", job.ID)
	}

	deleteErr := s.client().DeleteDashboard(ctx, job.TenantID, job.DashboardID)
	now, timeErr := dal.GetDatabaseTime()
	if timeErr != nil {
		return dashboardDeleteNotClaimed, timeErr
	}
	if deleteErr != nil {
		delay := 5 * time.Second
		for attempt := 1; attempt < job.Attempts && delay < 5*time.Minute; attempt++ {
			delay *= 2
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		if err := dal.MarkDashboardDeleteFailed(job.ID, *job.ClaimToken, job.Attempts, deleteErr.Error(), now.Add(delay), now); err != nil {
			if errors.Is(err, dal.ErrDashboardDeleteClaimLost) {
				return dashboardDeleteDeferred, nil
			}
			return dashboardDeleteNotClaimed, errors.Join(deleteErr, err)
		}
		return dashboardDeleteDeferred, fmt.Errorf("delete ThingsVis dashboard %s: %w", job.DashboardID, deleteErr)
	}
	if err := dal.FinalizeDashboardDelete(job.ID, *job.ClaimToken, job.Attempts, now); err != nil {
		if errors.Is(err, dal.ErrDashboardDeleteClaimLost) {
			return dashboardDeleteDeferred, nil
		}
		return dashboardDeleteNotClaimed, err
	}
	return dashboardDeleteDelivered, nil
}
