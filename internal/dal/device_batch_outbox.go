package dal

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"project/internal/model"
	"project/pkg/global"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const deviceBatchDeliveryLease = time.Minute

type DeviceBatchErrorKind string

const (
	DeviceBatchInvalidInput      DeviceBatchErrorKind = "invalid_input"
	DeviceBatchOwnershipConflict DeviceBatchErrorKind = "ownership_conflict"
	DeviceBatchAttributeConflict DeviceBatchErrorKind = "attribute_conflict"
)

var (
	ErrDeviceBatchInvalidInput      = errors.New("invalid device batch input")
	ErrDeviceBatchOwnershipConflict = errors.New("device number ownership conflict")
	ErrDeviceBatchAttributeConflict = errors.New("device number attribute conflict")
)

type DeviceBatchError struct {
	Kind         DeviceBatchErrorKind
	DeviceNumber string
	Field        string
	Reason       string
}

func (e *DeviceBatchError) Error() string {
	if e.DeviceNumber != "" {
		return fmt.Sprintf("device batch %s for %q: %s", e.Kind, e.DeviceNumber, e.Reason)
	}
	return fmt.Sprintf("device batch %s: %s", e.Kind, e.Reason)
}

func (e *DeviceBatchError) Unwrap() error {
	switch e.Kind {
	case DeviceBatchInvalidInput:
		return ErrDeviceBatchInvalidInput
	case DeviceBatchOwnershipConflict:
		return ErrDeviceBatchOwnershipConflict
	case DeviceBatchAttributeConflict:
		return ErrDeviceBatchAttributeConflict
	default:
		return nil
	}
}

// CreateDeviceBatch writes devices, their default-group relations and the
// notification outbox row atomically. Existing device numbers are accepted
// only when they belong to the same tenant and service access, making a retry
// return the original rows instead of creating duplicates.
func CreateDeviceBatch(devices []*model.Device, outbox *model.DeviceBatchOutbox) ([]*model.Device, *model.DeviceBatchOutbox, error) {
	return createDeviceBatch(global.DB, devices, outbox)
}

