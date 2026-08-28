package http_client

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
)

const maxPluginHTTPResponseBytes int64 = 4 << 20

var pluginHTTPClient = &http.Client{Timeout: 10 * time.Second}

// Get 发送get请求
func Get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := pluginHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET failed with status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPluginHTTPResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxPluginHTTPResponseBytes {
		return nil, fmt.Errorf("GET response exceeds %d bytes", maxPluginHTTPResponseBytes)
	}
	return body, nil
}

// PostWithHeader 发送带有header的post请求
func PostJson(targetUrl string, payload []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", targetUrl, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", "application/json")
	response, err := pluginHTTPClient.Do(req)
	return response, err
}

func generateHMAC(message, secret string) string {
	key := []byte(secret)
	h := hmac.New(sha256.New, key)
	h.Write([]byte(message))
	signature := h.Sum(nil)
	return hex.EncodeToString(signature)
}

// SendSignedRequestWithTimeout 发送带签名和超时的请求
func SendSignedRequestWithTimeout(ctx context.Context, url, message, secret string) error {
	signature := generateHMAC(message, secret)

	// Creating the request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(message))
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	// Adding the signature to the request header
	req.Header.Set("X-Signature-256", "sha256="+signature)
	req.Header.Set("Content-Type", "application/json")

	// Sending the request
	resp, err := pluginHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 检查HTTP状态码
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP请求失败，状态码: %d", resp.StatusCode)
	}

	logrus.Info("Webhook请求已发送，状态码:", resp.StatusCode)
	return nil
}
