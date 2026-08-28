package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"project/internal/dal"
	"project/internal/model"
	"project/internal/repo"
	"project/pkg/errcode"
	"project/pkg/global"
	"project/pkg/utils"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// MarketBundleInstallService handles the orchestration of market bundle installations
type MarketBundleInstallService struct {
	installRepo  *repo.MarketBundleInstallRepo
	marketClient *MarketClient
	thingsVis    *ThingsVisClient
	httpClient   *http.Client
}

// NewMarketBundleInstallService creates a new install service
func NewMarketBundleInstallService() *MarketBundleInstallService {
	return &MarketBundleInstallService{
		installRepo:  repo.NewMarketBundleInstallRepo(),
		marketClient: NewMarketClient(),
		thingsVis:    NewThingsVisClient(),
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// InstallBundle orchestrates the complete installation of a market bundle
func (s *MarketBundleInstallService) InstallBundle(ctx context.Context, req *model.InstallBundleRequest, claims *utils.UserClaims) (*model.InstallBundleResponse, error) {
	tenantID := claims.TenantID
	requestHash, err := hashInstallRequest(req, tenantID)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeParamError, "hash install request: "+err.Error())
	}

	// 1. Handle idempotency
	idempotencyKey := req.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = s.generateIdempotencyKey(req.BundleKey, req.Version, tenantID)
	}

	// Check for existing installation with same idempotency key
	existing, err := s.installRepo.GetByIdempotencyKey(ctx, idempotencyKey, tenantID)
	if err == nil && existing != nil {
		if existing.RequestHash != requestHash {
			return nil, errcode.WithData(errcode.CodeParamError, "idempotency key is already bound to a different request")
		}
		if err := s.installRepo.RefreshNotificationCredential(ctx, existing.ID, tenantID, req.MarketToken); err != nil {
			return nil, dbError("refresh install notification credential", err)
		}
		return s.buildIdempotentResponse(existing, tenantID)
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, dbError("query installation idempotency key", err)
	}

	// 2. Create installation record
	inst := &model.MarketBundleInstallation{
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		BundleKey:      req.BundleKey,
		BundleVersion:  req.Version,
		TenantID:       tenantID,
		Status:         model.InstallStateDownloading,
	}
	inst, err = s.installRepo.CreateInstallationWithAudit(
		ctx,
		inst,
		newInstallationAudit(tenantID, "install_started", "", model.InstallStateDownloading, nil),
	)
	if err != nil {
		existing, readErr := s.installRepo.GetByIdempotencyKey(ctx, idempotencyKey, tenantID)
		if readErr == nil && existing != nil {
			if existing.RequestHash != requestHash {
				return nil, errcode.WithData(errcode.CodeParamError, "idempotency key is already bound to a different request")
			}
			if err := s.installRepo.RefreshNotificationCredential(ctx, existing.ID, tenantID, req.MarketToken); err != nil {
				return nil, dbError("refresh install notification credential", err)
			}
			return s.buildIdempotentResponse(existing, tenantID)
		}
		return nil, errcode.WithData(errcode.CodeDBError, map[string]interface{}{
			"error": "Failed to create installation record: " + err.Error(),
		})
	}

	// 3. Download bundle from Horizon
	bundle, err := s.downloadBundleFromHorizon(ctx, req.MarketToken, req.BundleKey, req.Version)
	if err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "DOWNLOAD_FAILED", err)
	}

	// Update to DOWNLOADED state
	if err := s.transitionInstallation(ctx, inst.ID, tenantID, model.InstallStateDownloading, model.InstallStateDownloaded); err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "STATE_PERSIST_FAILED", err)
	}

	// 4. Verify bundle (hash, signature, schema, compatibility)
	verificationWarnings, err := s.verifyBundle(ctx, inst.ID, tenantID, bundle, req.MarketToken)
	if err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "VERIFICATION_FAILED", err)
	}

	// Update to VERIFIED state
	if err := s.transitionInstallation(ctx, inst.ID, tenantID, model.InstallStateDownloaded, model.InstallStateVerified); err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "STATE_PERSIST_FAILED", err)
	}

	// 5. Install device templates
	warnings := append([]string(nil), verificationWarnings...)
	deviceTemplateMappings, installWarnings, err := s.installDeviceTemplates(ctx, inst.ID, tenantID, bundle, req.OverwritePolicy)
	if err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "MODELS_INSTALL_FAILED", err)
	}
	warnings = append(warnings, installWarnings...)

	// Update to MODELS_INSTALLED state
	if err := s.transitionInstallation(ctx, inst.ID, tenantID, model.InstallStateVerified, model.InstallStateModelsInstalled); err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "STATE_PERSIST_FAILED", err)
	}

	// 6. Validate all runtime device bindings before creating any dashboard.
	resolvedBindings, err := s.resolveDeviceBindings(ctx, tenantID, bundle, req.DeviceBindings, deviceTemplateMappings)
	if err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "BINDING_FAILED", err)
	}

	// 7. Create dashboards via ThingsVis with real tenant device IDs.
	dashboardMappings, err := s.createDashboards(ctx, inst.ID, tenantID, claims.ID, bundle, resolvedBindings)
	if err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "DASHBOARD_CREATE_FAILED", err)
	}

	// Update to DASHBOARDS_CREATED state
	if err := s.transitionInstallation(ctx, inst.ID, tenantID, model.InstallStateModelsInstalled, model.InstallStateDashboardsCreated); err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "STATE_PERSIST_FAILED", err)
	}

	// 8. Record device bindings
	bindingStatuses, finalWarnings, err := s.processDeviceBindings(ctx, inst.ID, tenantID, bundle, req.DeviceBindings, deviceTemplateMappings)
	if err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "BINDING_FAILED", err)
	}
	warnings = append(warnings, finalWarnings...)

	// Update warnings
	if len(warnings) > 0 {
		if err := s.installRepo.UpdateWarnings(ctx, inst.ID, warnings); err != nil {
			return nil, s.failInstallation(ctx, inst.ID, tenantID, "WARNINGS_PERSIST_FAILED", err)
		}
	}

	// 9. Determine final state
	finalState := model.InstallStateCompleted
	hasUnboundRequired := false
	for _, bs := range bindingStatuses {
		if bs.Required && bs.LocalDeviceID == "" {
			hasUnboundRequired = true
			break
		}
	}

	if hasUnboundRequired {
		finalState = model.InstallStateWaitingForBindings
	}
	// Update to final state
	if err := s.installRepo.FinalizeWithNotification(
		ctx,
		inst.ID,
		finalState,
		&model.MarketInstallNotificationOutbox{
			TenantID:      tenantID,
			BundleKey:     req.BundleKey,
			BundleVersion: req.Version,
			MarketToken:   req.MarketToken,
		},
		newInstallationAudit(tenantID, "state_change", model.InstallStateDashboardsCreated, finalState, nil),
		newInstallationAudit(tenantID, "install_completed", "", finalState, nil),
	); err != nil {
		return nil, s.failInstallation(ctx, inst.ID, tenantID, "STATE_PERSIST_FAILED", err)
	}

	// Build response
	return s.buildInstallResponse(inst.ID, req.BundleKey, req.Version, finalState, deviceTemplateMappings, dashboardMappings, bindingStatuses, warnings, false, "")
}

