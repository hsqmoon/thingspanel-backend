package http_client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNotificationUsesCanonicalPluginEndpoint(t *testing.T) {
	t.Parallel()

	const (
		messageType = "1"
		message     = `{"service_access_id":"access-1"}`
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/api/v1/plugin/notification" {
			t.Errorf("unexpected notification path: %s", r.URL.Path)
		}

		var req struct {
			MessageType string `json:"message_type"`
			Message     string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode notification request: %v", err)
		}
		if req.MessageType != messageType {
			t.Errorf("unexpected message_type: %s", req.MessageType)
		}
		if req.Message != message {
			t.Errorf("unexpected message: %s", req.Message)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	if _, err := NotificationWithContext(context.Background(), messageType, message, host); err != nil {
		t.Fatalf("NotificationWithContext returned error: %v", err)
	}
}

func TestNotificationRejectsBusinessFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":500,"message":"reload failed"}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	_, err := NotificationWithContext(context.Background(), "1", `{}`, host)
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("expected business acknowledgement failure, got %v", err)
	}
}

func TestGetPluginFromConfigV2RejectsBusinessFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":500,"message":"invalid plugin configuration","data":{"unsafe":true}}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	data, err := GetPluginFromConfigV2(host, "service", "", "SVCR")
	if err == nil {
		t.Fatalf("expected business failure, got data=%v err=%v", data, err)
	}
	if data != nil {
		t.Fatalf("business failure must not expose response data: %v", data)
	}
}

func TestGetPluginFromConfigV2TimesOutWhenPluginStopsResponding(t *testing.T) {
	previousClient := pluginHTTPClient
	pluginHTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	t.Cleanup(func() { pluginHTTPClient = previousClient })

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	started := time.Now()
	_, err := GetPluginFromConfigV2(host, "service", "", "SVCR")
	if err == nil {
		t.Fatal("expected plugin form timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("plugin form timeout took too long: %s", elapsed)
	}
}

func TestGetRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", int(maxPluginHTTPResponseBytes+1)))
	}))
	defer server.Close()

	_, err := Get(server.URL)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected response-size failure, got %v", err)
	}
}

func TestGetServiceAccessDeviceListEncodesVoucher(t *testing.T) {
	t.Parallel()
	const voucher = `{"token":"a+b&c=d 中文"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/plugin/device/list" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if actual := r.URL.Query().Get("voucher"); actual != voucher {
			t.Errorf("voucher was not encoded safely: %q", actual)
		}
		if r.URL.Query().Get("page_size") != "10" || r.URL.Query().Get("page") != "2" {
			t.Errorf("unexpected pagination: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":0,"list":[]}}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	data, err := GetServiceAccessDeviceList(context.Background(), host, voucher, "10", "2")
	if err != nil {
		t.Fatalf("GetServiceAccessDeviceList returned error: %v", err)
	}
	if data == nil || data.Total != 0 || data.List == nil {
		t.Fatalf("unexpected data: %#v", data)
	}
}

func TestGetServiceAccessDeviceListDoesNotExposeVoucherOnTransportFailure(t *testing.T) {
	t.Parallel()
	const voucher = `{"token":"must-not-leak"}`
	_, err := GetServiceAccessDeviceList(context.Background(), "127.0.0.1:1", voucher, "10", "1")
	if err == nil {
		t.Fatal("expected transport failure")
	}
	if strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "voucher") {
		t.Fatalf("transport error exposed credentials: %v", err)
	}
}

func TestGetServiceAccessDeviceListCancelsUnresponsiveServer(t *testing.T) {
	t.Parallel()
	requestAccepted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestAccepted)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	host := strings.TrimPrefix(server.URL, "http://")
	_, err := GetServiceAccessDeviceList(ctx, host, `{}`, "10", "1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("request cancellation took too long: %s", elapsed)
	}
	select {
	case <-requestAccepted:
	default:
		t.Fatal("server did not accept request before cancellation")
	}
}

func TestGetServiceAccessDeviceListRejectsOversizedResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", int(maxServiceAccessDeviceListResponseBytes+1)))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	_, err := GetServiceAccessDeviceList(context.Background(), host, `{}`, "10", "1")
	if err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("expected response size limit error, got %v", err)
	}
}
