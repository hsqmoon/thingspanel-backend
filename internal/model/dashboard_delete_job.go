package model

import "time"

const TableNameDashboardDeleteJob = "dashboard_delete_jobs"

const (
	DashboardDeletePending    = "pending"
	DashboardDeleteProcessing = "processing"
	DashboardDeleteDelivered  = "delivered"
)

type DashboardDeleteJob struct {
	ID             string     `gorm:"column:id;primaryKey" json:"operation_id"`
	TenantID       string     `gorm:"column:tenant_id;not null;uniqueIndex:dashboard_delete_jobs_tenant_dashboard_unique" json:"tenant_id"`
	DashboardID    string     `gorm:"column:dashboard_id;not null;uniqueIndex:dashboard_delete_jobs_tenant_dashboard_unique" json:"dashboard_id"`
	Status         string     `gorm:"column:status;not null" json:"status"`
	ClaimToken     *string    `gorm:"column:claim_token" json:"-"`
	Attempts       int        `gorm:"column:attempts;not null" json:"attempts"`
	NextRetryAt    time.Time  `gorm:"column:next_retry_at;not null" json:"next_retry_at"`
	LeaseExpiresAt *time.Time `gorm:"column:lease_expires_at" json:"-"`
	LastError      *string    `gorm:"column:last_error" json:"-"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null" json:"updated_at"`
	DeliveredAt    *time.Time `gorm:"column:delivered_at" json:"delivered_at,omitempty"`
}

func (*DashboardDeleteJob) TableName() string {
	return TableNameDashboardDeleteJob
}

type DashboardDeleteRsp struct {
	OperationID string `json:"operation_id"`
	DashboardID string `json:"dashboard_id"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
}

func (j *DashboardDeleteJob) ToRsp() *DashboardDeleteRsp {
	status := DashboardDeletePending
	if j.Status == DashboardDeleteDelivered {
		status = DashboardDeleteDelivered
	}
	return &DashboardDeleteRsp{
		OperationID: j.ID,
		DashboardID: j.DashboardID,
		Status:      status,
		Attempts:    j.Attempts,
	}
}
