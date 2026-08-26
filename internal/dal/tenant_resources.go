package dal

import (
	"fmt"
	"strings"

	"project/pkg/global"
)

type tenantResourceTable struct {
	name   string
	uuidID bool
}

var tenantResourceTables = []tenantResourceTable{
	{name: "alarm_config"},
	{name: "alarm_history"},
	{name: "alarm_info"},
	{name: "attribute_datas"},
	{name: "boards"},
	{name: "device_configs"},
	{name: "device_model_attributes"},
	{name: "device_model_commands"},
	{name: "device_model_custom_commands"},
	{name: "device_model_custom_control"},
	{name: "device_model_events"},
	{name: "device_model_telemetry"},
	{name: "device_templates"},
	{name: "device_trigger_condition"},
	{name: "device_user_logs"},
	{name: "devices"},
	{name: "event_datas"},
	{name: "expected_datas"},
	{name: "groups"},
	{name: "latest_device_alarms"},
	{name: "local_dashboard_template_instances", uuidID: true},
	{name: "local_dashboard_templates", uuidID: true},
	{name: "market_bundle_installations", uuidID: true},
	{name: "market_installation_audit", uuidID: true},
	{name: "market_resource_mappings", uuidID: true},
	{name: "notification_groups"},
	{name: "notification_histories"},
	{name: "open_api_keys"},
	{name: "operation_logs"},
	{name: "ota_upgrade_packages"},
	{name: "products"},
	{name: "roles"},
	{name: "scene_action_info"},
	{name: "scene_automations"},
	{name: "scene_info"},
	{name: "scene_log"},
	{name: "service_access"},
	{name: "tenant_dashboard_menus"},
	{name: "users"},
	{name: "vis_dashboard"},
	{name: "vis_plugin"},
}

func FindForeignTenantResource(ids []string, tenantID string) (resourceType string, resourceID string, found bool, err error) {
	uniqueIDs := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	if len(uniqueIDs) == 0 || tenantID == "" {
		return "", "", false, nil
	}

	values := make([]string, len(uniqueIDs))
	args := make([]interface{}, 0, len(uniqueIDs)+1)
	for i, id := range uniqueIDs {
		values[i] = "(?)"
		args = append(args, id)
	}
	queries := make([]string, 0, len(tenantResourceTables))
	for _, table := range tenantResourceTables {
		idExpression := "t.id"
		if table.uuidID {
			idExpression = "t.id::text"
		}
		queries = append(queries, fmt.Sprintf(
			`SELECT '%s'::text AS resource_type, %s::text AS resource_id FROM "%s" t JOIN requested r ON %s = r.id WHERE t.tenant_id IS DISTINCT FROM ?`,
			table.name,
			idExpression,
			table.name,
			idExpression,
		))
		args = append(args, tenantID)
	}

	queryText := "WITH requested(id) AS (VALUES " + strings.Join(values, ",") + ") " +
		"SELECT resource_type, resource_id FROM (" + strings.Join(queries, " UNION ALL ") + ") resources LIMIT 1"
	var result struct {
		ResourceType string `gorm:"column:resource_type"`
		ResourceID   string `gorm:"column:resource_id"`
	}
	if err = global.DB.Raw(queryText, args...).Scan(&result).Error; err != nil {
		return "", "", false, err
	}
	return result.ResourceType, result.ResourceID, result.ResourceID != "", nil
}
