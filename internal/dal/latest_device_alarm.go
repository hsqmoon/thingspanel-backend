package dal

import (
	"context"

	"project/internal/query"
)

// LatestDeviceAlarmQuery 设备告警查询结构体
type LatestDeviceAlarmQuery struct{}

// CountDevicesByTenantAndStatus 根据租户ID和状态统计设备数量
func (q *LatestDeviceAlarmQuery) CountDevicesByTenantAndStatus(ctx context.Context, tenantID string) (int64, error) {
	lda := query.LatestDeviceAlarm

	// 查询指定状态的设备数量，alarm_status不等于N
	queryBuilder := lda.WithContext(ctx).Where(lda.AlarmStatus.Neq("N"))
	if tenantID != "" {
		queryBuilder = queryBuilder.Where(lda.TenantID.Eq(tenantID))
	}
	count, err := queryBuilder.
		Distinct(lda.DeviceID).
		Count()

	return count, err
}
