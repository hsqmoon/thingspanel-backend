package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"project/internal/model"
	"project/pkg/global"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MarketBundleInstallRepo handles database operations for market bundle installations
type MarketBundleInstallRepo struct{}

func NewMarketBundleInstallRepo() *MarketBundleInstallRepo {
	return &MarketBundleInstallRepo{}
}

// CreateInstallation creates a new installation record
func (r *MarketBundleInstallRepo) CreateInstallation(ctx context.Context, inst *model.MarketBundleInstallation) (*model.MarketBundleInstallation, error) {
	inst.ID = uuid.NewString()
	if err := global.DB.WithContext(ctx).Create(inst).Error; err != nil {
		return nil, fmt.Errorf("failed to create installation: %w", err)
	}
	return inst, nil
}

// CreateInstallationWithAudit persists the initial state and its audit record atomically.
func (r *MarketBundleInstallRepo) CreateInstallationWithAudit(
	ctx context.Context,
	inst *model.MarketBundleInstallation,
	audit *model.MarketInstallationAudit,
) (*model.MarketBundleInstallation, error) {
	inst.ID = uuid.NewString()
	audit.ID = uuid.NewString()
	audit.InstallationID = inst.ID
	if err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(inst).Error; err != nil {
			return fmt.Errorf("failed to create installation: %w", err)
		}
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("failed to create audit entry: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return inst, nil
}

// GetByIdempotencyKey retrieves an installation by idempotency key and tenant
func (r *MarketBundleInstallRepo) GetByIdempotencyKey(ctx context.Context, idempotencyKey, tenantID string) (*model.MarketBundleInstallation, error) {
	var inst model.MarketBundleInstallation
	err := global.DB.WithContext(ctx).
		Where("idempotency_key = ? AND tenant_id = ?", idempotencyKey, tenantID).
		First(&inst).Error
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// GetByID retrieves an installation by ID
func (r *MarketBundleInstallRepo) GetByID(ctx context.Context, id string) (*model.MarketBundleInstallation, error) {
	var inst model.MarketBundleInstallation
	err := global.DB.WithContext(ctx).Where("id = ?", id).First(&inst).Error
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// GetByTenantAndBundle retrieves an installation by tenant, bundle key, and version
func (r *MarketBundleInstallRepo) GetByTenantAndBundle(ctx context.Context, tenantID, bundleKey, version string) (*model.MarketBundleInstallation, error) {
	var inst model.MarketBundleInstallation
	err := global.DB.WithContext(ctx).
		Where("tenant_id = ? AND bundle_key = ? AND bundle_version = ?", tenantID, bundleKey, version).
		Order("created_at DESC").
		First(&inst).Error
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// ListByTenant retrieves installations for a tenant with pagination
func (r *MarketBundleInstallRepo) ListByTenant(ctx context.Context, tenantID string, q *model.ListInstallationsRequest) ([]*model.MarketBundleInstallation, int64, error) {
	db := global.DB.WithContext(ctx).Model(&model.MarketBundleInstallation{}).
		Where("tenant_id = ?", tenantID)

	if q.BundleKey != "" {
		db = db.Where("bundle_key = ?", q.BundleKey)
	}
	if q.Status != "" {
		db = db.Where("status = ?", q.Status)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > 100 {
		q.PageSize = 20
	}

	var installations []*model.MarketBundleInstallation
	offset := (q.Page - 1) * q.PageSize
	if err := db.Order("created_at DESC").Limit(q.PageSize).Offset(offset).Find(&installations).Error; err != nil {
		return nil, 0, err
	}
	return installations, total, nil
}

// UpdateStatus updates installation status and timestamps
func (r *MarketBundleInstallRepo) UpdateStatus(ctx context.Context, id, status, errorCode, errorMessage string) error {
	return global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return updateInstallationStatus(tx, id, status, errorCode, errorMessage)
	})
}

// UpdateStatusWithAudits applies a state transition and all required audit entries atomically.
func (r *MarketBundleInstallRepo) UpdateStatusWithAudits(
	ctx context.Context,
	id, status, errorCode, errorMessage string,
	audits ...*model.MarketInstallationAudit,
) error {
	return global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := updateInstallationStatus(tx, id, status, errorCode, errorMessage); err != nil {
			return err
		}
		for _, audit := range audits {
			audit.ID = uuid.NewString()
			audit.InstallationID = id
			if err := tx.Create(audit).Error; err != nil {
				return fmt.Errorf("failed to create audit entry: %w", err)
			}
		}
		return nil
	})
}

func (r *MarketBundleInstallRepo) FinalizeWithNotification(
	ctx context.Context,
	id, status string,
	outbox *model.MarketInstallNotificationOutbox,
	audits ...*model.MarketInstallationAudit,
) error {
	now := time.Now().UTC()
	outbox.ID = uuid.NewString()
	outbox.InstallationID = id
	outbox.Status = model.MarketInstallNotifyPending
	outbox.NextRetryAt = now
	outbox.CreatedAt = now
	outbox.UpdatedAt = now
	return global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := updateInstallationStatus(tx, id, status, "", ""); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "installation_id"}}, DoNothing: true}).Create(outbox).Error; err != nil {
			return fmt.Errorf("create market install notification outbox: %w", err)
		}
		for _, audit := range audits {
			audit.ID = uuid.NewString()
			audit.InstallationID = id
			if err := tx.Create(audit).Error; err != nil {
				return fmt.Errorf("failed to create audit entry: %w", err)
			}
		}
		return nil
	})
}

