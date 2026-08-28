package protocolplugin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisconnectDevicePropagatesPluginFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{name: "HTTP failure", statusCode: http.StatusBadGateway, body: `{}`, wantError: "HTTP 502 Bad Gateway"},
		{name: "business failure", statusCode: http.StatusOK, body: `{"code":500,"message":"disconnect failed"}`, wantError: "code=500 message=disconnect failed"},
		{name: "malformed response", statusCode: http.StatusOK, body: `{`, wantError: "decode protocol plugin disconnect response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/device/disconnect" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			err := DisconnectDevice("device-1", strings.TrimPrefix(server.URL, "http://"))
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("expected %q error, got %v", tt.wantError, err)
			}
		})
	}
}

func TestDisconnectDeviceAcceptsSuccessfulAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"message":"ok"}`))
	}))
	defer server.Close()

	if err := DisconnectDevice("device-1", strings.TrimPrefix(server.URL, "http://")); err != nil {
		t.Fatalf("DisconnectDevice returned error: %v", err)
	}
}
