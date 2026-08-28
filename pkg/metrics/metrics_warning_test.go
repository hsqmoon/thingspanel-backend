package metrics

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type failingHistoryStorage struct{}

func (failingHistoryStorage) SaveMetrics(time.Time, float64, float64, float64) error {
	return errors.New("injected history failure")
}

func (failingHistoryStorage) GetHistoryData(string, time.Duration) ([]MetricDataPoint, error) {
	return nil, nil
}

func (failingHistoryStorage) GetCurrentData() (*SystemMetrics, error) {
	return nil, nil
}

func TestMetricsHistoryFailureIsCounted(t *testing.T) {
	metrics := NewMetrics(fmt.Sprintf("test_metrics_%d", time.Now().UnixNano()))
	metrics.SetHistoryStorage(failingHistoryStorage{})
	metrics.saveMetricsHistory(time.Now(), 1, 2, 3)
	if got := testutil.ToFloat64(metrics.HistorySaveErrors); got != 1 {
		t.Fatalf("history save errors = %v, want 1", got)
	}
}
