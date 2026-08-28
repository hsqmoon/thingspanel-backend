package http_client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"project/pkg/errcode"

	"github.com/sirupsen/logrus"
)

/*
- 有子设备关联的设备配置不能更换协议类型
*/

type RspData struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

type RspDeviceListData struct {
	Code    int      `json:"code"`
	Message string   `json:"message"`
	Data    ListData `json:"data"`
}
type ListData struct {
	Total int          `json:"total"`
	List  []DeviceData `json:"list"`
}
type DeviceData struct {
	DeviceName     string `json:"device_name"`
	DeviceNumber   string `json:"device_number"`
	Description    string `json:"description"`
	IsBind         bool   `json:"is_bind"`
	DeviceConfigID string `json:"device_config_id"`
}

const maxServiceAccessDeviceListResponseBytes int64 = 4 << 20

var serviceAccessDeviceListHTTPClient = &http.Client{Timeout: 10 * time.Second}

// /api/v2/form/config
// CFG-配置表单 VCR-凭证表单 VCRT-凭证类型表单 SVCRT-服务凭证表单
func GetPluginFromConfigV2(host string, service_identifier string, device_type string, form_type string) (interface{}, error) {
	b, err := Get("http://" + host + "/api/v1/form/config?protocol_type=" + service_identifier + "&device_type=" + device_type + "&form_type=" + form_type)
	if err != nil {
		logrus.Error(err)
		// 判断是否为连接被拒绝的错误
		if err.Error() != "" && (strings.Contains(err.Error(), "connection refused")) {
			return nil, errcode.WithData(200068, err.Error())
		}
		return nil, errcode.WithData(200069, err.Error())
	}
	// 解析表单
	var rspdata RspData
	err = json.Unmarshal(b, &rspdata)
	if err != nil {
		logrus.Error(err)
		return nil, errcode.WithData(200070, err.Error())
	}
	if rspdata.Code != 200 {
		err = errcode.NewWithMessage(200070, rspdata.Message)
		logrus.Error(err)
		return nil, err
	}
	return rspdata.Data, nil
}

// 断开设备连接让设备重新连接
func DisconnectDevice(reqdata []byte, host string) (*http.Response, error) {
	return PostJson("http://"+host+"/api/v1/device/disconnect", reqdata)
}

func NotificationWithContext(ctx context.Context, messageType string, message string, host string) ([]byte, error) {
	type ReqData struct {
		MessageType string `json:"message_type"`
		Message     string `json:"message"`
	}
	reqData := ReqData{MessageType: messageType, Message: message}
	reqDataBytes, err := json.Marshal(reqData)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host+"/api/v1/plugin/notification", bytes.NewReader(reqDataBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		logrus.Error(err)
		return nil, fmt.Errorf("post plugin notification failed: %s", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		err = fmt.Errorf("protocol plugin response message: %s", response.Status)
		logrus.Error(err)
		return nil, err

	}
	// 读取body
	body, err := io.ReadAll(response.Body)
	if err != nil {
		logrus.Error(err)
		return nil, fmt.Errorf("read plugin response body failed: %s", err)
	}
	var acknowledgement RspData
	if err := json.Unmarshal(body, &acknowledgement); err != nil {
		return nil, fmt.Errorf("decode plugin notification acknowledgement failed: %w", err)
	}
	if acknowledgement.Code != http.StatusOK {
		return nil, fmt.Errorf("plugin notification rejected: code=%d message=%s", acknowledgement.Code, acknowledgement.Message)
	}
	logrus.Info(string(body))

	return body, nil
}

// /api/v1/service/access/device/list
// 三方服务列表查询
func GetServiceAccessDeviceList(ctx context.Context, host string, voucher string, page_size string, page string) (*ListData, error) {
	endpoint := url.URL{Scheme: "http", Host: host, Path: "/api/v1/plugin/device/list"}
	query := endpoint.Query()
	query.Set("voucher", voucher)
	query.Set("page_size", page_size)
	query.Set("page", page)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("create plugin device list request failed")
	}
	response, err := serviceAccessDeviceListHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("plugin device list request timed out: %w", context.DeadlineExceeded)
		}
		return nil, errors.New("plugin device list request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugin device list request failed with status %s", response.Status)
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, maxServiceAccessDeviceListResponseBytes+1))
	if err != nil {
		return nil, errors.New("read plugin device list response failed")
	}
	if int64(len(b)) > maxServiceAccessDeviceListResponseBytes {
		return nil, errors.New("plugin device list response exceeds size limit")
	}
	// 解析表单
	var rspdata RspDeviceListData
	err = json.Unmarshal(b, &rspdata)
	if err != nil {
		logrus.Error(err)
		return nil, fmt.Errorf("unmarshal response data failed: %s", err)
	}
	if rspdata.Code != 200 {
		err = fmt.Errorf("protocol plugin response message: %s", rspdata.Message)
		logrus.Error(err)
		return nil, err
	}
	// 如果rspdata.Data 为空，返回空数组
	if rspdata.Data.List == nil {
		rspdata.Data.List = []DeviceData{}
	}
	return &rspdata.Data, nil
}
