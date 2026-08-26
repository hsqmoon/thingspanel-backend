package middleware

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactOperationLogMessageRecursively(t *testing.T) {
	message := `{"password":"plain-password","data":{"accessToken":"jwt-value","items":[{"voucher":{"username":"device","password":"device-secret"}}],"name":"keep-me"}}`
	redacted := redactOperationLogMessage(message)

	for _, secret := range []string{"plain-password", "jwt-value", "device-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("secret %q remains in %s", secret, redacted)
		}
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(redacted), &payload); err != nil {
		t.Fatalf("redacted JSON is invalid: %v", err)
	}
	if payload["password"] != "[REDACTED]" || payload["data"].(map[string]interface{})["name"] != "keep-me" {
		t.Fatalf("unexpected redacted payload: %#v", payload)
	}
}

func TestRedactOperationLogMessageHandlesNonJSON(t *testing.T) {
	redacted := redactOperationLogMessage("password=plain&authorization=Bearer eyJ.secret.value")
	if strings.Contains(redacted, "plain") || strings.Contains(redacted, "eyJ.secret.value") {
		t.Fatalf("raw secret remains in %q", redacted)
	}
}

func TestRedactOperationLogMessageLimitsStoredSizeAfterRedaction(t *testing.T) {
	message := `{"password":"secret","data":"` + strings.Repeat("x", operationLogMessageLimit+100) + `"}`
	redacted := redactOperationLogMessage(message)
	if strings.Contains(redacted, "secret") {
		t.Fatal("secret remains in truncated message")
	}
	if len(redacted) > operationLogMessageLimit+len("...[内容过长已截断]") {
		t.Fatalf("message was not limited: %d", len(redacted))
	}
}