func hashInstallRequest(req *model.InstallBundleRequest, tenantID string) (string, error) {
	bindings := append([]model.DeviceBindingInput(nil), req.DeviceBindings...)
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].BindingKey == bindings[j].BindingKey {
			return bindings[i].LocalDeviceID < bindings[j].LocalDeviceID
		}
		return bindings[i].BindingKey < bindings[j].BindingKey
	})
	payload, err := json.Marshal(struct {
		TenantID       string                     `json:"tenantId"`
		BundleKey      string                     `json:"bundleKey"`
		Version        string                     `json:"version"`
		DeviceBindings []model.DeviceBindingInput `json:"deviceBindings"`
		Overwrite      string                     `json:"overwritePolicy"`
	}{tenantID, req.BundleKey, req.Version, bindings, req.OverwritePolicy})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:]), nil
}

// downloadBundleFromHorizon downloads the bundle from Horizon market
func (s *MarketBundleInstallService) downloadBundleFromHorizon(ctx context.Context, token, bundleKey, version string) (*model.HorizonBundleDownload, error) {
	baseURL := s.marketClient.baseURL
	url := fmt.Sprintf("%s/api/market/bundles/%s/versions/%s/download", baseURL, bundleKey, version)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeSystemError, map[string]interface{}{
			"error": "Failed to download bundle from Horizon: " + err.Error(),
		})
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, errcode.WithData(errcode.CodeSystemError, map[string]interface{}{
			"error": fmt.Sprintf("Horizon download failed with status %d: %s", resp.StatusCode, string(body)),
		})
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeSystemError, map[string]interface{}{
			"error": "Failed to read download response: " + err.Error(),
		})
	}

	// Parse bundle JSON
	var bundle struct {
		ContractVersion string          `json:"contractVersion"`
		BundleKind      string          `json:"bundleKind"`
		Metadata        json.RawMessage `json:"metadata"`
		Compatibility   json.RawMessage `json:"compatibility"`
		Resources       json.RawMessage `json:"resources"`
		Security        json.RawMessage `json:"security"`
	}
	if err := json.Unmarshal(body, &bundle); err != nil {
		return nil, errcode.WithData(errcode.CodeSystemError, map[string]interface{}{
			"error": "Failed to parse bundle JSON: " + err.Error(),
		})
	}

	// Compute content hash
	hash := sha256.Sum256(body)
	contentHash := "sha256:" + hex.EncodeToString(hash[:])

	return &model.HorizonBundleDownload{
		BundleFileBytes: body,
		ContentHash:     contentHash,
		ContractVersion: bundle.ContractVersion,
		BundleKind:      bundle.BundleKind,
		Metadata:        bundle.Metadata,
		Compatibility:   bundle.Compatibility,
		Resources:       bundle.Resources,
		Security:        bundle.Security,
	}, nil
}

// verifyBundle verifies the downloaded bundle
func (s *MarketBundleInstallService) verifyBundle(ctx context.Context, installID, tenantID string, bundle *model.HorizonBundleDownload, token string) ([]string, error) {
	// 1. Parse security section
	var security struct {
		ContainsSecrets     bool   `json:"containsSecrets"`
		ContainsRuntimeData bool   `json:"containsRuntimeData"`
		ContentHash         string `json:"contentHash"`
		Signature           string `json:"signature"`
	}
	if err := json.Unmarshal(bundle.Security, &security); err != nil {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Failed to parse bundle security section: " + err.Error(),
		})
	}

	// 2. Reject bundles with secrets
	if security.ContainsSecrets {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "BUNDLE_CONTAINS_SECRETS: Bundles containing secrets are not allowed",
		})
	}

	// 3. Verify content hash
	if security.ContentHash != "" && security.ContentHash != bundle.ContentHash {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": fmt.Sprintf("Content hash mismatch: expected %s, got %s", security.ContentHash, bundle.ContentHash),
		})
	}

	// 4. Verify contract version
	if bundle.ContractVersion != "1.0" {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": fmt.Sprintf("Unsupported contract version: %s", bundle.ContractVersion),
		})
	}

	// 5. Verify compatibility (ThingsPanel version, ThingsVis version, plugins)
	var compatibility struct {
		MinThingsPanel string `json:"minThingsPanel"`
		MinThingsVis   string `json:"minThingsVis"`
		Plugins        []struct {
			Identifier string `json:"identifier"`
			MinVersion string `json:"minVersion"`
		} `json:"plugins"`
	}
	if bundle.Compatibility != nil {
		if err := json.Unmarshal(bundle.Compatibility, &compatibility); err != nil {
			return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
				"error": "Failed to parse compatibility section: " + err.Error(),
			})
		}
	}

	// Check plugin dependencies
	var warnings []string
	for _, plugin := range compatibility.Plugins {
		p, err := dal.GetServicePluginByServiceIdentifier(plugin.Identifier)
		if err != nil || p == nil {
			warnings = append(warnings, fmt.Sprintf("Plugin '%s' not found locally", plugin.Identifier))
			if err := s.recordAudit(ctx, installID, tenantID, "plugin_warning", "", "", &model.MarketResourceMapping{
				ResourceType: "plugin",
				LocalName:    plugin.Identifier,
			}); err != nil {
				return nil, dbError("record plugin compatibility warning", err)
			}
		}
	}

	if len(warnings) > 0 {
		if err := s.installRepo.UpdateWarnings(ctx, installID, warnings); err != nil {
			return nil, dbError("save bundle verification warnings", err)
		}
	}

	if err := s.recordAudit(ctx, installID, tenantID, "bundle_verified", "", "", nil); err != nil {
		return nil, err
	}
	return warnings, nil
}