func (r *MarketBundleInstallRepo) ClaimDueNotifications(
	ctx context.Context,
	limit int,
	lease time.Duration,
) ([]*model.MarketInstallNotificationOutbox, error) {
	if limit < 1 {
		limit = 20
	}
	now := time.Now().UTC()
	claimed := make([]*model.MarketInstallNotificationOutbox, 0, limit)
	err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []*model.MarketInstallNotificationOutbox
		if err := tx.Where(
			"(status = ? AND next_retry_at <= ?) OR (status = ? AND lease_expires_at <= ?)",
			model.MarketInstallNotifyPending,
			now,
			model.MarketInstallNotifyProcessing,
			now,
		).Order("next_retry_at, created_at, id").Limit(limit).Find(&candidates).Error; err != nil {
			return err
		}
		for _, candidate := range candidates {
			claimToken := uuid.NewString()
			attempt := candidate.Attempts + 1
			leaseUntil := now.Add(lease)
			result := tx.Model(&model.MarketInstallNotificationOutbox{}).
				Where("id = ? AND attempts = ? AND ((status = ? AND next_retry_at <= ?) OR (status = ? AND lease_expires_at <= ?))",
					candidate.ID,
					candidate.Attempts,
					model.MarketInstallNotifyPending,
					now,
					model.MarketInstallNotifyProcessing,
					now,
				).
				Updates(map[string]interface{}{
					"status":           model.MarketInstallNotifyProcessing,
					"claim_token":      claimToken,
					"attempts":         attempt,
					"lease_expires_at": leaseUntil,
					"updated_at":       now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				candidate.Status = model.MarketInstallNotifyProcessing
				candidate.ClaimToken = &claimToken
				candidate.Attempts = attempt
				candidate.LeaseExpiresAt = &leaseUntil
				claimed = append(claimed, candidate)
			}
		}
		return nil
	})
	return claimed, err
}

