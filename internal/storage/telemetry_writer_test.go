package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func telemetryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&TelemetryData{}, &TelemetryCurrentData{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTelemetryBatchInsertChunksLargeFlush(t *testing.T) {
	db := telemetryTestDB(t)
	history := make([]TelemetryData, 13000)
	current := make([]TelemetryCurrentData, 6552)
	for i := range history {
		history[i] = TelemetryData{DeviceID: fmt.Sprintf("device-%05d", i), Key: "power", TS: int64(i + 1), TenantID: "tenant"}
	}
	for i := range current {
		current[i] = TelemetryCurrentData{DeviceID: fmt.Sprintf("device-%05d", i), Key: "power", TS: time.UnixMilli(int64(i + 1)), TenantID: "tenant"}
	}
	written, failed := (&telemetryWriter{db: db}).batchInsert(history, current)
	if written != len(history) || failed != 0 {
		t.Fatalf("written=%d failed=%d", written, failed)
	}
	var historyCount, currentCount int64
	if db.Model(&TelemetryData{}).Count(&historyCount).Error != nil || db.Model(&TelemetryCurrentData{}).Count(&currentCount).Error != nil {
		t.Fatal("count telemetry rows")
	}
	if historyCount != 13000 || currentCount != 6552 {
		t.Fatalf("history=%d current=%d", historyCount, currentCount)
	}
}

func TestTelemetryFallbackHandlesDifferentSliceLengths(t *testing.T) {
	db := telemetryTestDB(t)
	history := make([]TelemetryData, 10)
	current := make([]TelemetryCurrentData, 5)
	for i := range history {
		history[i] = TelemetryData{DeviceID: fmt.Sprintf("device-%02d", i), Key: "power", TS: int64(i + 1), TenantID: "tenant"}
	}
	for i := range current {
		current[i] = TelemetryCurrentData{DeviceID: fmt.Sprintf("device-%02d", i), Key: "power", TS: time.UnixMilli(int64(i + 1)), TenantID: "tenant"}
	}
	written, failed := (&telemetryWriter{db: db}).fallbackInsert(history, current)
	if written != len(history) || failed != 0 {
		t.Fatalf("written=%d failed=%d", written, failed)
	}
}