// installDeviceTemplates installs device templates from the bundle
func (s *MarketBundleInstallService) installDeviceTemplates(ctx context.Context, installID, tenantID string, bundle *model.HorizonBundleDownload, overwritePolicy string) ([]*model.ResourceMappingResponse, []string, error) {
	var resources struct {
		DeviceTemplates []struct {
			ResourceKey string `json:"resourceKey"`
			Version     string `json:"version"`
			Name        string `json:"name"`
			Protocol    struct {
				ProtocolType   string                 `json:"protocolType"`
				PublicDefaults map[string]interface{} `json:"publicDefaults"`
			} `json:"protocol"`
			ThingModel []struct {
				Kind        string `json:"kind"`
				Identifier  string `json:"identifier"`
				Name        string `json:"name"`
				DataType    string `json:"dataType"`
				Unit        string `json:"unit"`
				Description string `json:"description"`
				AccessMode  string `json:"accessMode"`
			} `json:"thingModel"`
		} `json:"deviceTemplates"`
	}

	if err := json.Unmarshal(bundle.Resources, &resources); err != nil {
		return nil, nil, errcode.WithData(errcode.CodeSystemError, map[string]interface{}{
			"error": "Failed to parse device templates: " + err.Error(),
		})
	}

	var mappings []*model.ResourceMappingResponse
	var warnings []string

	// Check for existing templates with same name (default: do not overwrite)
	for _, dt := range resources.DeviceTemplates {
		existingTpl, err := dal.GetDeviceTemplateByNameAndTenant(dt.Name, tenantID)
		if err == nil && existingTpl != nil {
			if overwritePolicy != "upgrade" {
				warnings = append(warnings, fmt.Sprintf("Template '%s' already exists locally, skipped", dt.Name))
				mappings = append(mappings, &model.ResourceMappingResponse{
					ResourceType:      model.ResourceTypeDeviceTemplate,
					MarketResourceKey: dt.ResourceKey,
					LocalID:           existingTpl.ID,
					LocalName:         dt.Name,
					Status:            "skipped_existing",
				})
				continue
			}
			return nil, warnings, errcode.WithData(
				errcode.CodeParamError,
				fmt.Sprintf("device template upgrade is not supported: %s", dt.Name),
			)
		}

		// Create new template
		templateID, err := s.createDeviceTemplate(ctx, tenantID, &dt)
		if err != nil {
			return nil, warnings, err
		}

		// Record mapping
		mapping := &model.MarketResourceMapping{
			InstallationID:    installID,
			TenantID:          tenantID,
			ResourceType:      model.ResourceTypeDeviceTemplate,
			MarketResourceKey: dt.ResourceKey,
			MarketVersion:     dt.Version,
			LocalID:           templateID,
			LocalName:         dt.Name,
			Status:            "active",
		}
		if _, err := s.installRepo.CreateResourceMappingWithAudit(
			ctx,
			mapping,
			newInstallationAudit(tenantID, "resource_created", "", "", mapping),
		); err != nil {
			compensationErr := dal.DeleteDeviceTemplateByID(templateID)
			if compensationErr != nil {
				return nil, warnings, errors.Join(
					dbError("record installed device template", err),
					fmt.Errorf("compensate device template %s: %w", templateID, compensationErr),
				)
			}
			return nil, warnings, dbError("record installed device template", err)
		}

		mappings = append(mappings, &model.ResourceMappingResponse{
			ResourceType:      model.ResourceTypeDeviceTemplate,
			MarketResourceKey: dt.ResourceKey,
			LocalID:           templateID,
			LocalName:         dt.Name,
			Status:            "created",
		})

	}

	return mappings, warnings, nil
}

