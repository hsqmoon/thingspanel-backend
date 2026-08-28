package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"project/internal/model"
	"project/internal/repo"
	"project/pkg/global"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCompensateInstallationRejectsCrossTenantBeforeAuditOrDelete(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:market-compensate-tenant?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.MarketBundleInstallation{}, &model.MarketInstallationAudit{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previous := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	installation := &model.MarketBundleInstallation{ID: "install-1", TenantID: "tenant-owner", Status: model.InstallStateFailed}
	if err := db.Create(installation).Error; err != nil {
		t.Fatalf("create installation: %v", err)
	}
	service := &MarketBundleInstallService{installRepo: repo.NewMarketBundleInstallRepo()}
	if err := service.CompensateInstallation(context.Background(), installation.ID, "tenant-attacker"); err == nil {
		t.Fatal("expected cross-tenant rejection")
	}
	var audits int64
	if err := db.Model(&model.MarketInstallationAudit{}).Count(&audits).Error; err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if audits != 0 {
		t.Fatalf("audit count = %d, want 0 before ownership", audits)
	}
}

func TestInstallRequestHashIgnoresCredentialRotationButRejectsBusinessChanges(t *testing.T) {
	base := &model.InstallBundleRequest{BundleKey: "bundle", Version: "1", MarketToken: "old", DeviceBindings: []model.DeviceBindingInput{{BindingKey: "sensor", LocalDeviceID: "device-1"}}}
	rotated := *base
	rotated.MarketToken = "new"
	hash1, err := hashInstallRequest(base, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	hash2, err := hashInstallRequest(&rotated, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Fatal("credential rotation changed business request hash")
	}
	changed := rotated
	changed.DeviceBindings = []model.DeviceBindingInput{{BindingKey: "sensor", LocalDeviceID: "device-2"}}
	hash3, err := hashInstallRequest(&changed, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	if hash1 == hash3 {
		t.Fatal("different binding produced the same request hash")
	}
}

func TestMarketInstallNotificationRetriesThenDelivers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:market-notify-retry?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.MarketBundleInstallation{}, &model.MarketInstallationAudit{}, &model.MarketInstallNotificationOutbox{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previous := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	repository := repo.NewMarketBundleInstallRepo()
	installation, err := repository.CreateInstallation(context.Background(), &model.MarketBundleInstallation{TenantID: "tenant", Status: model.InstallStateDashboardsCreated})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeWithNotification(context.Background(), installation.ID, model.InstallStateCompleted, &model.MarketInstallNotificationOutbox{
		TenantID: "tenant", BundleKey: "bundle", BundleVersion: "1", MarketToken: "token",
	}); err != nil {
		t.Fatal(err)
	}
	service := &MarketBundleInstallService{installRepo: repository, marketClient: &MarketClient{baseURL: server.URL}, httpClient: server.Client()}
	if err := service.DeliverPendingNotifications(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.MarketInstallNotificationOutbox{}).Where("installation_id = ?", installation.ID).Update("next_retry_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverPendingNotifications(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var outbox model.MarketInstallNotificationOutbox
	if err := db.Where("installation_id = ?", installation.ID).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if outbox.Status != model.MarketInstallNotifyDelivered || outbox.Attempts != 2 {
		t.Fatalf("outbox status=%s attempts=%d, want delivered/2", outbox.Status, outbox.Attempts)
	}
}

func TestMarketInstallNotification401WaitsForCredentialRefresh(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:market-notify-credential?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.MarketBundleInstallation{}, &model.MarketInstallationAudit{}, &model.MarketInstallNotificationOutbox{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previous := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh-token" {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	repository := repo.NewMarketBundleInstallRepo()
	installation, err := repository.CreateInstallation(context.Background(), &model.MarketBundleInstallation{TenantID: "tenant", Status: model.InstallStateDashboardsCreated})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.FinalizeWithNotification(context.Background(), installation.ID, model.InstallStateCompleted, &model.MarketInstallNotificationOutbox{
		TenantID: "tenant", BundleKey: "bundle", BundleVersion: "1", MarketToken: "expired-token",
	}); err != nil {
		t.Fatal(err)
	}
	service := &MarketBundleInstallService{installRepo: repository, marketClient: &MarketClient{baseURL: server.URL}, httpClient: server.Client()}
	if err := service.DeliverPendingNotifications(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var outbox model.MarketInstallNotificationOutbox
	if err := db.Where("installation_id = ?", installation.ID).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if outbox.Status != model.MarketInstallNotifyCredential {
		t.Fatalf("status = %s, want credential_required", outbox.Status)
	}
	if err := repository.RefreshNotificationCredential(context.Background(), installation.ID, "tenant", "fresh-token"); err != nil {
		t.Fatal(err)
	}
	if err := service.DeliverPendingNotifications(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("installation_id = ?", installation.ID).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	if outbox.Status != model.MarketInstallNotifyDelivered {
		t.Fatalf("status = %s, want delivered", outbox.Status)
	}
}

func TestNotifyHorizonInstallCompleteReturnsRemoteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	service := &MarketBundleInstallService{
		marketClient: &MarketClient{baseURL: server.URL},
		httpClient:   server.Client(),
	}
	if err := service.notifyHorizonInstallComplete(context.Background(), "token", "bundle", "1.0.0", "tenant", "install"); err == nil {
		t.Fatal("expected Horizon notification failure")
	}
}

func TestSaveDashboardTemplatesCompensatesEarlierTemplatesWhenLaterWriteFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:dashboard-template-compensation?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.LocalDashboardTemplate{}, &model.LocalDashboardTemplateBinding{}); err != nil {
		t.Fatalf("migrate dashboard templates: %v", err)
	}
	previous := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail-second-dashboard-template", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Name != "LocalDashboardTemplate" {
			return
		}
		template, ok := tx.Statement.Dest.(*model.LocalDashboardTemplate)
		if ok && template.Name == "Second" {
			tx.AddError(errors.New("injected second template failure"))
		}
	}); err != nil {
		t.Fatalf("register create failure: %v", err)
	}

	service := &DashboardTemplateService{repo: repo.NewDashboardTemplateRepo()}
	_, err = service.saveDashboardTemplates(
		context.Background(),
		"tenant-1",
		&model.DownloadDashboardTemplateRequest{BundleKey: "bundle-1", Version: "1.0.0"},
		&model.HorizonBundleDownload{Resources: json.RawMessage(`{
			"dashboards":[
				{"resourceKey":"first","name":"First","version":"1.0.0","dataSources":[]},
				{"resourceKey":"second","name":"Second","version":"1.0.0","dataSources":[]}
			]
		}`)},
		nil,
	)
	if err == nil {
		t.Fatal("expected second template write failure")
	}
	var count int64
	if err := db.Model(&model.LocalDashboardTemplate{}).Count(&count).Error; err != nil {
		t.Fatalf("count templates: %v", err)
	}
	if count != 0 {
		t.Fatalf("template count = %d, want 0 after compensation", count)
	}
}

func TestVerifyBundleRejectsMalformedCompatibilityInsteadOfContinuing(t *testing.T) {
	service := &MarketBundleInstallService{}
	_, err := service.verifyBundle(
		context.Background(),
		"install-1",
		"tenant-1",
		&model.HorizonBundleDownload{
			ContractVersion: "1.0",
			Security:        json.RawMessage(`{"containsSecrets":false}`),
			Compatibility:   json.RawMessage(`{"plugins":`),
		},
		"token",
	)
	if err == nil {
		t.Fatal("expected malformed compatibility error")
	}
}

func TestSaveDashboardTemplatesRejectsMalformedMetadataBeforeWriting(t *testing.T) {
	service := &DashboardTemplateService{}
	_, err := service.saveDashboardTemplates(
		context.Background(),
		"tenant-1",
		&model.DownloadDashboardTemplateRequest{BundleKey: "bundle-1", Version: "1.0.0"},
		&model.HorizonBundleDownload{
			Metadata: json.RawMessage(`{"description":`),
			Resources: json.RawMessage(`{
				"dashboards":[{"resourceKey":"dashboard-1","name":"Dashboard","version":"1.0.0"}]
			}`),
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected malformed metadata error")
	}
}

func TestResolveDashboardDataSourceBindingsPreservesReferences(t *testing.T) {
	raw := json.RawMessage(`[
		{
			"id":"__platform_binding_room_sensor__",
			"type":"PLATFORM_FIELD",
			"config":{
				"source":"platform",
				"deviceBinding":{"$deviceBinding":"room_sensor"},
				"requestedFields":["temperature"]
			}
		}
	]`)

	result, err := resolveDashboardDataSourceBindings(raw, map[string]string{
		"room_sensor": "device-1",
	})
	if err != nil {
		t.Fatalf("resolveDashboardDataSourceBindings() error = %v", err)
	}

	var dataSources []map[string]interface{}
	if err := json.Unmarshal(result, &dataSources); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if got := dataSources[0]["id"]; got != "__platform_binding_room_sensor__" {
		t.Fatalf("data source id = %v, want placeholder id preserved", got)
	}
	config := dataSources[0]["config"].(map[string]interface{})
	if got := config["deviceId"]; got != "device-1" {
		t.Fatalf("deviceId = %v, want device-1", got)
	}
	if _, exists := config["deviceBinding"]; exists {
		t.Fatal("deviceBinding placeholder was not removed")
	}
}

func TestValidateTemplateDeviceBindingsRequiresAllRequiredRoles(t *testing.T) {
	service := &DashboardTemplateService{}
	_, err := service.validateTemplateDeviceBindings(
		context.Background(),
		"tenant-1",
		[]*model.LocalDashboardTemplateBinding{
			{
				BindingKey:            "temperature_sensor",
				Required:              true,
				LocalDeviceTemplateID: "template-1",
			},
		},
		nil,
	)
	if err == nil {
		t.Fatal("expected missing required binding error")
	}
}

func TestValidateTemplateDeviceBindingsRejectsUnknownRoleBeforeDeviceLookup(t *testing.T) {
	service := &DashboardTemplateService{}
	_, err := service.validateTemplateDeviceBindings(
		context.Background(),
		"tenant-1",
		[]*model.LocalDashboardTemplateBinding{
			{
				BindingKey:            "temperature_sensor",
				Required:              true,
				LocalDeviceTemplateID: "template-1",
			},
		},
		[]model.DeviceBindingInput{
			{BindingKey: "unknown", LocalDeviceID: "device-1"},
		},
	)
	if err == nil {
		t.Fatal("expected unknown binding error")
	}
}