func (r *MarketBundleInstallRepo) MarkNotificationDelivered(
	ctx context.Context,
	id, claimToken string,
	attempt int,
) error {
	now := time.Now().UTC()
	result := global.DB.WithContext(ctx).Model(&model.MarketInstallNotificationOutbox{}).
		Where("id = ? AND status = ? AND claim_token = ? AND attempts = ?", id, model.MarketInstallNotifyProcessing, claimToken, attempt).
		Updates(map[string]interface{}{
			"status":           model.MarketInstallNotifyDelivered,
			"claim_token":      nil,
			"lease_expires_at": nil,
			"last_error":       "",
			"delivered_at":     now,
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("market install notification claim lost")
	}
	return nil
}

func (r *MarketBundleInstallRepo) MarkNotificationRetry(
	ctx context.Context,
	id, claimToken string,
	attempt int,
	nextRetry time.Time,
	lastError string,
) error {
	result := global.DB.WithContext(ctx).Model(&model.MarketInstallNotificationOutbox{}).
		Where("id = ? AND status = ? AND claim_token = ? AND attempts = ?", id, model.MarketInstallNotifyProcessing, claimToken, attempt).
		Updates(map[string]interface{}{
			"status":           model.MarketInstallNotifyPending,
			"claim_token":      nil,
			"lease_expires_at": nil,
			"next_retry_at":    nextRetry,
			"last_error":       lastError,
			"updated_at":       time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("market install notification claim lost")
	}
	return nil
}

func (r *MarketBundleInstallRepo) MarkNotificationCredentialRequired(
	ctx context.Context,
	id, claimToken string,
	attempt int,
	lastError string,
) error {
	result := global.DB.WithContext(ctx).Model(&model.MarketInstallNotificationOutbox{}).
		Where("id = ? AND status = ? AND claim_token = ? AND attempts = ?", id, model.MarketInstallNotifyProcessing, claimToken, attempt).
		Updates(map[string]interface{}{
			"status":           model.MarketInstallNotifyCredential,
			"claim_token":      nil,
			"lease_expires_at": nil,
			"last_error":       lastError,
			"updated_at":       time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("market install notification claim lost")
	}
	return nil
}

func (r *MarketBundleInstallRepo) RefreshNotificationCredential(
	ctx context.Context,
	installationID, tenantID, marketToken string,
) error {
	if marketToken == "" {
		return errors.New("market token is required")
	}
	return global.DB.WithContext(ctx).Model(&model.MarketInstallNotificationOutbox{}).
		Where("installation_id = ? AND tenant_id = ? AND status IN ?", installationID, tenantID, []string{
			model.MarketInstallNotifyPending,
			model.MarketInstallNotifyCredential,
		}).
		Updates(map[string]interface{}{
			"market_token":  marketToken,
			"status":        model.MarketInstallNotifyPending,
			"next_retry_at": time.Now().UTC(),
			"last_error":    "",
			"updated_at":    time.Now().UTC(),
		}).Error
}

func updateInstallationStatus(db *gorm.DB, id, status, errorCode, errorMessage string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"status":     status,
		"updated_at": now,
	}

	switch status {
	case model.InstallStateDownloading:
		updates["error_code"] = ""
		updates["error_message"] = ""
		updates["downloaded_at"] = nil
		updates["verified_at"] = nil
		updates["models_installed_at"] = nil
		updates["dashboards_created_at"] = nil
		updates["completed_at"] = nil
	case model.InstallStateDownloaded:
		updates["error_code"] = ""
		updates["error_message"] = ""
		updates["downloaded_at"] = now
	case model.InstallStateVerified:
		updates["error_code"] = ""
		updates["error_message"] = ""
		updates["verified_at"] = now
	case model.InstallStateModelsInstalled:
		updates["error_code"] = ""
		updates["error_message"] = ""
		updates["models_installed_at"] = now
	case model.InstallStateDashboardsCreated:
		updates["error_code"] = ""
		updates["error_message"] = ""
		updates["dashboards_created_at"] = now
	case model.InstallStateWaitingForBindings, model.InstallStateCompleted:
		updates["error_code"] = ""
		updates["error_message"] = ""
		updates["completed_at"] = now
	case model.InstallStateFailed, model.InstallStateCompensationRequired:
		updates["error_code"] = errorCode
		updates["error_message"] = errorMessage
		updates["completed_at"] = now
	}

	result := db.
		Model(&model.MarketBundleInstallation{}).
		Where("id = ?", id).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("installation %s not found", id)
	}
	return nil
}

// UpdateWarnings updates installation warnings
func (r *MarketBundleInstallRepo) UpdateWarnings(ctx context.Context, id string, warnings []string) error {
	warningsJSON, err := json.Marshal(warnings)
	if err != nil {
		return err
	}
	result := global.DB.WithContext(ctx).
		Model(&model.MarketBundleInstallation{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"warnings":   warningsJSON,
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("installation %s not found", id)
	}
	return nil
}

// --- Resource Mappings ---

// CreateResourceMapping creates a new resource mapping
func (r *MarketBundleInstallRepo) CreateResourceMapping(ctx context.Context, mapping *model.MarketResourceMapping) (*model.MarketResourceMapping, error) {
	mapping.ID = uuid.NewString()
	if err := global.DB.WithContext(ctx).Create(mapping).Error; err != nil {
		return nil, fmt.Errorf("failed to create resource mapping: %w", err)
	}
	return mapping, nil
}

// CreateResourceMappingWithAudit records a created resource and its audit atomically.
func (r *MarketBundleInstallRepo) CreateResourceMappingWithAudit(
	ctx context.Context,
	mapping *model.MarketResourceMapping,
	audit *model.MarketInstallationAudit,
) (*model.MarketResourceMapping, error) {
	mapping.ID = uuid.NewString()
	audit.ID = uuid.NewString()
	audit.InstallationID = mapping.InstallationID
	if err := global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(mapping).Error; err != nil {
			return fmt.Errorf("failed to create resource mapping: %w", err)
		}
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("failed to create audit entry: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return mapping, nil
}

// CreateDashboardRecords atomically persists a dashboard mapping, its audit, and all binding states.
func (r *MarketBundleInstallRepo) CreateDashboardRecords(
	ctx context.Context,
	mapping *model.MarketResourceMapping,
	audit *model.MarketInstallationAudit,
	bindings []*model.MarketBundleBindingStatus,
) error {
	mapping.ID = uuid.NewString()
	audit.ID = uuid.NewString()
	audit.InstallationID = mapping.InstallationID
	return global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(mapping).Error; err != nil {
			return fmt.Errorf("failed to create resource mapping: %w", err)
		}
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("failed to create audit entry: %w", err)
		}
		for _, binding := range bindings {
			binding.ID = uuid.NewString()
			if err := tx.Create(binding).Error; err != nil {
				return fmt.Errorf("failed to create binding status: %w", err)
			}
		}
		return nil
	})
}