// createDeviceTemplate creates a device template from bundle definition
func (s *MarketBundleInstallService) createDeviceTemplate(ctx context.Context, tenantID string, tmpl *struct {
	ResourceKey string `json:"resourceKey"`
	Version     string `json:"version"`
	Name        string `json:"name"`
	Protocol    struct {
		ProtocolType   string                 `json:"protocolType"`
		PublicDefaults map[string]interface{} `json:"publicDefaults"`
	} `json:"protocol"`
	ThingModel []struct {
		Kind        string `json:"kind"`
		Identifier  string `json:"identifier"`
		Name        string `json:"name"`
		DataType    string `json:"dataType"`
		Unit        string `json:"unit"`
		Description string `json:"description"`
		AccessMode  string `json:"accessMode"`
	} `json:"thingModel"`
}) (string, error) {
	now := time.Now().UTC()
	flag := int16(1) // private

	templateID := uuid.NewString()

	// Create device template
	newTemplate := &model.DeviceTemplate{
		ID:          templateID,
		Name:        tmpl.Name,
		TenantID:    tenantID,
		Version:     &tmpl.Version,
		Description: &tmpl.Name,
		Flag:        &flag,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	tx := global.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return "", tx.Error
	}

	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if err := tx.Create(newTemplate).Error; err != nil {
		tx.Rollback()
		return "", err
	}

	// Create thing model entries
	for _, field := range tmpl.ThingModel {
		switch field.Kind {
		case "telemetry":
			tm := model.DeviceModelTelemetry{
				ID:               uuid.NewString(),
				DeviceTemplateID: templateID,
				TenantID:         tenantID,
				DataName:         &field.Name,
				DataIdentifier:   field.Identifier,
				ReadWriteFlag:    getStringPtr(platformAccessMode(field.AccessMode)),
				DataType:         &field.DataType,
				Unit:             &field.Unit,
				Description:      &field.Description,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if err := tx.Create(&tm).Error; err != nil {
				tx.Rollback()
				return "", err
			}
		case "attribute":
			attr := model.DeviceModelAttribute{
				ID:               uuid.NewString(),
				DeviceTemplateID: templateID,
				TenantID:         tenantID,
				DataName:         &field.Name,
				DataIdentifier:   field.Identifier,
				ReadWriteFlag:    getStringPtr(platformAccessMode(field.AccessMode)),
				DataType:         &field.DataType,
				Unit:             &field.Unit,
				Description:      &field.Description,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if err := tx.Create(&attr).Error; err != nil {
				tx.Rollback()
				return "", err
			}
		case "event":
			evt := model.DeviceModelEvent{
				ID:               uuid.NewString(),
				DeviceTemplateID: templateID,
				TenantID:         tenantID,
				DataName:         &field.Name,
				DataIdentifier:   field.Identifier,
				Description:      &field.Description,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if err := tx.Create(&evt).Error; err != nil {
				tx.Rollback()
				return "", err
			}
		case "command":
			cmd := model.DeviceModelCommand{
				ID:               uuid.NewString(),
				DeviceTemplateID: templateID,
				TenantID:         tenantID,
				DataName:         &field.Name,
				DataIdentifier:   field.Identifier,
				Description:      &field.Description,
				CreatedAt:        now,
				UpdatedAt:        now,
			}
			if err := tx.Create(&cmd).Error; err != nil {
				tx.Rollback()
				return "", err
			}
		}
	}

	if err := tx.Commit().Error; err != nil {
		return "", err
	}

	return templateID, nil
}

// resolveDeviceBindings validates tenant ownership and exact installed template compatibility.
func (s *MarketBundleInstallService) resolveDeviceBindings(
	ctx context.Context,
	tenantID string,
	bundle *model.HorizonBundleDownload,
	input []model.DeviceBindingInput,
	templateMappings []*model.ResourceMappingResponse,
) (map[string]string, error) {
	var resources struct {
		Dashboards []struct {
			DeviceBindings []struct {
				BindingKey        string `json:"bindingKey"`
				DeviceTemplateKey string `json:"deviceTemplateKey"`
			} `json:"deviceBindings"`
		} `json:"dashboards"`
	}
	if err := json.Unmarshal(bundle.Resources, &resources); err != nil {
		return nil, errcode.WithData(errcode.CodeSystemError, "failed to parse dashboard bindings")
	}

	expectedTemplateIDs := make(map[string]string, len(templateMappings))
	for _, mapping := range templateMappings {
		if mapping.ResourceType == model.ResourceTypeDeviceTemplate {
			expectedTemplateIDs[mapping.MarketResourceKey] = mapping.LocalID
		}
	}
	provided := make(map[string]string, len(input))
	for _, binding := range input {
		if _, exists := provided[binding.BindingKey]; exists {
			return nil, errcode.WithData(errcode.CodeParamError, "duplicate device binding: "+binding.BindingKey)
		}
		provided[binding.BindingKey] = binding.LocalDeviceID
	}

	resolved := make(map[string]string)
	expectedKeys := make(map[string]bool)
	for _, dashboard := range resources.Dashboards {
		for _, binding := range dashboard.DeviceBindings {
			expectedKeys[binding.BindingKey] = true
			localDeviceID := provided[binding.BindingKey]
			if localDeviceID == "" {
				return nil, errcode.WithData(errcode.CodeParamError, "device binding is required: "+binding.BindingKey)
			}

			device, err := dal.GetDeviceByID(localDeviceID)
			if err != nil || device == nil || device.TenantID != tenantID {
				return nil, errcode.WithData(errcode.CodeParamError, "bound device is unavailable")
			}
			if device.DeviceConfigID == nil || *device.DeviceConfigID == "" {
				return nil, errcode.WithData(errcode.CodeParamError, "bound device has no device configuration")
			}
			config, err := dal.GetDeviceConfigByID(*device.DeviceConfigID)
			if err != nil || config == nil || config.DeviceTemplateID == nil {
				return nil, errcode.WithData(errcode.CodeParamError, "bound device has no device template")
			}
			expectedTemplateID := expectedTemplateIDs[binding.DeviceTemplateKey]
			if expectedTemplateID == "" || *config.DeviceTemplateID != expectedTemplateID {
				return nil, errcode.WithData(
					errcode.CodeParamError,
					fmt.Sprintf("device binding %s is not compatible with template %s", binding.BindingKey, binding.DeviceTemplateKey),
				)
			}
			resolved[binding.BindingKey] = localDeviceID
		}
	}
	for bindingKey := range provided {
		if !expectedKeys[bindingKey] {
			return nil, errcode.WithData(errcode.CodeParamError, "unknown device binding: "+bindingKey)
		}
	}
	return resolved, nil
}

// createDashboards creates dashboards via ThingsVis import API
func (s *MarketBundleInstallService) createDashboards(ctx context.Context, installID, tenantID, userID string, bundle *model.HorizonBundleDownload, resolvedBindings map[string]string) ([]*model.ResourceMappingResponse, error) {
	var resources struct {
		Dashboards []struct {
			ResourceKey    string          `json:"resourceKey"`
			Version        string          `json:"version"`
			Name           string          `json:"name"`
			SchemaVersion  string          `json:"schemaVersion"`
			CanvasConfig   json.RawMessage `json:"canvasConfig"`
			Nodes          json.RawMessage `json:"nodes"`
			DataSources    json.RawMessage `json:"dataSources"`
			Variables      json.RawMessage `json:"variables"`
			DeviceBindings []struct {
				BindingKey        string `json:"bindingKey"`
				DeviceTemplateKey string `json:"deviceTemplateKey"`
				Required          bool   `json:"required"`
				AllowMany         bool   `json:"allowMany"`
				DisplayName       string `json:"displayName"`
			} `json:"deviceBindings"`
			FieldBindings []struct {
				BindingKey string `json:"bindingKey"`
				Kind       string `json:"kind"`
				Identifier string `json:"identifier"`
				Required   bool   `json:"required"`
			} `json:"fieldBindings"`
		} `json:"dashboards"`
	}

	if err := json.Unmarshal(bundle.Resources, &resources); err != nil {
		return nil, errcode.WithData(errcode.CodeSystemError, map[string]interface{}{
			"error": "Failed to parse dashboards: " + err.Error(),
		})
	}

	var mappings []*model.ResourceMappingResponse

	for _, dash := range resources.Dashboards {
		// Create dashboard via ThingsVis import
		dashboardID, err := s.importDashboard(ctx, tenantID, userID, &dash, resolvedBindings)
		if err != nil {
			return nil, fmt.Errorf("failed to create dashboard %s: %w", dash.Name, err)
		}

		// Record mapping
		mapping := &model.MarketResourceMapping{
			InstallationID:    installID,
			TenantID:          tenantID,
			ResourceType:      model.ResourceTypeDashboard,
			MarketResourceKey: dash.ResourceKey,
			MarketVersion:     dash.Version,
			LocalID:           dashboardID,
			LocalName:         dash.Name,
			Status:            "active",
		}
		bindingRecords := make([]*model.MarketBundleBindingStatus, 0, len(dash.DeviceBindings))
		for _, db := range dash.DeviceBindings {
			localDeviceID := resolvedBindings[db.BindingKey]
			bindingStatus := model.BindingStatusPending
			if localDeviceID != "" {
				bindingStatus = model.BindingStatusBound
			}
			bindingRecords = append(bindingRecords, &model.MarketBundleBindingStatus{
				InstallationID:    installID,
				BindingKey:        db.BindingKey,
				DeviceTemplateKey: db.DeviceTemplateKey,
				Required:          db.Required,
				LocalDeviceID:     localDeviceID,
				Status:            bindingStatus,
			})
		}
		if err := s.installRepo.CreateDashboardRecords(
			ctx,
			mapping,
			newInstallationAudit(tenantID, "resource_created", "", "", mapping),
			bindingRecords,
		); err != nil {
			compensationErr := s.thingsVis.DeleteDashboard(context.WithoutCancel(ctx), tenantID, dashboardID)
			if compensationErr != nil {
				return nil, errors.Join(
					dbError("record imported dashboard", err),
					fmt.Errorf("compensate dashboard %s: %w", dashboardID, compensationErr),
				)
			}
			return nil, dbError("record imported dashboard", err)
		}

		mappings = append(mappings, &model.ResourceMappingResponse{
			ResourceType:      model.ResourceTypeDashboard,
			MarketResourceKey: dash.ResourceKey,
			LocalID:           dashboardID,
			LocalName:         dash.Name,
			Status:            "created",
		})

	}

	return mappings, nil
}

// importDashboard imports a dashboard template into ThingsVis
func (s *MarketBundleInstallService) importDashboard(ctx context.Context, tenantID, userID string, dash *struct {
	ResourceKey    string          `json:"resourceKey"`
	Version        string          `json:"version"`
	Name           string          `json:"name"`
	SchemaVersion  string          `json:"schemaVersion"`
	CanvasConfig   json.RawMessage `json:"canvasConfig"`
	Nodes          json.RawMessage `json:"nodes"`
	DataSources    json.RawMessage `json:"dataSources"`
	Variables      json.RawMessage `json:"variables"`
	DeviceBindings []struct {
		BindingKey        string `json:"bindingKey"`
		DeviceTemplateKey string `json:"deviceTemplateKey"`
		Required          bool   `json:"required"`
		AllowMany         bool   `json:"allowMany"`
		DisplayName       string `json:"displayName"`
	} `json:"deviceBindings"`
	FieldBindings []struct {
		BindingKey string `json:"bindingKey"`
		Kind       string `json:"kind"`
		Identifier string `json:"identifier"`
		Required   bool   `json:"required"`
	} `json:"fieldBindings"`
}, resolvedBindings map[string]string) (string, error) {
	importReq := ThingsVisImportRequest{
		Name: dash.Name,
		DashboardSnapshot: ThingsVisMarketSnapshot{
			Name:          dash.Name,
			SchemaVersion: dash.SchemaVersion,
			CanvasConfig:  dash.CanvasConfig,
			Nodes:         dash.Nodes,
			DataSources:   dash.DataSources,
			Variables:     dash.Variables,
		},
		DeviceBindings: func() []DeviceBindingImport {
			result := make([]DeviceBindingImport, 0, len(dash.DeviceBindings))
			for _, db := range dash.DeviceBindings {
				result = append(result, DeviceBindingImport{
					BindingKey:    db.BindingKey,
					LocalDeviceID: resolvedBindings[db.BindingKey],
				})
			}
			return result
		}(),
	}

	return s.thingsVis.ImportDashboard(ctx, tenantID, userID, &importReq)
}

// processDeviceBindings validates and records device bindings
func (s *MarketBundleInstallService) processDeviceBindings(ctx context.Context, installID, tenantID string, bundle *model.HorizonBundleDownload, bindings []model.DeviceBindingInput, templateMappings []*model.ResourceMappingResponse) ([]*model.BindingStatusResponse, []string, error) {
	var resources struct {
		Dashboards []struct {
			DeviceBindings []struct {
				BindingKey        string `json:"bindingKey"`
				DeviceTemplateKey string `json:"deviceTemplateKey"`
				Required          bool   `json:"required"`
				AllowMany         bool   `json:"allowMany"`
				DisplayName       string `json:"displayName"`
			} `json:"deviceBindings"`
		} `json:"dashboards"`
	}

	if err := json.Unmarshal(bundle.Resources, &resources); err != nil {
		return nil, nil, err
	}

	var allBindings []struct {
		BindingKey        string
		DeviceTemplateKey string
		Required          bool
		AllowMany         bool
		DisplayName       string
	}
	for _, dash := range resources.Dashboards {
		for _, db := range dash.DeviceBindings {
			allBindings = append(allBindings, struct {
				BindingKey        string
				DeviceTemplateKey string
				Required          bool
				AllowMany         bool
				DisplayName       string
			}{
				BindingKey:        db.BindingKey,
				DeviceTemplateKey: db.DeviceTemplateKey,
				Required:          db.Required,
				AllowMany:         db.AllowMany,
				DisplayName:       db.DisplayName,
			})
		}
	}

	var responses []*model.BindingStatusResponse
	var warnings []string

	// Build template key to ID mapping
	templateMap := make(map[string]string)
	for _, m := range templateMappings {
		if m.ResourceType == model.ResourceTypeDeviceTemplate {
			templateMap[m.MarketResourceKey] = m.LocalID
		}
	}

	for _, expectedBinding := range allBindings {
		response := &model.BindingStatusResponse{
			BindingKey:        expectedBinding.BindingKey,
			DeviceTemplateKey: expectedBinding.DeviceTemplateKey,
			Required:          expectedBinding.Required,
			Status:            model.BindingStatusPending,
		}

		// Find user-provided binding
		var userBinding *model.DeviceBindingInput
		for i := range bindings {
			if bindings[i].BindingKey == expectedBinding.BindingKey {
				userBinding = &bindings[i]
				break
			}
		}

		if userBinding != nil {
			// Validate device belongs to tenant
			device, err := dal.GetDeviceByID(userBinding.LocalDeviceID)
			if err != nil || device == nil {
				return nil, warnings, errcode.WithData(errcode.CodeParamError, "bound device is unavailable")
			}

			if device.TenantID != tenantID {
				return nil, warnings, errcode.WithData(errcode.CodeParamError, "bound device does not belong to current tenant")
			}

			// Validate device template is compatible
			expectedTemplateID := templateMap[expectedBinding.DeviceTemplateKey]
			if device.DeviceConfigID == nil || expectedTemplateID == "" {
				return nil, warnings, errcode.WithData(errcode.CodeParamError, "bound device template is unavailable")
			}
			dc, err := dal.GetDeviceConfigByID(*device.DeviceConfigID)
			if err != nil || dc == nil || dc.DeviceTemplateID == nil {
				return nil, warnings, errcode.WithData(errcode.CodeParamError, "bound device configuration is unavailable")
			}
			if *dc.DeviceTemplateID != expectedTemplateID {
				return nil, warnings, errcode.WithData(
					errcode.CodeParamError,
					fmt.Sprintf("device binding %s is incompatible with its device template", expectedBinding.BindingKey),
				)
			}

			// Update binding status
			if err := s.updateBindingStatus(ctx, installID, expectedBinding.BindingKey, userBinding.LocalDeviceID, model.BindingStatusBound, ""); err != nil {
				return nil, warnings, dbError("update device binding status", err)
			}

			response.LocalDeviceID = userBinding.LocalDeviceID
			response.Status = model.BindingStatusBound
		}

		responses = append(responses, response)
	}

	return responses, warnings, nil
}

// updateBindingStatus updates a binding status record
func (s *MarketBundleInstallService) updateBindingStatus(ctx context.Context, installID, bindingKey, localDeviceID, status, errorMessage string) error {
	binding, err := s.installRepo.GetBindingByKey(ctx, installID, bindingKey)
	if err != nil {
		return err
	}
	return s.installRepo.UpdateBindingDevice(ctx, binding.ID, localDeviceID, status, errorMessage)
}

// failInstallation marks an installation as failed and reports persistence or compensation failures.
func (s *MarketBundleInstallService) failInstallation(ctx context.Context, installID, tenantID, errorCode string, cause error) error {
	logrus.Errorf("Installation %s failed: %s - %s", installID, errorCode, cause.Error())
	persistenceContext := context.WithoutCancel(ctx)

	if err := s.installRepo.UpdateStatusWithAudits(
		persistenceContext,
		installID,
		model.InstallStateFailed,
		errorCode,
		cause.Error(),
		newInstallationAudit(tenantID, "state_change", "", model.InstallStateFailed, nil),
	); err != nil {
		return errors.Join(cause, dbError("persist failed installation state", err))
	}
	if err := s.checkCompensationNeeded(persistenceContext, installID, tenantID); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// checkCompensationNeeded determines if compensation is required
func (s *MarketBundleInstallService) checkCompensationNeeded(ctx context.Context, installID, tenantID string) error {
	mappings, err := s.installRepo.GetResourceMappingsByInstallation(ctx, installID)
	if err != nil {
		return dbError("query resources for compensation", err)
	}

	hasCreatedResources := false
	for _, m := range mappings {
		if m.Status == "active" {
			hasCreatedResources = true
			break
		}
	}

	if hasCreatedResources {
		return s.installRepo.UpdateStatusWithAudits(
			ctx,
			installID,
			model.InstallStateCompensationRequired,
			"COMPENSATION_NEEDED",
			"Some resources were created before failure",
			newInstallationAudit(
				tenantID,
				"state_change",
				model.InstallStateFailed,
				model.InstallStateCompensationRequired,
				nil,
			),
		)
	}
	return nil
}

// CompensateInstallation removes resources created during a failed installation
func (s *MarketBundleInstallService) CompensateInstallation(ctx context.Context, installID, tenantID string) error {
	inst, err := s.installRepo.GetByID(ctx, installID)
	if err != nil {
		return errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation not found: " + err.Error(),
		})
	}
	if inst.TenantID != tenantID {
		return errcode.WithData(errcode.CodeNotFound, "installation not found")
	}

	if inst.Status != model.InstallStateCompensationRequired && inst.Status != model.InstallStateFailed {
		return errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation is not in compensation required state",
		})
	}

	if err := s.recordAudit(ctx, installID, tenantID, "compensation_started", inst.Status, "COMPENSATING", nil); err != nil {
		return dbError("record compensation start", err)
	}

	// Get all created resources
	mappings, err := s.installRepo.GetResourceMappingsByInstallation(ctx, installID)
	if err != nil {
		return err
	}

	// Delete created resources
	for _, m := range mappings {
		if m.Status == "active" {
			switch m.ResourceType {
			case model.ResourceTypeDeviceTemplate:
				// Delete device template (should cascade to models)
				if err := dal.DeleteDeviceTemplateByID(m.LocalID); err != nil {
					return fmt.Errorf("delete device template %s: %w", m.LocalID, err)
				}
			case model.ResourceTypeDashboard:
				// Delete dashboard via ThingsVis
				if err := s.thingsVis.DeleteDashboard(ctx, tenantID, m.LocalID); err != nil {
					return fmt.Errorf("delete dashboard %s: %w", m.LocalID, err)
				}
			}

			// Mark mapping as deleted
			audit := newInstallationAudit(tenantID, "resource_deleted", "", "", m)
			audit.InstallationID = installID
			if err := s.installRepo.UpdateResourceMappingStatusWithAudit(ctx, m.ID, "deleted", audit); err != nil {
				return dbError("record compensated resource", err)
			}
		}
	}

	// Update installation status
	return s.installRepo.UpdateStatusWithAudits(
		ctx,
		installID,
		model.InstallStateFailed,
		"COMPENSATED",
		"Resources cleaned up",
		newInstallationAudit(tenantID, "compensation_completed", "", model.InstallStateFailed, nil),
	)
}

