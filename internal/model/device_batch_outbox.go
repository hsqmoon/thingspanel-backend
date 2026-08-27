package model

import "time"

const TableNameDeviceBatchOutbox = "device_batch_outbox"

const (
	DeviceBatchDeliveryPending    = "pending"
	DeviceBatchDeliveryProcessing = "processing"
	DeviceBatchDeliveryDelivered  = "delivered"
)

// DeviceBatchOutbox records an at-least-once plugin notification created in
// the same transaction as its devices. EventID is also sent to the plugin so a
// receiver can deduplicate a delivery repeated after an uncertain HTTP result.
type DeviceBatchOutbox struct {
	EventID            string     `gorm:"column:event_id;primaryKey" json:"event_id"`
	IdempotencyKey     string     `gorm:"column:idempotency_key;uniqueIndex" json:"idempotency_key"`
	TenantID           string     `gorm:"column:tenant_id;not null" json:"tenant_id"`
	ServiceAccessID    string     `gorm:"column:service_access_id;not null" json:"service_access_id"`
	ServiceAccessRefID *string    `gorm:"column:service_access_ref_id" json:"-"`
	Destination        string     `gorm:"column:destination;not null" json:"-"`
	Payload            string     `gorm:"column:payload;type:jsonb;not null" json:"-"`
	Status             string     `gorm:"column:status;not null" json:"status"`
	ClaimToken         *string    `gorm:"column:claim_token" json:"-"`
	Attempts           int        `gorm:"column:attempts;not null" json:"attempts"`
	NextRetryAt        time.Time  `gorm:"column:next_retry_at;not null" json:"next_retry_at"`
	LastError          *string    `gorm:"column:last_error" json:"last_error,omitempty"`
	CreatedAt          time.Time  `gorm:"column:created_at;not null" json:"created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;not null" json:"updated_at"`
	DeliveredAt        *time.Time `gorm:"column:delivered_at" json:"delivered_at,omitempty"`
}

func (*DeviceBatchOutbox) TableName() string {
	return TableNameDeviceBatchOutbox
}
