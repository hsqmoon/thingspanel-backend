package repo

import (
	"context"
	"errors"
	"testing"

	"project/internal/model"
	"project/pkg/global"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupMarketInstallRepoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&model.MarketBundleInstallation{},
		&model.MarketResourceMapping{},
		&model.MarketBundleBindingStatus{},
		&model.MarketInstallationAudit{},
		&model.MarketInstallNotificationOutbox{},
	); err != nil {
		t.Fatalf("migrate market installation tables: %v", err)
	}
	previous := global.DB
	global.DB = db
	t.Cleanup(func() { global.DB = previous })
	return db
}

func TestFinalizeWithNotificationRollsBackFinalStateWhenOutboxWriteFails(t *testing.T) {
	db := setupMarketInstallRepoTestDB(t)
	repository := NewMarketBundleInstallRepo()
	installation, err := repository.CreateInstallation(context.Background(), &model.MarketBundleInstallation{
		TenantID: "tenant-1",
		Status:   model.InstallStateDashboardsCreated,
	})
	if err != nil {
		t.Fatalf("create installation: %v", err)
	}
	injectCreateFailure(t, db, "MarketInstallNotificationOutbox")
	err = repository.FinalizeWithNotification(
		context.Background(),
		installation.ID,
		model.InstallStateCompleted,
		&model.MarketInstallNotificationOutbox{TenantID: "tenant-1", MarketToken: "secret"},
		&model.MarketInstallationAudit{TenantID: "tenant-1", Action: "completed"},
	)
	if err == nil {
		t.Fatal("expected outbox write failure")
	}
	reloaded, err := repository.GetByID(context.Background(), installation.ID)
	if err != nil {
		t.Fatalf("reload installation: %v", err)
	}
	if reloaded.Status != model.InstallStateDashboardsCreated {
		t.Fatalf("status = %s, want rollback to %s", reloaded.Status, model.InstallStateDashboardsCreated)
	}
}

func TestConcurrentInstallationCreateLeavesOneIdempotencyOwner(t *testing.T) {
	db := setupMarketInstallRepoTestDB(t)
	if err := db.Exec("CREATE UNIQUE INDEX install_idempotency_test_uq ON market_bundle_installations(idempotency_key, tenant_id)").Error; err != nil {
		t.Fatalf("create unique index: %v", err)
	}
	repository := NewMarketBundleInstallRepo()
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := repository.CreateInstallationWithAudit(
				context.Background(),
				&model.MarketBundleInstallation{IdempotencyKey: "same", RequestHash: "hash", TenantID: "tenant-1", Status: model.InstallStateDownloading},
				&model.MarketInstallationAudit{TenantID: "tenant-1", Action: "started"},
			)
			results <- err
		}()
	}
	close(start)
	failures := 0
	for i := 0; i < 2; i++ {
		if <-results != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("create failures = %d, want exactly 1 unique conflict", failures)
	}
	var count int64
	if err := db.Model(&model.MarketBundleInstallation{}).Count(&count).Error; err != nil {
		t.Fatalf("count installations: %v", err)
	}
	if count != 1 {
		t.Fatalf("installation count = %d, want 1", count)
	}
}

func injectCreateFailure(t *testing.T, db *gorm.DB, schemaName string) {
	t.Helper()
	callbackName := "test:fail-create:" + schemaName
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == schemaName {
			tx.AddError(errors.New("injected " + schemaName + " create failure"))
		}
	}); err != nil {
		t.Fatalf("register create failure: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
}

func TestCreateInstallationWithAuditRollsBackWhenAuditFails(t *testing.T) {
	db := setupMarketInstallRepoTestDB(t)
	injectCreateFailure(t, db, "MarketInstallationAudit")
	repository := NewMarketBundleInstallRepo()

	_, err := repository.CreateInstallationWithAudit(
		context.Background(),
		&model.MarketBundleInstallation{TenantID: "tenant-1", Status: model.InstallStateDownloading},
		&model.MarketInstallationAudit{TenantID: "tenant-1", Action: "install_started"},
	)
	if err == nil {
		t.Fatal("expected audit failure")
	}
	var count int64
	if err := db.Model(&model.MarketBundleInstallation{}).Count(&count).Error; err != nil {
		t.Fatalf("count installations: %v", err)
	}
	if count != 0 {
		t.Fatalf("installation count = %d, want 0 after rollback", count)
	}
}

func TestUpdateStatusWithAuditsRollsBackStateWhenAuditFails(t *testing.T) {
	db := setupMarketInstallRepoTestDB(t)
	repository := NewMarketBundleInstallRepo()
	installation, err := repository.CreateInstallation(context.Background(), &model.MarketBundleInstallation{
		TenantID: "tenant-1",
		Status:   model.InstallStateDownloading,
	})
	if err != nil {
		t.Fatalf("create installation: %v", err)
	}
	injectCreateFailure(t, db, "MarketInstallationAudit")

	err = repository.UpdateStatusWithAudits(
		context.Background(),
		installation.ID,
		model.InstallStateDownloaded,
		"",
		"",
		&model.MarketInstallationAudit{TenantID: "tenant-1", Action: "state_change"},
	)
	if err == nil {
		t.Fatal("expected audit failure")
	}
	reloaded, err := repository.GetByID(context.Background(), installation.ID)
	if err != nil {
		t.Fatalf("reload installation: %v", err)
	}
	if reloaded.Status != model.InstallStateDownloading {
		t.Fatalf("status = %s, want %s after rollback", reloaded.Status, model.InstallStateDownloading)
	}
}

func TestCreateDashboardRecordsRollsBackAllRecordsWhenBindingFails(t *testing.T) {
	db := setupMarketInstallRepoTestDB(t)
	injectCreateFailure(t, db, "MarketBundleBindingStatus")
	repository := NewMarketBundleInstallRepo()

	err := repository.CreateDashboardRecords(
		context.Background(),
		&model.MarketResourceMapping{InstallationID: "install-1", TenantID: "tenant-1", ResourceType: model.ResourceTypeDashboard},
		&model.MarketInstallationAudit{TenantID: "tenant-1", Action: "resource_created"},
		[]*model.MarketBundleBindingStatus{{InstallationID: "install-1", BindingKey: "sensor"}},
	)
	if err == nil {
		t.Fatal("expected binding failure")
	}
	for name, target := range map[string]interface{}{
		"mappings": &model.MarketResourceMapping{},
		"audits":   &model.MarketInstallationAudit{},
		"bindings": &model.MarketBundleBindingStatus{},
	} {
		var count int64
		if err := db.Model(target).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want 0 after rollback", name, count)
		}
	}
}

func TestUpdateWarningsRejectsMissingInstallation(t *testing.T) {
	setupMarketInstallRepoTestDB(t)
	err := NewMarketBundleInstallRepo().UpdateWarnings(context.Background(), "missing", []string{"warning"})
	if err == nil {
		t.Fatal("expected missing installation error")
	}
}