// GetInstallationStatus retrieves installation status with full details
func (s *MarketBundleInstallService) GetInstallationStatus(ctx context.Context, installID, tenantID string) (*model.InstallBundleResponse, error) {
	inst, err := s.installRepo.GetByID(ctx, installID)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation not found",
		})
	}

	if inst.TenantID != tenantID {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation not found",
		})
	}

	mappings, err := s.installRepo.GetResourceMappingsByInstallation(ctx, installID)
	if err != nil {
		return nil, dbError("list installation resource mappings", err)
	}
	bindings, err := s.installRepo.GetBindingStatusesByInstallation(ctx, installID)
	if err != nil {
		return nil, dbError("list installation binding statuses", err)
	}

	var resourceMappings []*model.ResourceMappingResponse
	for _, m := range mappings {
		resourceMappings = append(resourceMappings, &model.ResourceMappingResponse{
			ResourceType:      m.ResourceType,
			MarketResourceKey: m.MarketResourceKey,
			LocalID:           m.LocalID,
			LocalName:         m.LocalName,
			Status:            m.Status,
		})
	}

	var bindingStatuses []*model.BindingStatusResponse
	for _, b := range bindings {
		bindingStatuses = append(bindingStatuses, &model.BindingStatusResponse{
			BindingKey:        b.BindingKey,
			DeviceTemplateKey: b.DeviceTemplateKey,
			Required:          b.Required,
			LocalDeviceID:     b.LocalDeviceID,
			Status:            b.Status,
			ErrorMessage:      b.ErrorMessage,
		})
	}

	var warnings []string
	if inst.Warnings != nil {
		if err := json.Unmarshal(inst.Warnings, &warnings); err != nil {
			return nil, dbError("decode installation warnings", err)
		}
	}

	return &model.InstallBundleResponse{
		InstallationID:   inst.ID,
		BundleKey:        inst.BundleKey,
		Version:          inst.BundleVersion,
		Status:           inst.Status,
		ResourceMappings: resourceMappings,
		BindingStatus:    bindingStatuses,
		Warnings:         warnings,
		IsIdempotent:     false,
	}, nil
}