func createDeviceBatch(db *gorm.DB, devices []*model.Device, outbox *model.DeviceBatchOutbox) ([]*model.Device, *model.DeviceBatchOutbox, error) {
	if db == nil {
		return nil, nil, fmt.Errorf("database is not initialized")
	}
	if len(devices) == 0 {
		return nil, nil, &DeviceBatchError{Kind: DeviceBatchInvalidInput, Field: "devices", Reason: "at least one device is required"}
	}
	if outbox == nil {
		return nil, nil, &DeviceBatchError{Kind: DeviceBatchInvalidInput, Field: "outbox", Reason: "outbox is required"}
	}
	if outbox.TenantID == "" {
		return nil, nil, &DeviceBatchError{Kind: DeviceBatchInvalidInput, Field: "tenant_id", Reason: "tenant ID is required"}
	}
	if outbox.ServiceAccessID == "" {
		return nil, nil, &DeviceBatchError{Kind: DeviceBatchInvalidInput, Field: "service_access_id", Reason: "service access ID is required"}
	}

	numbers := make([]string, 0, len(devices))
	requestedByNumber := make(map[string]*model.Device, len(devices))
	for _, device := range devices {
		if device == nil {
			return nil, nil, &DeviceBatchError{Kind: DeviceBatchInvalidInput, Field: "device", Reason: "device is required"}
		}
		if device.DeviceNumber == "" {
			return nil, nil, &DeviceBatchError{Kind: DeviceBatchInvalidInput, Field: "device_number", Reason: "device number is required"}
		}
		if _, duplicate := requestedByNumber[device.DeviceNumber]; duplicate {
			return nil, nil, &DeviceBatchError{Kind: DeviceBatchInvalidInput, DeviceNumber: device.DeviceNumber, Field: "device_number", Reason: "device number is duplicated in the request"}
		}
		numbers = append(numbers, device.DeviceNumber)
		requestedByNumber[device.DeviceNumber] = device
	}

	var persisted []*model.Device
	var persistedOutbox model.DeviceBatchOutbox
	err := db.Transaction(func(tx *gorm.DB) error {
		insertDevices := append([]*model.Device(nil), devices...)
		sort.Slice(insertDevices, func(i, j int) bool {
			return insertDevices[i].DeviceNumber < insertDevices[j].DeviceNumber
		})
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "device_number"}},
			DoNothing: true,
		}).Create(insertDevices).Error; err != nil {
			return err
		}

		var rows []*model.Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("device_number IN ?", numbers).
			Order("device_number ASC").Find(&rows).Error; err != nil {
			return err
		}
		byNumber := make(map[string]*model.Device, len(rows))
		for _, row := range rows {
			byNumber[row.DeviceNumber] = row
		}
		persisted = make([]*model.Device, 0, len(numbers))
		for _, number := range numbers {
			row := byNumber[number]
			if row == nil {
				return fmt.Errorf("device %q was not persisted", number)
			}
			if row.TenantID != outbox.TenantID || row.ServiceAccessID == nil || *row.ServiceAccessID != outbox.ServiceAccessID {
				return &DeviceBatchError{Kind: DeviceBatchOwnershipConflict, DeviceNumber: number, Field: "device_number", Reason: "device number belongs to another tenant or service access"}
			}
			requested := requestedByNumber[number]
			if row.ID != requested.ID && (!reflect.DeepEqual(row.Name, requested.Name) ||
				!reflect.DeepEqual(row.DeviceConfigID, requested.DeviceConfigID) ||
				!reflect.DeepEqual(row.Description, requested.Description)) {
				return &DeviceBatchError{Kind: DeviceBatchAttributeConflict, DeviceNumber: number, Field: "device_number", Reason: "existing device has different batch attributes"}
			}
			persisted = append(persisted, row)
		}

		var rootGroups []model.Group
		if err := tx.Where("tenant_id = ? AND parent_id = ?", outbox.TenantID, "0").
			Order("created_at ASC").Find(&rootGroups).Error; err != nil {
			return err
		}
		if len(rootGroups) == 1 {
			relations := make([]model.RGroupDevice, 0, len(persisted))
			for _, device := range persisted {
				relations = append(relations, model.RGroupDevice{
					GroupID: rootGroups[0].ID, DeviceID: device.ID, TenantID: outbox.TenantID,
				})
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&relations).Error; err != nil {
				return err
			}
		}

		type deviceBatchMember struct {
			DeviceNumber string `json:"device_number"`
			DeviceID     string `json:"device_id"`
		}
		members := make([]deviceBatchMember, 0, len(persisted))
		for _, device := range persisted {
			members = append(members, deviceBatchMember{device.DeviceNumber, device.ID})
		}
		sort.Slice(members, func(i, j int) bool { return members[i].DeviceNumber < members[j].DeviceNumber })
		idempotencySource, err := json.Marshal(struct {
			TenantID        string              `json:"tenant_id"`
			ServiceAccessID string              `json:"service_access_id"`
			Devices         []deviceBatchMember `json:"devices"`
		}{outbox.TenantID, outbox.ServiceAccessID, members})
		if err != nil {
			return err
		}
		eventHash := sha256.Sum256(idempotencySource)
		outbox.EventID = fmt.Sprintf("%x", eventHash)
		outbox.IdempotencyKey = outbox.EventID
		deviceNumbers := make([]string, len(members))
		for i := range members {
			deviceNumbers[i] = members[i].DeviceNumber
		}
		payload, err := json.Marshal(struct {
			EventID         string   `json:"event_id"`
			IdempotencyKey  string   `json:"idempotency_key"`
			EventType       string   `json:"event_type"`
			TenantID        string   `json:"tenant_id"`
			ServiceAccessID string   `json:"service_access_id"`
			DeviceNumbers   []string `json:"device_numbers"`
		}{outbox.EventID, outbox.EventID, "service_access.devices.changed", outbox.TenantID, outbox.ServiceAccessID, deviceNumbers})
		if err != nil {
			return err
		}
		outbox.Payload = string(payload)
		databaseNow, err := databaseTime(tx)
		if err != nil {
			return err
		}
		outbox.NextRetryAt = databaseNow
		outbox.CreatedAt = databaseNow
		outbox.UpdatedAt = databaseNow
		serviceAccessRefID := outbox.ServiceAccessID
		outbox.ServiceAccessRefID = &serviceAccessRefID
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "idempotency_key"}},
			DoNothing: true,
		}).Create(outbox).Error; err != nil {
			return err
		}
		return tx.Where("idempotency_key = ?", outbox.IdempotencyKey).Take(&persistedOutbox).Error
	})
	if err != nil {
		return nil, nil, err
	}
	return persisted, &persistedOutbox, nil
}

