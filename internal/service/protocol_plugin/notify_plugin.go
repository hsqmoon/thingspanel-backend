package protocolplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"project/internal/dal"
	"project/third_party/others/http_client"
)

// 设备配置更新后主动断开设备连接
func DeviceConfigUpdateAndDisconnect(deviceConfigID string, protocolType string, deviceType string) error {

	// 根据协议类型获取协议信息
	servicePlugin, err := dal.GetServicePluginByServiceIdentifier(protocolType)
	if err != nil {
		return err
	}
	// 获取协议插件host:
	_, host, err := dal.GetServicePluginHttpAddressByID(servicePlugin.ID)
	if err != nil {
		return err
	}
	// 通知所有相关网关断开连接
	if deviceType == "3" {
		// 获取已绑定网关的关联的子设备列表
		deviceIDs, err := dal.GetGatewayDevicesBySubDeviceConfigID(deviceConfigID)
		if err != nil {
			return err
		}
		// 断开设备连接
		var disconnectErrors []error
		for _, deviceID := range deviceIDs {
			if err := DisconnectDevice(deviceID, host); err != nil {
				disconnectErrors = append(disconnectErrors, fmt.Errorf("disconnect gateway %s: %w", deviceID, err))
			}
		}
		return errors.Join(disconnectErrors...)
	} else if deviceType == "1" || deviceType == "2" {
		// 根据设备配置ID获取设备列表
		devices, err := dal.GetDevicesByDeviceConfigID(deviceConfigID)
		if err != nil {
			return err
		}
		// 断开设备连接
		var disconnectErrors []error
		for _, device := range devices {
			if err := DisconnectDevice(device.ID, host); err != nil {
				disconnectErrors = append(disconnectErrors, fmt.Errorf("disconnect device %s: %w", device.ID, err))
			}
		}
		return errors.Join(disconnectErrors...)
	}
	return nil

}

// 通知协议插件
func DisconnectDevice(deviceID string, httpAddress string) (err error) {
	type ReqData struct {
		DeviceID string `json:"device_id"`
	}
	reqData := ReqData{DeviceID: deviceID}
	reqDataBytes, err := json.Marshal(reqData)
	if err != nil {
		return err
	}
	rsp, err := http_client.DisconnectDevice(reqDataBytes, httpAddress)
	if err != nil {
		return fmt.Errorf("disconnect protocol plugin device: %w", err)
	}
	defer func() {
		if closeErr := rsp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close protocol plugin response: %w", closeErr))
		}
	}()
	if rsp.StatusCode != http.StatusOK {
		return fmt.Errorf("protocol plugin disconnect returned HTTP %s", rsp.Status)
	}
	//解析返回数据
	var rspData http_client.RspData
	err = json.NewDecoder(rsp.Body).Decode(&rspData)
	if err != nil {
		return fmt.Errorf("decode protocol plugin disconnect response: %w", err)
	}
	if rspData.Code != http.StatusOK {
		return fmt.Errorf("protocol plugin rejected disconnect: code=%d message=%s", rspData.Code, rspData.Message)
	}
	return nil
}

// 根据设备ID通知协议插件
// 修改设备调用
// 删除设备调用
// 新增网关子设备的时候使用（deviceID送网关设备ID）
// 移除网关子设备调用
func DisconnectDeviceByDeviceID(deviceID string) error {
	// 获取设备信息
	device, err := dal.GetDeviceByID(deviceID)
	if err != nil {
		return err
	}
	if device.DeviceConfigID == nil {
		return nil
	}
	// 获取设备配置
	deviceConfig, err := dal.GetDeviceConfigByID(*device.DeviceConfigID)
	if err != nil {
		return err
	}
	if deviceConfig == nil {
		return nil
	}
	if deviceConfig.ProtocolType == nil {
		return fmt.Errorf("protocol type not found")
	}
	if *deviceConfig.ProtocolType == "MQTT" {
		return nil
	}
	// 根据协议类型获取协议信息
	servicePlugin, err := dal.GetServicePluginByServiceIdentifier(*deviceConfig.ProtocolType)
	if err != nil {
		return err
	}
	// 获取协议插件host:
	_, host, err := dal.GetServicePluginHttpAddressByID(servicePlugin.ID)
	if err != nil {
		return err
	}
	// 断开设备连接
	if deviceConfig.DeviceType == "3" {
		err = DisconnectDevice(*device.ParentID, host)
		if err != nil {
			return err
		}
	} else {
		err = DisconnectDevice(deviceID, host)
		if err != nil {
			return err
		}
	}
	return nil
}