// UpdateDeviceBinding updates a device binding for a WAITING_FOR_BINDINGS installation
func (s *MarketBundleInstallService) UpdateDeviceBinding(ctx context.Context, installID, tenantID string, req *model.UpdateBindingRequest) error {
	inst, err := s.installRepo.GetByID(ctx, installID)
	if err != nil {
		return errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation not found",
		})
	}

	if inst.TenantID != tenantID {
		return errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation not found",
		})
	}

	if inst.Status != model.InstallStateWaitingForBindings {
		return errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation is not in WAITING_FOR_BINDINGS state",
		})
	}

	// Validate device
	device, err := dal.GetDeviceByID(req.LocalDeviceID)
	if err != nil || device == nil {
		return errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Device not found",
		})
	}

	if device.TenantID != tenantID {
		return errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Device does not belong to current tenant",
		})
	}

	// Update binding
	if err := s.updateBindingStatus(ctx, installID, req.BindingKey, req.LocalDeviceID, model.BindingStatusBound, ""); err != nil {
		return err
	}

	// Check if all required bindings are now complete
	bindings, err := s.installRepo.GetBindingStatusesByInstallation(ctx, installID)
	if err != nil {
		return err
	}

	allRequiredBound := true
	for _, b := range bindings {
		if b.Required && b.LocalDeviceID == "" {
			allRequiredBound = false
			break
		}
	}

	if allRequiredBound {
		return s.installRepo.UpdateStatusWithAudits(
			ctx,
			installID,
			model.InstallStateCompleted,
			"",
			"",
			newInstallationAudit(tenantID, "state_change", model.InstallStateWaitingForBindings, model.InstallStateCompleted, nil),
			newInstallationAudit(tenantID, "all_bindings_complete", "", "", nil),
		)
	}

	return nil
}

