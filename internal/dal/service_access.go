package dal

import (
	"context"
	"errors"

	"project/internal/model"
	"project/internal/query"
	"project/pkg/global"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrServiceAccessHasDevices    = errors.New("service access still has devices")
	ErrPendingDeviceBatchDelivery = errors.New("service access has pending device batch delivery")
	ErrIdempotencyConflict        = errors.New("idempotency key was already used for a different request")
)

// DeleteServiceAccess serializes device creation and access deletion by
// locking the tenant-owned access row before checking every dependent row.
func DeleteServiceAccess(id, tenantID string) error {
	if id == "" || tenantID == "" {
		return gorm.ErrRecordNotFound
	}
	return global.DB.Transaction(func(tx *gorm.DB) error {
		var serviceAccess model.ServiceAccess
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ?", id, tenantID).
			Take(&serviceAccess).Error; err != nil {
			return err
		}

		var devices int64
		if err := tx.Model(&model.Device{}).
			Where("service_access_id = ? AND tenant_id = ?", id, tenantID).
			Count(&devices).Error; err != nil {
			return err
		}
		if devices > 0 {
			return ErrServiceAccessHasDevices
		}

		var pending int64
		if err := tx.Model(&model.DeviceBatchOutbox{}).
			Where("service_access_id = ? AND tenant_id = ? AND status <> ?", id, tenantID, model.DeviceBatchDeliveryDelivered).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return ErrPendingDeviceBatchDelivery
		}
		if err := tx.Model(&model.DeviceBatchOutbox{}).
			Where("service_access_ref_id = ? AND tenant_id = ? AND status = ?", id, tenantID, model.DeviceBatchDeliveryDelivered).
			Update("service_access_ref_id", nil).Error; err != nil {
			return err
		}
		result := tx.Where("id = ? AND tenant_id = ?", id, tenantID).Delete(&model.ServiceAccess{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// UpdateServiceAccessWithOutbox commits the tenant-owned access-point update
// and its durable plugin notification as one database transaction.
func UpdateServiceAccessWithOutbox(id, tenantID string, updates map[string]interface{}, outbox *model.DeviceBatchOutbox) error {
	if id == "" || tenantID == "" || outbox == nil {
		return gorm.ErrRecordNotFound
	}

	return global.DB.Transaction(func(tx *gorm.DB) error {
		var serviceAccess model.ServiceAccess
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ?", id, tenantID).
			Take(&serviceAccess).Error; err != nil {
			return err
		}

		var existing model.DeviceBatchOutbox
		err := tx.Where("idempotency_key = ?", outbox.IdempotencyKey).Take(&existing).Error
		if err == nil {
			if existing.TenantID != tenantID || existing.ServiceAccessID != id || existing.Payload != outbox.Payload {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		result := tx.Model(&model.ServiceAccess{}).
			Where("id = ? AND tenant_id = ?", id, tenantID).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}

		now, err := databaseTime(tx)
		if err != nil {
			return err
		}
		outbox.TenantID = tenantID
		outbox.ServiceAccessID = id
		outbox.ServiceAccessRefID = &id
		outbox.Status = model.DeviceBatchDeliveryPending
		outbox.NextRetryAt = now
		outbox.CreatedAt = now
		outbox.UpdatedAt = now
		return tx.Create(outbox).Error
	})
}

func GetServiceAccessListByPage(req *model.GetServiceAccessByPageReq, tenantID string) (int64, interface{}, error) {
	var count int64
	var serviceAccess = []model.ServiceAccess{}

	q := query.ServiceAccess
	queryBuilder := q.WithContext(context.Background())
	queryBuilder = queryBuilder.Where(q.ServicePluginID.Eq(req.ServicePluginID))
	if tenantID != "" {
		queryBuilder = queryBuilder.Where(q.TenantID.Eq(tenantID))
	}

	count, err := queryBuilder.Count()
	if err != nil {
		logrus.Error(err)
		return count, serviceAccess, err
	}
	if req.Page != 0 && req.PageSize != 0 {
		queryBuilder = queryBuilder.Limit(req.PageSize)
		queryBuilder = queryBuilder.Offset((req.Page - 1) * req.PageSize)
	}

	err = queryBuilder.Select().Order(q.CreateAt.Desc()).Scan(&serviceAccess)
	if err != nil {
		logrus.Error(err)
		return count, serviceAccess, err
	}
	return count, serviceAccess, err
}

// 通过service_plugin_id获取服务接入点列表
func GetServiceAccessListByServicePluginID(servicePluginID string) ([]model.ServiceAccess, error) {
	q := query.ServiceAccess
	queryBuilder := q.WithContext(context.Background())
	var serviceAccess = []model.ServiceAccess{}
	err := queryBuilder.Where(q.ServicePluginID.Eq(servicePluginID)).Select().Scan(&serviceAccess)
	if err != nil {
		logrus.Error(err)
		return serviceAccess, err
	}
	return serviceAccess, nil
}

// 通过id获取服务接入点信息
func GetServiceAccessByID(id string) (*model.ServiceAccess, error) {
	// 使用first查询
	q := query.ServiceAccess
	queryBuilder := q.WithContext(context.Background())
	serviceAccess, err := queryBuilder.Where(q.ID.Eq(id)).First()
	if err != nil {
		logrus.Error(err)
		return nil, err
	}
	return serviceAccess, nil
}

// GetServiceAccessByIDAndTenant prevents a tenant from binding devices to an
// access point owned by another tenant.
func GetServiceAccessByIDAndTenant(id, tenantID string) (*model.ServiceAccess, error) {
	q := query.ServiceAccess
	return q.WithContext(context.Background()).Where(q.ID.Eq(id), q.TenantID.Eq(tenantID)).First()
}