// ClaimDeviceBatchOutbox leases one due row. PostgreSQL row locks make claims
// safe across multiple backend instances; an expired processing lease is
// reclaimed with a new claim token after a process crash.
func ClaimDeviceBatchOutbox(eventID string) (*model.DeviceBatchOutbox, error) {
	if global.DB == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	var claimed model.DeviceBatchOutbox
	found := false
	err := global.DB.Transaction(func(tx *gorm.DB) error {
		databaseNow, err := databaseTime(tx)
		if err != nil {
			return err
		}
		var claimedEventID string
		q := tx.Model(&model.DeviceBatchOutbox{}).
			Select("event_id").
			Where("status IN ? AND next_retry_at <= ?", []string{
				model.DeviceBatchDeliveryPending, model.DeviceBatchDeliveryProcessing,
			}, databaseNow).
			Order("next_retry_at ASC, created_at ASC").
			Limit(1).
			Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
		if eventID != "" {
			q = q.Where("event_id = ?", eventID)
		}
		if err := q.Scan(&claimedEventID).Error; err != nil || claimedEventID == "" {
			return err
		}

		leaseUntil := databaseNow.Add(deviceBatchDeliveryLease)
		claimToken := uuid.NewString()
		result := tx.Model(&model.DeviceBatchOutbox{}).
			Where("event_id = ?", claimedEventID).
			Updates(map[string]interface{}{
				"status":        model.DeviceBatchDeliveryProcessing,
				"claim_token":   claimToken,
				"attempts":      gorm.Expr("attempts + 1"),
				"next_retry_at": leaseUntil,
				"updated_at":    databaseNow,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("device batch event %s was not claimed", claimedEventID)
		}
		found = true
		return tx.Where("event_id = ?", claimedEventID).Take(&claimed).Error
	})
	if err != nil || !found {
		return nil, err
	}
	return &claimed, nil
}

func GetDatabaseTime() (time.Time, error) {
	if global.DB == nil {
		return time.Time{}, fmt.Errorf("database is not initialized")
	}
	return databaseTime(global.DB)
}

func databaseTime(db *gorm.DB) (time.Time, error) {
	if db.Dialector.Name() == "sqlite" {
		var unixSeconds int64
		if err := db.Raw("SELECT strftime('%s', 'now')").Scan(&unixSeconds).Error; err != nil {
			return time.Time{}, err
		}
		return time.Unix(unixSeconds, 0).UTC(), nil
	}
	var databaseNow time.Time
	if err := db.Raw("SELECT CURRENT_TIMESTAMP").Scan(&databaseNow).Error; err != nil {
		return time.Time{}, err
	}
	return databaseNow, nil
}

func MarkDeviceBatchOutboxDelivered(eventID, claimToken string, attempt int, destination string, now time.Time) error {
	result := global.DB.Model(&model.DeviceBatchOutbox{}).
		Where("event_id = ? AND status = ? AND claim_token = ? AND attempts = ?", eventID, model.DeviceBatchDeliveryProcessing, claimToken, attempt).
		Updates(map[string]interface{}{
			"status":       model.DeviceBatchDeliveryDelivered,
			"claim_token":  nil,
			"destination":  destination,
			"last_error":   nil,
			"delivered_at": now,
			"updated_at":   now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("device batch event %s is not processing", eventID)
	}
	return nil
}

func MarkDeviceBatchOutboxFailed(eventID, claimToken string, attempt int, destination, failure string, nextRetryAt, now time.Time) error {
	result := global.DB.Model(&model.DeviceBatchOutbox{}).
		Where("event_id = ? AND status = ? AND claim_token = ? AND attempts = ?", eventID, model.DeviceBatchDeliveryProcessing, claimToken, attempt).
		Updates(map[string]interface{}{
			"status":        model.DeviceBatchDeliveryPending,
			"claim_token":   nil,
			"destination":   destination,
			"last_error":    failure,
			"next_retry_at": nextRetryAt,
			"updated_at":    now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("device batch event %s is not processing", eventID)
	}
	return nil
}

func GetDeviceBatchOutbox(eventID string) (*model.DeviceBatchOutbox, error) {
	var outbox model.DeviceBatchOutbox
	if err := global.DB.Where("event_id = ?", eventID).Take(&outbox).Error; err != nil {
		return nil, err
	}
	return &outbox, nil
}

func CleanupDeliveredDeviceBatchOutbox(retention time.Duration) error {
	databaseNow, err := GetDatabaseTime()
	if err != nil {
		return err
	}
	return global.DB.Where("status = ? AND delivered_at < ?", model.DeviceBatchDeliveryDelivered, databaseNow.Add(-retention)).
		Delete(&model.DeviceBatchOutbox{}).Error
}