// RetryInstallation retries a failed installation
func (s *MarketBundleInstallService) RetryInstallation(ctx context.Context, installID, tenantID string, req *model.RetryInstallationRequest) (*model.InstallBundleResponse, error) {
	inst, err := s.installRepo.GetByID(ctx, installID)
	if err != nil {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation not found",
		})
	}

	if inst.TenantID != tenantID {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Installation not found",
		})
	}

	if inst.Status != model.InstallStateFailed && inst.Status != model.InstallStateCompensationRequired {
		return nil, errcode.WithData(errcode.CodeParamError, map[string]interface{}{
			"error": "Only failed installations can be retried",
		})
	}

	if err := s.installRepo.UpdateStatusWithAudits(
		ctx,
		inst.ID,
		model.InstallStateDownloading,
		"",
		"",
		newInstallationAudit(tenantID, "retry_started", inst.Status, model.InstallStateDownloading, nil),
	); err != nil {
		return nil, err
	}

	// Return a placeholder response - in a real implementation, we'd need to re-download
	return &model.InstallBundleResponse{
		InstallationID: inst.ID,
		BundleKey:      inst.BundleKey,
		Version:        inst.BundleVersion,
		Status:         model.InstallStateDownloading,
		IsIdempotent:   false,
	}, nil
}

// ListInstallations lists all installations for a tenant
func (s *MarketBundleInstallService) ListInstallations(ctx context.Context, tenantID string, q *model.ListInstallationsRequest) (*model.ListInstallationsResponse, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PageSize < 1 || q.PageSize > 100 {
		q.PageSize = 20
	}

	installations, total, err := s.installRepo.ListByTenant(ctx, tenantID, q)
	if err != nil {
		return nil, err
	}

	return &model.ListInstallationsResponse{
		Data:     installations,
		Total:    int(total),
		Page:     q.Page,
		PageSize: q.PageSize,
	}, nil
}

// --- Helper methods ---