// GetResourceMappingsByInstallation retrieves all mappings for an installation
func (r *MarketBundleInstallRepo) GetResourceMappingsByInstallation(ctx context.Context, installationID string) ([]*model.MarketResourceMapping, error) {
	var mappings []*model.MarketResourceMapping
	err := global.DB.WithContext(ctx).
		Where("installation_id = ?", installationID).
		Order("resource_type, created_at").
		Find(&mappings).Error
	if err != nil {
		return nil, err
	}
	return mappings, nil
}

// GetResourceMappingByLocalID retrieves mapping by local ID and type
func (r *MarketBundleInstallRepo) GetResourceMappingByLocalID(ctx context.Context, tenantID, localID, resourceType string) (*model.MarketResourceMapping, error) {
	var mapping model.MarketResourceMapping
	err := global.DB.WithContext(ctx).
		Where("tenant_id = ? AND local_id = ? AND resource_type = ? AND status = 'active'", tenantID, localID, resourceType).
		First(&mapping).Error
	if err != nil {
		return nil, err
	}
	return &mapping, nil
}

// UpdateResourceMappingStatus updates a resource mapping status
func (r *MarketBundleInstallRepo) UpdateResourceMappingStatus(ctx context.Context, id, status string) error {
	return global.DB.WithContext(ctx).
		Model(&model.MarketResourceMapping{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"status":     status,
			"updated_at": time.Now(),
		}).Error
}

func (r *MarketBundleInstallRepo) UpdateResourceMappingStatusWithAudit(
	ctx context.Context,
	id, status string,
	audit *model.MarketInstallationAudit,
) error {
	audit.ID = uuid.NewString()
	return global.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.MarketResourceMapping{}).
			Where("id = ?", id).
			Updates(map[string]interface{}{
				"status":     status,
				"updated_at": time.Now(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("resource mapping %s not found", id)
		}
		if err := tx.Create(audit).Error; err != nil {
			return fmt.Errorf("failed to create audit entry: %w", err)
		}
		return nil
	})
}

// --- Binding Status ---

// CreateBindingStatus creates a new binding status record
func (r *MarketBundleInstallRepo) CreateBindingStatus(ctx context.Context, binding *model.MarketBundleBindingStatus) (*model.MarketBundleBindingStatus, error) {
	binding.ID = uuid.NewString()
	if err := global.DB.WithContext(ctx).Create(binding).Error; err != nil {
		return nil, fmt.Errorf("failed to create binding status: %w", err)
	}
	return binding, nil
}

// GetBindingStatusesByInstallation retrieves all binding statuses for an installation
func (r *MarketBundleInstallRepo) GetBindingStatusesByInstallation(ctx context.Context, installationID string) ([]*model.MarketBundleBindingStatus, error) {
	var bindings []*model.MarketBundleBindingStatus
	err := global.DB.WithContext(ctx).
		Where("installation_id = ?", installationID).
		Order("binding_key").
		Find(&bindings).Error
	if err != nil {
		return nil, err
	}
	return bindings, nil
}

// UpdateBindingDevice updates the bound device for a binding
func (r *MarketBundleInstallRepo) UpdateBindingDevice(ctx context.Context, id, localDeviceID, status, errorMessage string) error {
	now := time.Now()
	return global.DB.WithContext(ctx).
		Model(&model.MarketBundleBindingStatus{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"local_device_id": localDeviceID,
			"bound_at":        now,
			"status":          status,
			"error_message":   errorMessage,
			"updated_at":      now,
		}).Error
}

// GetBindingByKey retrieves binding by installation ID and binding key
func (r *MarketBundleInstallRepo) GetBindingByKey(ctx context.Context, installationID, bindingKey string) (*model.MarketBundleBindingStatus, error) {
	var binding model.MarketBundleBindingStatus
	err := global.DB.WithContext(ctx).
		Where("installation_id = ? AND binding_key = ?", installationID, bindingKey).
		First(&binding).Error
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// --- Audit Trail ---

// CreateAuditEntry creates a new audit entry
func (r *MarketBundleInstallRepo) CreateAuditEntry(ctx context.Context, audit *model.MarketInstallationAudit) (*model.MarketInstallationAudit, error) {
	audit.ID = uuid.NewString()
	if err := global.DB.WithContext(ctx).Create(audit).Error; err != nil {
		return nil, fmt.Errorf("failed to create audit entry: %w", err)
	}
	return audit, nil
}

// GetAuditTrail retrieves audit entries for an installation
func (r *MarketBundleInstallRepo) GetAuditTrail(ctx context.Context, installationID string) ([]*model.MarketInstallationAudit, error) {
	var entries []*model.MarketInstallationAudit
	err := global.DB.WithContext(ctx).
		Where("installation_id = ?", installationID).
		Order("created_at DESC").
		Find(&entries).Error
	if err != nil {
		return nil, err
	}
	return entries, nil
}