func (s *MarketBundleInstallService) generateIdempotencyKey(bundleKey, version, tenantID string) string {
	data := fmt.Sprintf("%s:%s:%s", bundleKey, version, tenantID)
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

func (s *MarketBundleInstallService) buildIdempotentResponse(inst *model.MarketBundleInstallation, tenantID string) (*model.InstallBundleResponse, error) {
	mappings, err := s.installRepo.GetResourceMappingsByInstallation(context.Background(), inst.ID)
	if err != nil {
		return nil, dbError("list idempotent resource mappings", err)
	}
	bindings, err := s.installRepo.GetBindingStatusesByInstallation(context.Background(), inst.ID)
	if err != nil {
		return nil, dbError("list idempotent binding statuses", err)
	}

	var resourceMappings []*model.ResourceMappingResponse
	for _, m := range mappings {
		resourceMappings = append(resourceMappings, &model.ResourceMappingResponse{
			ResourceType:      m.ResourceType,
			MarketResourceKey: m.MarketResourceKey,
			LocalID:           m.LocalID,
			LocalName:         m.LocalName,
			Status:            m.Status,
		})
	}

	var bindingStatuses []*model.BindingStatusResponse
	for _, b := range bindings {
		bindingStatuses = append(bindingStatuses, &model.BindingStatusResponse{
			BindingKey:        b.BindingKey,
			DeviceTemplateKey: b.DeviceTemplateKey,
			Required:          b.Required,
			LocalDeviceID:     b.LocalDeviceID,
			Status:            b.Status,
			ErrorMessage:      b.ErrorMessage,
		})
	}

	return &model.InstallBundleResponse{
		InstallationID:    inst.ID,
		BundleKey:         inst.BundleKey,
		Version:           inst.BundleVersion,
		Status:            inst.Status,
		ResourceMappings:  resourceMappings,
		BindingStatus:     bindingStatuses,
		IsIdempotent:      true,
		ExistingInstallID: inst.ID,
	}, nil
}

func (s *MarketBundleInstallService) buildInstallResponse(installID, bundleKey, version, status string, templateMappings []*model.ResourceMappingResponse, dashboardMappings []*model.ResourceMappingResponse, bindingStatuses []*model.BindingStatusResponse, warnings []string, isIdempotent bool, existingID string) (*model.InstallBundleResponse, error) {
	// Combine all resource mappings
	allMappings := append(templateMappings, dashboardMappings...)

	return &model.InstallBundleResponse{
		InstallationID:    installID,
		BundleKey:         bundleKey,
		Version:           version,
		Status:            status,
		ResourceMappings:  allMappings,
		BindingStatus:     bindingStatuses,
		Warnings:          warnings,
		IsIdempotent:      isIdempotent,
		ExistingInstallID: existingID,
	}, nil
}

func newInstallationAudit(tenantID, action, prevState, newState string, mapping *model.MarketResourceMapping) *model.MarketInstallationAudit {
	audit := &model.MarketInstallationAudit{
		TenantID:  tenantID,
		Action:    action,
		PrevState: prevState,
		NewState:  newState,
	}
	if mapping != nil {
		audit.ResourceType = mapping.ResourceType
		audit.ResourceKey = mapping.MarketResourceKey
		audit.LocalID = mapping.LocalID
	}
	return audit
}

func (s *MarketBundleInstallService) recordAudit(ctx context.Context, installID, tenantID, action, prevState, newState string, mapping *model.MarketResourceMapping) error {
	audit := newInstallationAudit(tenantID, action, prevState, newState, mapping)
	audit.InstallationID = installID
	_, err := s.installRepo.CreateAuditEntry(ctx, audit)
	return err
}

func (s *MarketBundleInstallService) transitionInstallation(
	ctx context.Context,
	installID, tenantID, previousState, nextState string,
) error {
	return s.installRepo.UpdateStatusWithAudits(
		ctx,
		installID,
		nextState,
		"",
		"",
		newInstallationAudit(tenantID, "state_change", previousState, nextState, nil),
	)
}

func (s *MarketBundleInstallService) notifyHorizonInstallComplete(ctx context.Context, token, bundleKey, version, tenantID, installID string) error {
	if token == "" {
		return errors.New("Horizon install notification requires a market token")
	}

	baseURL := s.marketClient.baseURL
	url := fmt.Sprintf("%s/api/market/bundles/installations/%s/status", baseURL, installID)

	reqBody := map[string]string{
		"bundleKey": bundleKey,
		"version":   version,
		"tenantId":  tenantID,
		"status":    "completed",
	}
	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encode Horizon install notification: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBytes))
	if err != nil {
		return fmt.Errorf("create Horizon install notification request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notify Horizon of installation: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("Horizon install notification status %d; read response: %w", resp.StatusCode, readErr)
		}
		return &horizonNotificationError{statusCode: resp.StatusCode, message: fmt.Sprintf("Horizon install notification failed with status %d: %s", resp.StatusCode, string(body))}
	}
	return nil
}

type horizonNotificationError struct {
	statusCode int
	message    string
}

func (e *horizonNotificationError) Error() string { return e.message }

func (s *MarketBundleInstallService) DeliverPendingNotifications(ctx context.Context, limit int) error {
	jobs, err := s.installRepo.ClaimDueNotifications(ctx, limit, time.Minute)
	if err != nil {
		return err
	}
	var deliveryErrors []error
	for _, job := range jobs {
		if job.ClaimToken == nil {
			deliveryErrors = append(deliveryErrors, fmt.Errorf("notification %s has no claim token", job.ID))
			continue
		}
		deliveryErr := s.notifyHorizonInstallComplete(
			ctx,
			job.MarketToken,
			job.BundleKey,
			job.BundleVersion,
			job.TenantID,
			job.InstallationID,
		)
		if deliveryErr == nil {
			if err := s.installRepo.MarkNotificationDelivered(ctx, job.ID, *job.ClaimToken, job.Attempts); err != nil {
				deliveryErrors = append(deliveryErrors, err)
			}
			continue
		}
		var remoteErr *horizonNotificationError
		if errors.As(deliveryErr, &remoteErr) && remoteErr.statusCode == http.StatusUnauthorized {
			if err := s.installRepo.MarkNotificationCredentialRequired(ctx, job.ID, *job.ClaimToken, job.Attempts, deliveryErr.Error()); err != nil {
				deliveryErrors = append(deliveryErrors, errors.Join(deliveryErr, err))
			}
			continue
		}
		delay := time.Duration(5*(1<<min(job.Attempts-1, 9))) * time.Second
		if err := s.installRepo.MarkNotificationRetry(
			ctx,
			job.ID,
			*job.ClaimToken,
			job.Attempts,
			time.Now().UTC().Add(delay),
			deliveryErr.Error(),
		); err != nil {
			deliveryErrors = append(deliveryErrors, errors.Join(deliveryErr, err))
		}
	}
	return errors.Join(deliveryErrors...)
}

// getStringPtr returns a pointer to a string
func getStringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
